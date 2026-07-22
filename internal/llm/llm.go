// Package llm is a client for the Anthropic Messages API wire format,
// served by api.anthropic.com or any compatible proxy (e.g. litellm).
package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// Complete sends the message history and returns the model's response.
func (c *Client) Complete(messages []Message) (*Response, error) {
	payload, err := json.Marshal(request{
		Model:     c.Model,
		MaxTokens: 1024,
		Messages:  messages,
	})
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
