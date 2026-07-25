package agent

import (
	"encoding/json"
	"fmt"

	"github.com/toddky/todd-agent/internal/llm"
)

type Agent struct {
	Client *llm.Client
	Tools  *Registry
	// AllowExit advertises the internal exit tool so the model can terminate the agent process.
	AllowExit bool
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
	for {
		response, err := a.Client.CompleteStream(messages, defs, func(text string) {
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

			output, err := a.Tools.Run(block.Name, block.Input)
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
