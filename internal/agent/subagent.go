package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/toddky/menehune/internal/llm"
)

// spawnTimeout bounds the initialize and session/new round-trips at spawn.
// Local process startup plus two replies; defaultToolTimeout (10s) is ample.
const spawnTimeout = defaultToolTimeout

// killGrace is how long Kill waits after closing stdin before force-killing.
// 2s: the child exits on stdin EOF, so the grace only covers its cleanup.
const killGrace = 2 * time.Second

// Subagent is a child menehune process driven over the ACP subset
// (JSON-RPC 2.0 over stdio, one JSON object per line). The parent talks to
// it through a synthetic subagent_<name> tool dispatched inside Turn.
type Subagent struct {
	// Name is the bundled agent under agents/<name> the child loads.
	Name string
	// execPath overrides the child binary; tests point it at a fake script.
	execPath string

	cmd       *exec.Cmd
	stdin     io.WriteCloser
	scanner   *bufio.Scanner
	sessionID string
	nextID    int
}

// rpcFrame is any incoming line: a response (ID+Result/Error) or a notification (Method+Params).
type rpcFrame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// sessionUpdate is the update payload inside a session/update notification.
// Only the fields this parent renders are parsed.
type sessionUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	Content       struct {
		Text string `json:"text"`
	} `json:"content"`
	ToolCallID string `json:"toolCallId"`
	Status     string `json:"status"`
	RawInput   string `json:"rawInput"`
	RawOutput  string `json:"rawOutput"`
}

// toolName is the synthetic tool the model calls; hyphens become underscores
// to keep the name a plain identifier (jira-scout -> subagent_jira_scout).
func (s *Subagent) toolName() string {
	return "subagent_" + strings.ReplaceAll(s.Name, "-", "_")
}

// Definition builds the tool definition advertised to the model.
func (s *Subagent) Definition() llm.ToolDef {
	description := fmt.Sprintf(
		"Delegate a task to the %q subagent, which runs with its own restricted tools and instructions. It keeps context from earlier calls in this session, so follow-ups can reference prior answers.",
		s.Name)
	return llm.ToolDef{
		Name:        s.toolName(),
		Description: description,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"prompt": {"type": "string", "description": "The task or question for the subagent"}
			},
			"required": ["prompt"]
		}`),
	}
}

// Spawn starts the child process and completes the initialize and
// session/new handshakes. Call spawns lazily, so frontends never call this.
func (s *Subagent) Spawn() error {
	executable := s.execPath
	if executable == "" {
		selfPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("subagent %s: resolve own binary: %w", s.Name, err)
		}
		executable = selfPath
	}

	cmd := exec.Command(executable, "--agent", s.Name, "--acp")
	// The child's failures should reach the user's terminal, not vanish;
	// inheriting stderr also avoids a full-pipe deadlock.
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("subagent %s: stdin pipe: %w", s.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("subagent %s: stdout pipe: %w", s.Name, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("subagent %s: start child: %w", s.Name, err)
	}
	s.cmd = cmd
	s.stdin = stdin
	s.scanner = bufio.NewScanner(stdout)
	// Same 16MiB line cap the ACP frontend uses for large tool output.
	s.scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	if _, err := s.roundTrip("initialize", map[string]int{"protocolVersion": 1}, spawnTimeout, nil); err != nil {
		s.Kill()
		return err
	}
	result, err := s.roundTrip("session/new", struct{}{}, spawnTimeout, nil)
	if err != nil {
		s.Kill()
		return err
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(result, &session); err != nil || session.SessionID == "" {
		s.Kill()
		return fmt.Errorf("subagent %s: session/new returned no sessionId in %s", s.Name, result)
	}
	s.sessionID = session.SessionID
	return nil
}

// Call sends one prompt and returns the subagent's answer text.
// Tool activity streamed by the child is forwarded through notify; the same
// sessionId is reused every call so the child keeps its conversation context.
func (s *Subagent) Call(prompt string, notify func(Event)) (string, error) {
	if s.cmd == nil {
		if err := s.Spawn(); err != nil {
			return "", err
		}
	}

	params := map[string]interface{}{
		"sessionId": s.sessionID,
		"prompt":    []map[string]string{{"type": "text", "text": prompt}},
	}
	var answer strings.Builder
	// A subagent turn can run many tool calls; cap it like the largest tool budget.
	_, err := s.roundTrip("session/prompt", params, maxToolTimeout, func(update sessionUpdate) {
		switch update.SessionUpdate {
		case "agent_message_chunk":
			answer.WriteString(update.Content.Text)
		case "tool_call":
			notify(Event{
				Type:      EventToolCallStarted,
				ToolName:  s.Name + "/" + update.ToolCallID,
				ToolInput: update.RawInput,
			})
		case "tool_call_update":
			notify(Event{
				Type:     EventToolResult,
				ToolName: s.Name + "/" + update.ToolCallID,
				Result:   update.RawOutput,
				IsError:  update.Status == "failed",
			})
		}
	})
	if err != nil {
		return "", err
	}
	return answer.String(), nil
}

// Kill closes the child's stdin so it exits on EOF, then force-kills after killGrace.
func (s *Subagent) Kill() {
	if s.cmd == nil {
		return
	}
	s.stdin.Close()
	done := make(chan struct{})
	go func() {
		s.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(killGrace):
		s.cmd.Process.Kill()
		<-done
	}
	s.cmd = nil
}

// roundTrip sends one request and reads frames until its response arrives.
// session/update notifications seen in between are handed to onUpdate when non-nil.
func (s *Subagent) roundTrip(method string, params interface{}, timeout time.Duration, onUpdate func(sessionUpdate)) (json.RawMessage, error) {
	s.nextID++
	requestID := s.nextID
	request := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      requestID,
		"method":  method,
		"params":  params,
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("subagent %s: marshal %s request: %w", s.Name, method, err)
	}
	payload = append(payload, '\n')
	if _, err := s.stdin.Write(payload); err != nil {
		return nil, fmt.Errorf("subagent %s: write %s request: %w", s.Name, method, err)
	}

	// The watchdog kills the child on deadline, which unblocks the scanner with EOF.
	watchdog := time.AfterFunc(timeout, func() { s.cmd.Process.Kill() })
	defer watchdog.Stop()

	for s.scanner.Scan() {
		line := s.scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var frame rpcFrame
		if err := json.Unmarshal(line, &frame); err != nil {
			return nil, fmt.Errorf("subagent %s: bad frame %q: %w", s.Name, line, err)
		}

		if frame.Method == "session/update" && onUpdate != nil {
			var updateParams struct {
				Update sessionUpdate `json:"update"`
			}
			if err := json.Unmarshal(frame.Params, &updateParams); err == nil {
				onUpdate(updateParams.Update)
			}
			continue
		}

		var responseID int
		if frame.ID != nil && json.Unmarshal(frame.ID, &responseID) == nil && responseID == requestID {
			if frame.Error != nil {
				return nil, fmt.Errorf("subagent %s: %s failed (code %d): %s", s.Name, method, frame.Error.Code, frame.Error.Message)
			}
			return frame.Result, nil
		}
	}
	if !watchdog.Stop() {
		return nil, fmt.Errorf("subagent %s: %s timed out after %s; child killed", s.Name, method, timeout)
	}
	return nil, fmt.Errorf("subagent %s: child closed its pipe during %s (crashed?)", s.Name, method)
}
