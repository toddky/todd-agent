// Package llm is a client for the Anthropic Messages API wire format,
// served by api.anthropic.com or any compatible proxy (e.g. litellm).
package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Client struct {
	APIKey  string
	BaseURL string
	Model   string
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
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
	Stream    bool      `json:"stream,omitempty"`
}

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

func (c *Client) send(body request) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	return resp, nil
}

// Complete sends the message history and returns the model's response.
func (c *Client) Complete(messages []Message) (*Response, error) {
	resp, err := c.send(request{
		Model:     c.Model,
		MaxTokens: 1024,
		Messages:  messages,
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
	Type  string `json:"type"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
	Error *APIError `json:"error"`
}

// CompleteStream sends the message history with streaming enabled, calling
// onText for each text fragment as it arrives. It returns the full
// concatenated text once the stream ends.
func (c *Client) CompleteStream(messages []Message, onText func(string)) (string, error) {
	resp, err := c.send(request{
		Model:     c.Model,
		MaxTokens: 1024,
		Messages:  messages,
		Stream:    true,
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("stream request failed (status %s): %s", resp.Status, body)
	}

	var full strings.Builder
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
			return full.String(), fmt.Errorf("parse stream event: %w; raw: %s", err, data)
		}
		if event.Type == "error" && event.Error != nil {
			return full.String(), fmt.Errorf("api error (%s): %s", event.Error.Type, event.Error.Message)
		}
		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" {
			full.WriteString(event.Delta.Text)
			onText(event.Delta.Text)
		}
	}
	if err := scanner.Err(); err != nil {
		return full.String(), fmt.Errorf("read stream: %w", err)
	}
	return full.String(), nil
}
