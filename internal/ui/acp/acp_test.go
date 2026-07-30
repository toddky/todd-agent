package acp

import (
	"strings"
	"testing"

	"github.com/toddky/todd-agent/internal/agent"
	"github.com/toddky/todd-agent/internal/llm"
)

// scriptedStreamer replays canned responses in order, streaming each response's
// text blocks through onText first, the way the real client does.
type scriptedStreamer struct {
	responses []llm.Response
	callCount int
}

func (s *scriptedStreamer) CompleteStream(messages []llm.Message, tools []llm.ToolDef, onText func(string)) (*llm.Response, error) {
	response := s.responses[s.callCount]
	s.callCount++
	for _, block := range response.Content {
		if block.Type == "text" {
			onText(block.Text)
		}
	}
	return &response, nil
}

func TestRun(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		responses []llm.Response
		want      []string
	}{
		{
			name:  "initialize",
			input: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
			want: []string{
				`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":false}}}`,
			},
		},
		{
			name:  "session new",
			input: `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp"}}`,
			want: []string{
				`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"sess-1"}}`,
			},
		},
		{
			name:  "parse error",
			input: `{not json`,
			want: []string{
				`"error":{"code":-32700`,
			},
		},
		{
			name:  "method not found",
			input: `{"jsonrpc":"2.0","id":3,"method":"session/bogus"}`,
			want: []string{
				`"error":{"code":-32601,"message":"method not found: session/bogus"}`,
			},
		},
		{
			name:  "prompt with unknown session",
			input: `{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":"nope","prompt":[{"type":"text","text":"hi"}]}}`,
			want: []string{
				`"error":{"code":-32602,"message":"unknown sessionId \"nope\""}`,
			},
		},
		{
			name: "prompt streams text and completes",
			input: `{"jsonrpc":"2.0","id":5,"method":"session/new"}` + "\n" +
				`{"jsonrpc":"2.0","id":6,"method":"session/prompt","params":{"sessionId":"sess-1","prompt":[{"type":"text","text":"hi"}]}}`,
			responses: []llm.Response{
				{Content: []llm.ContentBlock{{Type: "text", Text: "hello"}}, StopReason: "end_turn"},
			},
			want: []string{
				`{"jsonrpc":"2.0","id":5,"result":{"sessionId":"sess-1"}}`,
				`"method":"session/update"`,
				`"agent_message_chunk"`,
				`{"type":"text","text":"hello"}`,
				`{"jsonrpc":"2.0","id":6,"result":{"stopReason":"end_turn"}}`,
			},
		},
		{
			name: "prompt keeps history across calls",
			input: `{"jsonrpc":"2.0","id":7,"method":"session/new"}` + "\n" +
				`{"jsonrpc":"2.0","id":8,"method":"session/prompt","params":{"sessionId":"sess-1","prompt":[{"type":"text","text":"first"}]}}` + "\n" +
				`{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"sess-1","prompt":[{"type":"text","text":"second"}]}}`,
			responses: []llm.Response{
				{Content: []llm.ContentBlock{{Type: "text", Text: "one"}}, StopReason: "end_turn"},
				{Content: []llm.ContentBlock{{Type: "text", Text: "two"}}, StopReason: "end_turn"},
			},
			want: []string{
				`{"jsonrpc":"2.0","id":8,"result":{"stopReason":"end_turn"}}`,
				`{"jsonrpc":"2.0","id":9,"result":{"stopReason":"end_turn"}}`,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			streamer := &scriptedStreamer{responses: test.responses}
			engine := &agent.Agent{Client: streamer, Tools: &agent.Registry{}}
			var output strings.Builder

			if err := Run(engine, strings.NewReader(test.input), &output); err != nil {
				t.Fatalf("Run: %v", err)
			}
			for _, want := range test.want {
				if !strings.Contains(output.String(), want) {
					t.Errorf("output missing %q\ngot:\n%s", want, output.String())
				}
			}
		})
	}
}

// TestRunSecondPromptSendsHistory pins the context-keeping behavior: the second
// session/prompt must include the first exchange in what reaches the streamer.
func TestRunSecondPromptSendsHistory(t *testing.T) {
	var secondCallMessages []llm.Message
	streamer := &recordingStreamer{
		responses: []llm.Response{
			{Content: []llm.ContentBlock{{Type: "text", Text: "one"}}, StopReason: "end_turn"},
			{Content: []llm.ContentBlock{{Type: "text", Text: "two"}}, StopReason: "end_turn"},
		},
		onSecondCall: func(messages []llm.Message) { secondCallMessages = messages },
	}
	engine := &agent.Agent{Client: streamer, Tools: &agent.Registry{}}
	input := `{"jsonrpc":"2.0","id":1,"method":"session/new"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"sess-1","prompt":[{"type":"text","text":"first"}]}}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"sess-1","prompt":[{"type":"text","text":"second"}]}}`
	var output strings.Builder

	if err := Run(engine, strings.NewReader(input), &output); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// user "first", assistant "one", user "second" = 3 messages on the second call.
	if len(secondCallMessages) != 3 {
		t.Fatalf("second prompt sent %d messages, want 3 (history kept)", len(secondCallMessages))
	}
	if got := secondCallMessages[0].Content[0].Text; got != "first" {
		t.Errorf("history head = %q, want %q", got, "first")
	}
}

// recordingStreamer is a scriptedStreamer that also captures the messages of the second call.
type recordingStreamer struct {
	responses    []llm.Response
	callCount    int
	onSecondCall func([]llm.Message)
}

func (s *recordingStreamer) CompleteStream(messages []llm.Message, tools []llm.ToolDef, onText func(string)) (*llm.Response, error) {
	if s.callCount == 1 && s.onSecondCall != nil {
		s.onSecondCall(messages)
	}
	response := s.responses[s.callCount]
	s.callCount++
	for _, block := range response.Content {
		if block.Type == "text" {
			onText(block.Text)
		}
	}
	return &response, nil
}
