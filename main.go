package main

import (
	"fmt"
	"os"

	"github.com/toddky/todd-agent/internal/llm"
)

func main() {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_AUTH_TOKEN")
	}
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY (or ANTHROPIC_AUTH_TOKEN) must be set")
		os.Exit(1)
	}

	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	model := os.Getenv("ANTHROPIC_MODEL")
	if model == "" {
		model = "claude-sonnet-5"
	}

	client := &llm.Client{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
	}

	response, err := client.Complete([]llm.Message{
		{Role: "user", Content: "Say hello world."},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println(response.Text())
}
