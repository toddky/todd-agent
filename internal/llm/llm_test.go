package llm

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestToAPI(t *testing.T) {
	cases := []struct {
		name     string
		messages []Message
		want     []apiMessage
	}{
		{
			name:     "empty history",
			messages: nil,
			want:     nil,
		},
		{
			name:     "text only",
			messages: []Message{TextMessage("user", "hello")},
			want: []apiMessage{
				{Role: "user", Content: "hello"},
			},
		},
		{
			name: "multiple text blocks concatenate",
			messages: []Message{{
				Role: "assistant",
				Content: []ContentBlock{
					{Type: "text", Text: "part one "},
					{Type: "text", Text: "part two"},
				},
			}},
			want: []apiMessage{
				{Role: "assistant", Content: "part one part two"},
			},
		},
		{
			name: "assistant tool call",
			messages: []Message{{
				Role: "assistant",
				Content: []ContentBlock{
					{Type: "text", Text: "reading the file"},
					{Type: "tool_use", ID: "call_1", Name: "read_file", Input: json.RawMessage(`{"path":"a.txt"}`)},
				},
			}},
			want: []apiMessage{
				{
					Role:    "assistant",
					Content: "reading the file",
					ToolCalls: []apiToolCall{{
						ID:       "call_1",
						Type:     "function",
						Function: apiFunction{Name: "read_file", Arguments: `{"path":"a.txt"}`},
					}},
				},
			},
		},
		{
			name: "tool result becomes its own tool message",
			messages: []Message{{
				Role: "user",
				Content: []ContentBlock{
					{Type: "tool_result", ToolUseID: "call_1", Content: "file contents"},
				},
			}},
			want: []apiMessage{
				{Role: "tool", Content: "file contents", ToolCallID: "call_1"},
			},
		},
		{
			name: "tool result error gets prefix",
			messages: []Message{{
				Role: "user",
				Content: []ContentBlock{
					{Type: "tool_result", ToolUseID: "call_1", Content: "file not found", IsError: true},
				},
			}},
			want: []apiMessage{
				{Role: "tool", Content: "ERROR: file not found", ToolCallID: "call_1"},
			},
		},
		{
			name: "empty tool result becomes a placeholder",
			messages: []Message{{
				Role: "user",
				Content: []ContentBlock{
					{Type: "tool_result", ToolUseID: "call_1", Content: ""},
				},
			}},
			want: []apiMessage{
				{Role: "tool", Content: "(no output)", ToolCallID: "call_1"},
			},
		},
		{
			name: "multiple tool results split into separate messages",
			messages: []Message{{
				Role: "user",
				Content: []ContentBlock{
					{Type: "tool_result", ToolUseID: "call_1", Content: "one"},
					{Type: "tool_result", ToolUseID: "call_2", Content: "two"},
				},
			}},
			want: []apiMessage{
				{Role: "tool", Content: "one", ToolCallID: "call_1"},
				{Role: "tool", Content: "two", ToolCallID: "call_2"},
			},
		},
		{
			name: "tool-result-only message produces no user message",
			messages: []Message{
				TextMessage("user", "read it"),
				{Role: "user", Content: []ContentBlock{
					{Type: "tool_result", ToolUseID: "call_1", Content: "data"},
				}},
			},
			want: []apiMessage{
				{Role: "user", Content: "read it"},
				{Role: "tool", Content: "data", ToolCallID: "call_1"},
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := toAPI(testCase.messages, false)
			if !reflect.DeepEqual(got, testCase.want) {
				t.Errorf("toAPI() = %+v, want %+v", got, testCase.want)
			}
		})
	}
}

