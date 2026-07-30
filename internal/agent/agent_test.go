package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/toddky/menehune/internal/llm"
)

// fakeClient plays back scripted responses, one per CompleteStream call.
type fakeClient struct {
	responses []*llm.Response
	calls     int
	// lastTools records the tool defs advertised on the most recent call.
	lastTools []llm.ToolDef
}

func (f *fakeClient) CompleteStream(messages []llm.Message, tools []llm.ToolDef, onText func(string)) (*llm.Response, error) {
	f.lastTools = tools
	if f.calls >= len(f.responses) {
		return nil, errors.New("fakeClient: no scripted response left")
	}
	response := f.responses[f.calls]
	f.calls++
	for _, block := range response.Content {
		if block.Type == "text" {
			onText(block.Text)
		}
	}
	return response, nil
}

// emptyRegistry returns a registry with no tools, for turns that never dispatch.
func emptyRegistry() *Registry {
	return &Registry{Dir: "/nowhere", Tools: map[string]Tool{}}
}

// echoRegistry returns a registry with one real echo tool built in a tempdir.
func echoRegistry(t *testing.T) *Registry {
	t.Helper()
	dir := t.TempDir()
	writeTool(t, dir, "echo_tool", echoTool)
	registry, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}
	return registry
}

func textResponse(text string) *llm.Response {
	return &llm.Response{
		Content:    []llm.ContentBlock{{Type: "text", Text: text}},
		StopReason: "stop",
	}
}

func toolCallResponse(id, name, input string) *llm.Response {
	return &llm.Response{
		Content: []llm.ContentBlock{{
			Type:  "tool_use",
			ID:    id,
			Name:  name,
			Input: json.RawMessage(input),
		}},
		StopReason: "tool_use",
	}
}

func TestTurnTextOnly(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{textResponse("hello")}}
	agent := &Agent{Client: client, Tools: emptyRegistry()}

	var events []Event
	messages, err := agent.Turn([]llm.Message{llm.TextMessage("user", "hi")}, func(event Event) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("Turn() error: %v", err)
	}

	if len(messages) != 2 || messages[1].Role != "assistant" {
		t.Errorf("Turn() history = %+v, want user + assistant", messages)
	}
	sawComplete := false
	for _, event := range events {
		if event.Type == EventTurnComplete {
			sawComplete = true
		}
	}
	if !sawComplete {
		t.Error("Turn() never published EventTurnComplete")
	}
}

func TestTurnDispatchesToolAndLoops(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{
		toolCallResponse("call_1", "echo_tool", `{"key": "value"}`),
		textResponse("done"),
	}}
	agent := &Agent{Client: client, Tools: echoRegistry(t)}

	var events []Event
	messages, err := agent.Turn([]llm.Message{llm.TextMessage("user", "go")}, func(event Event) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("Turn() error: %v", err)
	}
	if client.calls != 2 {
		t.Errorf("CompleteStream called %d times, want 2 (tool round then final)", client.calls)
	}

	// History: user, assistant(tool_use), user(tool_result), assistant(text).
	if len(messages) != 4 {
		t.Fatalf("Turn() history has %d messages, want 4: %+v", len(messages), messages)
	}
	result := messages[2].Content[0]
	if result.Type != "tool_result" || result.ToolUseID != "call_1" {
		t.Errorf("third message = %+v, want tool_result for call_1", result)
	}
	if result.Content != `{"key": "value"}` || result.IsError {
		t.Errorf("tool_result = %+v, want echoed input with IsError=false", result)
	}
}

func TestTurnReportsToolFailureToModel(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{
		toolCallResponse("call_1", "ghost_tool", `{}`),
		textResponse("recovered"),
	}}
	agent := &Agent{Client: client, Tools: emptyRegistry()}

	messages, err := agent.Turn([]llm.Message{llm.TextMessage("user", "go")}, func(Event) {})
	if err != nil {
		t.Fatalf("Turn() error: %v", err)
	}

	// The unknown tool must not kill the turn; its error goes back as an is_error tool_result.
	if len(messages) != 4 {
		t.Fatalf("Turn() history has %d messages, want 4: %+v", len(messages), messages)
	}
	result := messages[2].Content[0]
	if result.Type != "tool_result" || !result.IsError {
		t.Errorf("third message = %+v, want is_error tool_result", result)
	}
	if !strings.Contains(result.Content, "unknown tool") {
		t.Errorf("tool_result content = %q, want unknown-tool error text", result.Content)
	}
}

func TestTurnExitToolReturnsExitRequest(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{
		toolCallResponse("call_1", "exit", `{"code": 7, "reason": "test says so"}`),
	}}
	agent := &Agent{Client: client, Tools: emptyRegistry(), AllowExit: true}

	_, err := agent.Turn([]llm.Message{llm.TextMessage("user", "exit 7")}, func(Event) {})

	var exitRequest *ExitRequest
	if !errors.As(err, &exitRequest) {
		t.Fatalf("Turn() error = %v, want *ExitRequest", err)
	}
	if exitRequest.Code != 7 || exitRequest.Reason != "test says so" {
		t.Errorf("ExitRequest = %+v, want code 7 with reason", exitRequest)
	}
}

func TestTurnExitToolIgnoredWithoutAllowExit(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{
		toolCallResponse("call_1", "exit", `{"code": 7, "reason": "should not work"}`),
		textResponse("continued"),
	}}
	agent := &Agent{Client: client, Tools: emptyRegistry(), AllowExit: false}

	_, err := agent.Turn([]llm.Message{llm.TextMessage("user", "try exit")}, func(Event) {})

	var exitRequest *ExitRequest
	if errors.As(err, &exitRequest) {
		t.Fatal("Turn() honored the exit tool without AllowExit")
	}
	// Without AllowExit the name is unknown, so the model gets a tool error and the loop continues.
	if err != nil {
		t.Fatalf("Turn() error: %v", err)
	}
}

func TestTurnExitToolInvalidInput(t *testing.T) {
	client := &fakeClient{responses: []*llm.Response{
		toolCallResponse("call_1", "exit", `not json`),
	}}
	agent := &Agent{Client: client, Tools: emptyRegistry(), AllowExit: true}

	_, err := agent.Turn([]llm.Message{llm.TextMessage("user", "exit")}, func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "invalid input") {
		t.Errorf("Turn() error = %v, want invalid-input error", err)
	}
	var exitRequest *ExitRequest
	if errors.As(err, &exitRequest) {
		t.Error("Turn() returned an ExitRequest for invalid input")
	}
}

func TestTurnAdvertisesExitToolOnlyWhenAllowed(t *testing.T) {
	cases := []struct {
		name      string
		allowExit bool
		wantExit  bool
	}{
		{name: "allowed", allowExit: true, wantExit: true},
		{name: "not allowed", allowExit: false, wantExit: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client := &fakeClient{responses: []*llm.Response{textResponse("ok")}}
			agent := &Agent{Client: client, Tools: emptyRegistry(), AllowExit: testCase.allowExit}

			if _, err := agent.Turn([]llm.Message{llm.TextMessage("user", "hi")}, func(Event) {}); err != nil {
				t.Fatalf("Turn() error: %v", err)
			}

			sawExit := false
			for _, def := range client.lastTools {
				if def.Name == "exit" {
					sawExit = true
				}
			}
			if sawExit != testCase.wantExit {
				t.Errorf("exit tool advertised = %v, want %v", sawExit, testCase.wantExit)
			}
		})
	}
}
