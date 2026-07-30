package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/toddky/menehune/internal/llm"
)

// ResponseStreamer is the slice of the LLM client the agent loop needs; tests substitute a fake.
type ResponseStreamer interface {
	CompleteStream(messages []llm.Message, tools []llm.ToolDef, onText func(string)) (*llm.Response, error)
}

type Agent struct {
	Client ResponseStreamer
	Tools  *Registry
	// ToolsDirs are the source tool dirs Setup linked from, kept so ReloadTools
	// can re-link and rebuild the registry without restarting the process.
	ToolsDirs []string
	// SystemPrompt is sent as a system message ahead of the history when non-empty.
	SystemPrompt string
	// AllowExit advertises the internal exit tool so the model can terminate the agent process.
	AllowExit bool
	// Subagents maps each subagent's tool name (subagent_<name>) to its child
	// process handle; Turn dispatches these internally, like the exit tool.
	Subagents map[string]*Subagent
}

// AttachSubagent registers a subagent under its tool name so the next Turn
// advertises it. Frontends call this for --subagent flags and REPL &name.
func (a *Agent) AttachSubagent(sub *Subagent) {
	if a.Subagents == nil {
		a.Subagents = map[string]*Subagent{}
	}
	a.Subagents[sub.toolName()] = sub
}

// KillSubagents terminates every attached subagent child process.
func (a *Agent) KillSubagents() {
	for _, sub := range a.Subagents {
		sub.Kill()
	}
}

// ReloadTools re-links the source tool dirs and rebuilds the registry so schema
// edits and added or removed tool files take effect without restarting the agent.
// The live registry is swapped only on success, so a failed reload leaves the
// working tool set intact. It returns the tool names added and removed since the
// last load, each sorted. Turn re-reads Definitions() every turn, so the swapped
// registry takes effect on the next prompt with no further wiring.
func (a *Agent) ReloadTools() (added, removed []string, err error) {
	// An empty ToolsDirs would re-run Setup with no sources and wipe the tool set;
	// refuse loudly rather than silently leaving the agent with zero tools.
	if len(a.ToolsDirs) == 0 {
		return nil, nil, fmt.Errorf("reload tools: no source tool dirs recorded; set Agent.ToolsDirs")
	}

	previous := make(map[string]bool, len(a.Tools.Tools))
	for name := range a.Tools.Tools {
		previous[name] = true
	}

	if err := Setup(a.ToolsDirs...); err != nil {
		return nil, nil, fmt.Errorf("reload tools: %w", err)
	}
	registry, err := LoadAll(filepath.Join(GetRuntimeDir(), "tools"))
	if err != nil {
		return nil, nil, fmt.Errorf("reload tools: %w", err)
	}

	for name := range registry.Tools {
		if !previous[name] {
			added = append(added, name)
		}
		delete(previous, name)
	}
	for name := range previous {
		removed = append(removed, name)
	}
	sort.Strings(added)
	sort.Strings(removed)

	a.Tools = registry
	return added, removed, nil
}

// ExitRequest is returned as the Turn error when the model calls the exit tool.
// Frontends propagate it up so main can os.Exit with Code after deferred cleanup runs.
type ExitRequest struct {
	Code   int    `json:"code"`
	Reason string `json:"reason"`
}

func (e *ExitRequest) Error() string {
	return fmt.Sprintf("agent exit requested (code %d): %s", e.Code, e.Reason)
}

// exitToolName is the internal tool advertised when AllowExit is set.
// It is dispatched inside Turn, never exec'd from the tools dir.
const exitToolName = "exit"

var exitToolDef = llm.ToolDef{
	Name:        exitToolName,
	Description: "Terminate the agent process with the given exit code. Call this when asked to end with a status, e.g. pass/fail checks. The agent stops immediately; no further tools run and no more text is read.",
	InputSchema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"code": {"type": "integer", "description": "Process exit code, 0-125; 0 means success/pass"},
			"reason": {"type": "string", "description": "One-sentence justification, printed for the user"}
		},
		"required": ["code", "reason"]
	}`),
}

// Turn sends the message history and loops on tool calls until the model stops requesting tools.
// Each event is reported through notify.
// It returns the updated history including assistant turns and tool results.
// If the model calls the exit tool (see AllowExit), Turn stops at once and returns *ExitRequest as the error.
func (a *Agent) Turn(messages []llm.Message, notify func(Event)) ([]llm.Message, error) {
	defs := a.Tools.Definitions()
	if a.AllowExit {
		defs = append(defs, exitToolDef)
	}
	// Sort subagent defs by name so requests stay deterministic, like Definitions().
	subagentNames := make([]string, 0, len(a.Subagents))
	for name := range a.Subagents {
		subagentNames = append(subagentNames, name)
	}
	sort.Strings(subagentNames)
	for _, name := range subagentNames {
		defs = append(defs, a.Subagents[name].Definition())
	}
	for {
		// The system message is prepended per request, not stored in the returned
		// history, so callers cannot accumulate duplicates across turns.
		messagesCopy := messages
		if a.SystemPrompt != "" {
			messagesCopy = append([]llm.Message{llm.TextMessage("system", a.SystemPrompt)}, messages...)
		}
		response, err := a.Client.CompleteStream(messagesCopy, defs, func(text string) {
			notify(Event{Type: EventTextDelta, Text: text})
		})
		if err != nil {
			notify(Event{Type: EventError, Err: err})
			return messages, err
		}

		messages = append(messages, llm.Message{Role: "assistant", Content: response.Content})
		if response.StopReason != "tool_use" {
			notify(Event{Type: EventTurnComplete})
			return messages, nil
		}

		var results []llm.ContentBlock
		for _, block := range response.Content {
			if block.Type != "tool_use" {
				continue
			}
			notify(Event{
				Type:      EventToolCallStarted,
				ToolName:  block.Name,
				ToolInput: string(block.Input),
			})

			// The exit tool ends the whole process: stop at once, skip remaining calls,
			// and never report a result back to the model.
			if a.AllowExit && block.Name == exitToolName {
				var request ExitRequest
				if err := json.Unmarshal(block.Input, &request); err != nil {
					return messages, fmt.Errorf("exit tool called with invalid input %s: %w", block.Input, err)
				}
				return messages, &request
			}

			output, err := a.runTool(block.Name, block.Input, notify)
			isError := err != nil
			if isError {
				output = err.Error()
			}
			notify(Event{
				Type:     EventToolResult,
				ToolName: block.Name,
				Result:   output,
				IsError:  isError,
			})
			results = append(results, llm.ContentBlock{
				Type:      "tool_result",
				ToolUseID: block.ID,
				Content:   output,
				IsError:   isError,
			})
		}
		messages = append(messages, llm.Message{Role: "user", Content: results})
	}
}

// runTool dispatches one tool call: attached subagents are handled in-process,
// everything else execs a script through the registry.
func (a *Agent) runTool(name string, input json.RawMessage, notify func(Event)) (string, error) {
	sub, isSubagent := a.Subagents[name]
	if !isSubagent {
		return a.Tools.Run(name, input)
	}
	var call struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &call); err != nil || call.Prompt == "" {
		return "", fmt.Errorf("tool %s requires a non-empty \"prompt\" string, got %s", name, input)
	}
	return sub.Call(call.Prompt, notify)
}
