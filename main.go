package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/toddky/todd-agent/internal/agent"
	"github.com/toddky/todd-agent/internal/llm"
	"github.com/toddky/todd-agent/internal/ui/repl"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_AUTH_TOKEN")
	}
	if apiKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY (or ANTHROPIC_AUTH_TOKEN) must be set")
	}

	// Trim trailing slashes so path joins in the client can't build "//v1/..." URLs.
	baseURL := strings.TrimRight(os.Getenv("ANTHROPIC_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	model := os.Getenv("ANTHROPIC_MODEL")
	if model == "" {
		model = "claude-sonnet-5"
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable to find tools dir: %w", err)
	}
	sourceTools := filepath.Join(filepath.Dir(executable), "tools")
	if _, err := os.Stat(sourceTools); err != nil {
		// `go run` puts the binary in a temp dir; fall back to ./tools.
		sourceTools = "tools"
	}

	// Every executable in the tools dir is allowed.
	// Per-agent restriction happens by pointing different agents at different source dirs later.
	if err := agent.Setup(sourceTools); err != nil {
		return err
	}
	defer func() {
		if err := agent.Cleanup(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()

	registry, err := agent.LoadAll(filepath.Join(agent.GetRuntimeDir(), "tools"))
	if err != nil {
		return err
	}

	engine := &agent.Agent{
		Client: &llm.Client{APIKey: apiKey, BaseURL: baseURL, Model: model},
		Tools:  registry,
	}
	return repl.Run(engine)
}
