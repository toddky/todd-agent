// Package llm is a client for the Anthropic Messages API wire format, served by api.anthropic.com or any compatible proxy (e.g. litellm).
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
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Error      *APIError      `json:"error"`
}

type APIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []Message `json:"messages"`
	Tools     []ToolDef `json:"tools,omitempty"`
	Stream    bool      `json:"stream,omitempty"`
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

	// A trailing slash in BaseURL would build "//v1/messages", which FastAPI proxies 404.
	url := strings.TrimRight(c.BaseURL, "/") + "/v1/messages"

	backoff := time.Second
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", c.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")

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

// Complete sends the message history and returns the model's response.
func (c *Client) Complete(messages []Message, tools []ToolDef) (*Response, error) {
	resp, err := c.send(request{
		Model:     c.Model,
		MaxTokens: maxTokens,
		Messages:  messages,
		Tools:     tools,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var parsed Response
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse response (status %s): %w; raw: %s", resp.Status, err, body)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("api error (%s): %s", parsed.Error.Type, parsed.Error.Message)
	}
	return &parsed, nil
}

type streamEvent struct {
	Type         string        `json:"type"`
	Index        int           `json:"index"`
	ContentBlock *ContentBlock `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Error *APIError `json:"error"`
}

// CompleteStream sends the message history with streaming enabled, calling onText for each text fragment as it arrives.
// It returns the full response, including any tool_use blocks assembled from input_json_delta events.
func (c *Client) CompleteStream(messages []Message, tools []ToolDef, onText func(string)) (*Response, error) {
	resp, err := c.send(request{
		Model:     c.Model,
		MaxTokens: maxTokens,
		Messages:  messages,
		Tools:     tools,
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

	var result Response
	// The API streams tool_use input as JSON fragments; collect per block index.
	inputParts := make(map[int]*strings.Builder)
	indexToPos := make(map[int]int)

	scanner := bufio.NewScanner(resp.Body)
	// Allow long SSE lines; default 64KB can truncate large deltas.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		data, found := strings.CutPrefix(line, "data: ")
		if !found {
			continue
		}

		var event streamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return nil, fmt.Errorf("parse stream event: %w; raw: %s", err, data)
		}

		switch event.Type {
		case "error":
			if event.Error != nil {
				return nil, fmt.Errorf("api error (%s): %s", event.Error.Type, event.Error.Message)
			}
		case "content_block_start":
			if event.ContentBlock == nil {
				return nil, fmt.Errorf("content_block_start without content_block; raw: %s", data)
			}
			indexToPos[event.Index] = len(result.Content)
			result.Content = append(result.Content, *event.ContentBlock)
			if event.ContentBlock.Type == "tool_use" {
				inputParts[event.Index] = &strings.Builder{}
			}
		case "content_block_delta":
			pos, known := indexToPos[event.Index]
			if !known {
				return nil, fmt.Errorf("delta for unknown block index %d; raw: %s", event.Index, data)
			}
			switch event.Delta.Type {
			case "text_delta":
				result.Content[pos].Text += event.Delta.Text
				onText(event.Delta.Text)
			case "input_json_delta":
				inputParts[event.Index].WriteString(event.Delta.PartialJSON)
			}
		case "message_delta":
			if event.Delta.StopReason != "" {
				result.StopReason = event.Delta.StopReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}

	for index, builder := range inputParts {
		input := builder.String()
		if input == "" {
			input = "{}"
		}
		result.Content[indexToPos[index]].Input = json.RawMessage(input)
	}
	return &result, nil
}
