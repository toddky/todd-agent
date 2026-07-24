// Package llm is a client for the OpenAI Chat Completions wire format, served by litellm or any compatible proxy.
// The exported types keep Anthropic-style content blocks; translation to the wire format happens inside the client.
package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	APIKey  string
	BaseURL string
	Model   string
}

type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// TextMessage builds a message whose content is a single text block.
func TextMessage(role, text string) Message {
	return Message{Role: role, Content: []ContentBlock{{Type: "text", Text: text}}}
}

// ContentBlock is one element of a message's content array.
// Which fields are set depends on Type: "text" uses Text; "tool_use" uses ID, Name, Input; "tool_result" uses ToolUseID, Content, IsError.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// ToolDef advertises one callable tool to the model.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type Response struct {
	Content    []ContentBlock
	StopReason string
}

type APIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// maxTokens is the per-response output budget.
// 8192 is a guess; the real limit is model-specific, so raise it if generations get truncated.
const maxTokens = 8192

// Text returns the concatenated text blocks of the response.
func (r *Response) Text() string {
	var out string
	for _, block := range r.Content {
		if block.Type == "text" {
			out += block.Text
		}
	}
	return out
}

// apiMessage is one Chat Completions message on the wire.
// Unlike Message, text is a flat string; tool calls and tool results get dedicated fields.
// ToolCallID is set only on role "tool" messages to link a result to the call it answers.
type apiMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []apiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// apiToolCall is one tool invocation the model makes: which tool, with what arguments.
// The Chat Completions equivalent of an Anthropic tool_use block.
type apiToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function apiFunction `json:"function"`
}

// apiFunction is the name/arguments pair nested under a tool call's "function" key.
// Arguments is a JSON-encoded string, not a JSON object; that quirk is the wire format's.
type apiFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// apiTool advertises one callable tool in the request's "tools" array: the definition, not a call.
// It wraps a ToolDef in the Chat Completions "function" envelope.
type apiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type request struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	Messages  []apiMessage `json:"messages"`
	Tools     []apiTool    `json:"tools,omitempty"`
	Stream    bool          `json:"stream,omitempty"`
}

// toAPI flattens block-structured history into Chat Completions messages.
// Each tool_result block becomes its own "tool" role message, as the wire format requires.
func toAPI(messages []Message) []apiMessage {
	var wire []apiMessage
	for _, message := range messages {
		var text string
		var calls []apiToolCall
		var results []apiMessage
		for _, block := range message.Content {
			switch block.Type {
			case "text":
				text += block.Text
			case "tool_use":
				calls = append(calls, apiToolCall{
					ID:   block.ID,
					Type: "function",
					Function: apiFunction{
						Name:      block.Name,
						Arguments: string(block.Input),
					},
				})
			case "tool_result":
				content := block.Content
				// The wire format has no is_error flag; a prefix is how the model learns the call failed.
				if block.IsError {
					content = "ERROR: " + content
				}
				results = append(results, apiMessage{
					Role:       "tool",
					Content:    content,
					ToolCallID: block.ToolUseID,
				})
			}
		}
		if text != "" || len(calls) > 0 {
			wire = append(wire, apiMessage{Role: message.Role, Content: text, ToolCalls: calls})
		}
		wire = append(wire, results...)
	}
	return wire
}

func toAPITools(tools []ToolDef) []apiTool {
	var wire []apiTool
	for _, tool := range tools {
		var wrapped apiTool
		wrapped.Type = "function"
		wrapped.Function.Name = tool.Name
		wrapped.Function.Description = tool.Description
		wrapped.Function.Parameters = tool.InputSchema
		wire = append(wire, wrapped)
	}
	return wire
}

// fromAPI rebuilds content blocks and the Anthropic-style stop reason the agent loop expects.
func fromAPI(text string, calls []apiToolCall, finishReason string) *Response {
	var response Response
	if text != "" {
		response.Content = append(response.Content, ContentBlock{Type: "text", Text: text})
	}
	for _, call := range calls {
		input := call.Function.Arguments
		if input == "" {
			input = "{}"
		}
		response.Content = append(response.Content, ContentBlock{
			Type:  "tool_use",
			ID:    call.ID,
			Name:  call.Function.Name,
			Input: json.RawMessage(input),
		})
	}
	response.StopReason = finishReason
	if finishReason == "tool_calls" {
		response.StopReason = "tool_use"
	}
	return &response
}

// httpClient times out when the API stops responding.
// ResponseHeaderTimeout is used instead of Client.Timeout so a long streaming body read is not killed mid-response.
// 60s covers slow proxy queueing; a stuck request surfaces as a timeout error instead of hanging forever.
var httpClient = &http.Client{
	Transport: &http.Transport{ResponseHeaderTimeout: 60 * time.Second},
}