func TestMarkCacheBreakpoint(t *testing.T) {
	t.Run("text message moves content into parts", func(t *testing.T) {
		wire := toAPI([]Message{
			TextMessage("user", "hello"),
			TextMessage("assistant", "hi there"),
		}, true)

		if wire[0].CacheControl != nil || wire[0].Content != "hello" {
			t.Errorf("first message = %+v, want untouched flat string", wire[0])
		}
		last := wire[len(wire)-1]
		parts, ok := last.Content.([]apiContentPart)
		if !ok || len(parts) != 1 {
			t.Fatalf("last message Content = %+v, want one apiContentPart", last.Content)
		}
		if parts[0].Text != "hi there" || parts[0].CacheControl != anthropicCacheMarker {
			t.Errorf("marked part = %+v, want text preserved and cache_control set", parts[0])
		}
		if last.CacheControl != nil {
			t.Errorf("message-level CacheControl = %+v, want nil when parts carry the marker", last.CacheControl)
		}
	})

	t.Run("tool message marks at message level", func(t *testing.T) {
		wire := toAPI([]Message{{
			Role: "user",
			Content: []ContentBlock{
				{Type: "tool_result", ToolUseID: "call_1", Content: "data"},
			},
		}}, true)

		last := wire[len(wire)-1]
		if last.Content != "data" {
			t.Errorf("tool message Content = %+v, want flat string preserved", last.Content)
		}
		if last.CacheControl != anthropicCacheMarker {
			t.Errorf("tool message CacheControl = %+v, want anthropicCacheMarker", last.CacheControl)
		}
	})

	t.Run("empty history does not panic", func(t *testing.T) {
		if got := toAPI(nil, true); got != nil {
			t.Errorf("toAPI(nil, true) = %+v, want nil", got)
		}
	})
}

func TestToAPIToolsCacheBreakpoint(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	got := toAPITools([]ToolDef{
		{Name: "bash", InputSchema: schema},
		{Name: "grep", InputSchema: schema},
	}, true)

	if got[0].CacheControl != nil {
		t.Errorf("first tool CacheControl = %+v, want nil", got[0].CacheControl)
	}
	if got[1].CacheControl != anthropicCacheMarker {
		t.Errorf("last tool CacheControl = %+v, want anthropicCacheMarker", got[1].CacheControl)
	}
}

func TestToAPITools(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	got := toAPITools([]ToolDef{{Name: "bash", Description: "run a command", InputSchema: schema}}, false)

	if len(got) != 1 {
		t.Fatalf("toAPITools() returned %d tools, want 1", len(got))
	}
	tool := got[0]
	if tool.Type != "function" {
		t.Errorf("Type = %q, want \"function\"", tool.Type)
	}
	if tool.Function.Name != "bash" {
		t.Errorf("Function.Name = %q, want \"bash\"", tool.Function.Name)
	}
	if tool.Function.Description != "run a command" {
		t.Errorf("Function.Description = %q, want \"run a command\"", tool.Function.Description)
	}
	if string(tool.Function.Parameters) != string(schema) {
		t.Errorf("Function.Parameters = %s, want %s", tool.Function.Parameters, schema)
	}
}

func TestFromAPI(t *testing.T) {
	cases := []struct {
		name         string
		text         string
		calls        []apiToolCall
		finishReason string
		wantContent  []ContentBlock
		wantStop     string
	}{
		{
			name:         "text only",
			text:         "hello",
			finishReason: "stop",
			wantContent:  []ContentBlock{{Type: "text", Text: "hello"}},
			wantStop:     "stop",
		},
		{
			name: "tool call maps finish reason to tool_use",
			text: "",
			calls: []apiToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: apiFunction{Name: "bash", Arguments: `{"command":"ls"}`},
			}},
			finishReason: "tool_calls",
			wantContent: []ContentBlock{{
				Type:  "tool_use",
				ID:    "call_1",
				Name:  "bash",
				Input: json.RawMessage(`{"command":"ls"}`),
			}},
			wantStop: "tool_use",
		},
		{
			name: "empty arguments default to empty object",
			calls: []apiToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: apiFunction{Name: "noargs", Arguments: ""},
			}},
			finishReason: "tool_calls",
			wantContent: []ContentBlock{{
				Type:  "tool_use",
				ID:    "call_1",
				Name:  "noargs",
				Input: json.RawMessage("{}"),
			}},
			wantStop: "tool_use",
		},
		{
			name:         "empty response",
			finishReason: "stop",
			wantContent:  nil,
			wantStop:     "stop",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := fromAPI(testCase.text, testCase.calls, testCase.finishReason)
			if !reflect.DeepEqual(got.Content, testCase.wantContent) {
				t.Errorf("Content = %+v, want %+v", got.Content, testCase.wantContent)
			}
			if got.StopReason != testCase.wantStop {
				t.Errorf("StopReason = %q, want %q", got.StopReason, testCase.wantStop)
			}
		})
	}
}
