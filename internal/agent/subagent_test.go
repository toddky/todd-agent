package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toddky/todd-agent/internal/llm"
)

// fakeChildScript speaks just enough of the ACP subset to drive Subagent:
// it answers initialize, session/new, and session/prompt by request id, and
// streams one agent_message_chunk before each prompt response. Every received
// line is appended to the log file given as $1 so tests can assert on inputs.
const fakeChildScript = `#!/usr/bin/env bash
log="$1"
while IFS= read -r line; do
	printf '%s\n' "$line" >> "$log"
	id="$(jq -r '.id' <<<"$line")"
	method="$(jq -r '.method' <<<"$line")"
	case "$method" in
	initialize)
		printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":false}}}\n' "$id"
		;;
	session/new)
		printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-1"}}\n' "$id"
		;;
	session/prompt)
		printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer"}}}}\n'
		printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call","toolCallId":"grep","title":"grep","status":"in_progress","rawInput":"{}"}}}\n'
		printf '{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}\n' "$id"
		;;
	esac
done
`

// writeFakeChild writes the fake child plus a wrapper that pins the log path,
// since Subagent always invokes the child as "<path> --agent <name> --acp".
func writeFakeChild(t *testing.T, script string) (childPath, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "requests.log")
	inner := filepath.Join(dir, "inner.sh")
	if err := os.WriteFile(inner, []byte(script), 0o755); err != nil {
		t.Fatalf("write inner script: %v", err)
	}
	childPath = filepath.Join(dir, "child.sh")
	wrapper := "#!/usr/bin/env bash\nexec " + inner + " " + logPath + "\n"
	if err := os.WriteFile(childPath, []byte(wrapper), 0o755); err != nil {
		t.Fatalf("write wrapper script: %v", err)
	}
	return childPath, logPath
}

func TestSubagentCall(t *testing.T) {
	childPath, logPath := writeFakeChild(t, fakeChildScript)
	sub := &Subagent{Name: "fake-scout", execPath: childPath}
	defer sub.Kill()

	var events []Event
	answer, err := sub.Call("do the thing", func(event Event) { events = append(events, event) })
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if answer != "answer" {
		t.Errorf("answer = %q, want %q", answer, "answer")
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (the tool_call)", len(events))
	}
	if events[0].Type != EventToolCallStarted || events[0].ToolName != "fake-scout/grep" {
		t.Errorf("event = %+v, want ToolCallStarted for fake-scout/grep", events[0])
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	for _, method := range []string{"initialize", "session/new", "session/prompt"} {
		if !strings.Contains(string(log), method) {
			t.Errorf("child never received %s; log:\n%s", method, log)
		}
	}
}

func TestSubagentReusesSession(t *testing.T) {
	childPath, logPath := writeFakeChild(t, fakeChildScript)
	sub := &Subagent{Name: "fake-scout", execPath: childPath}
	defer sub.Kill()

	discard := func(Event) {}
	if _, err := sub.Call("first", discard); err != nil {
		t.Fatalf("first Call: %v", err)
	}
	if _, err := sub.Call("second", discard); err != nil {
		t.Fatalf("second Call: %v", err)
	}

	log, _ := os.ReadFile(logPath)
	if got := strings.Count(string(log), `"session/new"`); got != 1 {
		t.Errorf("child saw %d session/new calls, want 1 (session reused)", got)
	}
	if got := strings.Count(string(log), `"sess-1"`); got != 2 {
		t.Errorf("child saw sessionId sess-1 %d times, want 2 (one per prompt)", got)
	}
}

func TestSubagentChildError(t *testing.T) {
	errorScript := `#!/usr/bin/env bash
while IFS= read -r line; do
	id="$(jq -r '.id' <<<"$line")"
	method="$(jq -r '.method' <<<"$line")"
	case "$method" in
	initialize)
		printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1}}\n' "$id"
		;;
	session/new)
		printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-1"}}\n' "$id"
		;;
	session/prompt)
		printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"model exploded"}}\n' "$id"
		;;
	esac
done
`
	childPath, _ := writeFakeChild(t, errorScript)
	sub := &Subagent{Name: "fake-scout", execPath: childPath}
	defer sub.Kill()

	_, err := sub.Call("boom", func(Event) {})
	if err == nil {
		t.Fatal("Call returned nil error, want the child's JSON-RPC error surfaced")
	}
	if !strings.Contains(err.Error(), "model exploded") {
		t.Errorf("error = %q, want it to contain the child's message", err)
	}
}

func TestTurnDispatchesSubagent(t *testing.T) {
	childPath, _ := writeFakeChild(t, fakeChildScript)
	sub := &Subagent{Name: "fake-scout", execPath: childPath}
	defer sub.Kill()

	// Turn 1: the model calls subagent_fake_scout; turn 2: it stops.
	streamer := &scriptedClient{responses: []llm.Response{
		{
			Content: []llm.ContentBlock{{
				Type:  "tool_use",
				ID:    "call-1",
				Name:  "subagent_fake_scout",
				Input: json.RawMessage(`{"prompt":"look it up"}`),
			}},
			StopReason: "tool_use",
		},
		{
			Content:    []llm.ContentBlock{{Type: "text", Text: "done"}},
			StopReason: "end_turn",
		},
	}}
	engine := &Agent{Client: streamer, Tools: &Registry{}}
	engine.AttachSubagent(sub)

	messages, err := engine.Turn([]llm.Message{llm.TextMessage("user", "go")}, func(Event) {})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}

	// The advertised defs must include the subagent tool.
	found := false
	for _, def := range streamer.lastTools {
		if def.Name == "subagent_fake_scout" {
			found = true
		}
	}
	if !found {
		t.Error("subagent_fake_scout was not advertised to the model")
	}

	// The tool_result message must carry the subagent's answer, not a registry error.
	var result string
	for _, message := range messages {
		for _, block := range message.Content {
			if block.Type == "tool_result" && block.ToolUseID == "call-1" {
				result = block.Content
			}
		}
	}
	if result != "answer" {
		t.Errorf("tool_result = %q, want %q from the subagent", result, "answer")
	}
}

// scriptedClient replays canned responses and records the last advertised tool defs.
type scriptedClient struct {
	responses []llm.Response
	callCount int
	lastTools []llm.ToolDef
}

func (s *scriptedClient) CompleteStream(messages []llm.Message, tools []llm.ToolDef, onText func(string)) (*llm.Response, error) {
	s.lastTools = tools
	response := s.responses[s.callCount]
	s.callCount++
	return &response, nil
}