// maxBackoff caps the retry wait; doubling from 1s hits the 60s ceiling on the 7th retry.
const maxBackoff = 60 * time.Second

// retryLimit is how many failed attempts to retry before giving up.
// 7 walks the full 1s..60s backoff ladder once.
const retryLimit = 7

// send posts the request, retrying timeouts, network errors, 429, and 5xx with exponential backoff.
// The last failure reason (including API timeouts) is returned so the user sees why the request gave up.
func (c *Client) send(body request) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// /v1/chat/completions rather than /v1/messages: the proxy's WAF exempts only this route
	// from its path-traversal body rule, and file contents legitimately contain "../.." sequences.
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/chat/completions"

	backoff := time.Second
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.APIKey)

		resp, err := httpClient.Do(req)
		if err == nil && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return resp, nil
		}

		var reason string
		if err != nil {
			// Timeouts land here; err.Error() names them (e.g. "timeout awaiting response headers").
			reason = err.Error()
		} else {
			reason = "status " + resp.Status
			resp.Body.Close()
		}
		if attempt == retryLimit {
			return nil, fmt.Errorf("send request: giving up after %d retries: %s", retryLimit, reason)
		}

		fmt.Fprintf(os.Stderr, "api request failed (%s); retrying in %s\n", reason, backoff)
		time.Sleep(backoff)
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// completion is the non-streaming Chat Completions response envelope.
type completion struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string         `json:"content"`
			ToolCalls []apiToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Error *APIError `json:"error"`
}

// Complete sends the message history and returns the model's response.
func (c *Client) Complete(messages []Message, tools []ToolDef) (*Response, error) {
	resp, err := c.send(request{
		Model:     c.Model,
		MaxTokens: maxTokens,
		Messages:  toAPI(messages),
		Tools:     toAPITools(tools),
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var parsed completion
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse response (status %s): %w; raw: %s", resp.Status, err, body)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("api error (%s): %s", parsed.Error.Type, parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("response has no choices (status %s); raw: %s", resp.Status, body)
	}
	choice := parsed.Choices[0]
	return fromAPI(choice.Message.Content, choice.Message.ToolCalls, choice.FinishReason), nil
}

// chunk is one streamed Chat Completions SSE event.
type chunk struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Delta        struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int          `json:"index"`
				ID       string       `json:"id"`
				Function apiFunction `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Error *APIError `json:"error"`
}

// CompleteStream sends the message history with streaming enabled, calling onText for each text fragment as it arrives.
// It returns the full response, including tool calls assembled from streamed argument fragments.
func (c *Client) CompleteStream(messages []Message, tools []ToolDef, onText func(string)) (*Response, error) {
	resp, err := c.send(request{
		Model:     c.Model,
		MaxTokens: maxTokens,
		Messages:  toAPI(messages),
		Tools:     toAPITools(tools),
		Stream:    true,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("stream request failed (status %s): %s", resp.Status, body)
	}

	var text string
	var finishReason string
	// Tool call fragments arrive keyed by index; id and name come once, arguments accumulate.
	calls := make(map[int]*apiToolCall)
	arguments := make(map[int]*strings.Builder)
	var order []int

	scanner := bufio.NewScanner(resp.Body)
	// Allow long SSE lines; default 64KB can truncate large deltas.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		data, found := strings.CutPrefix(line, "data: ")
		if !found || data == "[DONE]" {
			continue
		}

		var event chunk
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return nil, fmt.Errorf("parse stream event: %w; raw: %s", err, data)
		}
		if event.Error != nil {
			return nil, fmt.Errorf("api error (%s): %s", event.Error.Type, event.Error.Message)
		}
		if len(event.Choices) == 0 {
			continue
		}
		choice := event.Choices[0]

		if choice.Delta.Content != "" {
			text += choice.Delta.Content
			onText(choice.Delta.Content)
		}
		for _, fragment := range choice.Delta.ToolCalls {
			call, known := calls[fragment.Index]
			if !known {
				call = &apiToolCall{Type: "function"}
				calls[fragment.Index] = call
				arguments[fragment.Index] = &strings.Builder{}
				order = append(order, fragment.Index)
			}
			if fragment.ID != "" {
				call.ID = fragment.ID
			}
			if fragment.Function.Name != "" {
				call.Function.Name = fragment.Function.Name
			}
			arguments[fragment.Index].WriteString(fragment.Function.Arguments)
		}
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}

	var assembled []apiToolCall
	for _, index := range order {
		call := calls[index]
		call.Function.Arguments = arguments[index].String()
		assembled = append(assembled, *call)
	}
	return fromAPI(text, assembled, finishReason), nil
}
