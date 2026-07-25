package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/toddky/todd-agent/internal/agent"
	"github.com/toddky/todd-agent/internal/llm"
	"github.com/toddky/todd-agent/internal/ui/oneshot"
	"github.com/toddky/todd-agent/internal/ui/repl"
)

// Exit codes:
//
//	0 = success (in --oneshot: verdict pass)
//	1 = verdict fail (--oneshot only)
//	2 = the model never called the verdict tool (--oneshot only)
//	3 = runtime error (bad flags, missing key, tool discovery or API failure)
//
// run returns the code instead of calling os.Exit so its deferred cleanup still runs.
func main() {
	code, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}

func run() (int, error) {
	prompt := flag.String("prompt", "", "first prompt to send; in REPL mode it runs before reading input")
	oneshotMode := flag.Bool("oneshot", false, "answer --prompt in a single turn and exit (requires --prompt)")
	allowExit := flag.Bool("allow-exit", false, "advertise an internal exit tool so the model can end the agent with any exit code")
	flag.Parse()
	if *oneshotMode && *prompt == "" {
		return 3, fmt.Errorf("--oneshot requires --prompt; pass the question with --prompt '...'")
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_AUTH_TOKEN")
	}
	if apiKey == "" {
		return 3, fmt.Errorf("ANTHROPIC_API_KEY (or ANTHROPIC_AUTH_TOKEN) must be set")
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
		return 3, fmt.Errorf("locate executable to find tools dir: %w", err)
	}
	sourceTools := filepath.Join(filepath.Dir(executable), "tools")
	if _, err := os.Stat(sourceTools); err != nil {
		// `go run` puts the binary in a temp dir; fall back to ./tools.
		sourceTools = "tools"
	}

	// Every executable in the tools dir is allowed.
	// Per-agent restriction happens by pointing different agents at different source dirs later.
	if err := agent.Setup(sourceTools); err != nil {
		return 3, err
	}
	defer func() {
		if err := agent.Cleanup(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()

	registry, err := agent.LoadAll(filepath.Join(agent.GetRuntimeDir(), "tools"))
	if err != nil {
		return 3, err
	}

	engine := &agent.Agent{
		Client:    &llm.Client{APIKey: apiKey, BaseURL: baseURL, Model: model},
		Tools:     registry,
		AllowExit: *allowExit,
	}
	if *oneshotMode {
		return oneshot.Run(engine, *prompt)
	}
	if err := repl.Run(engine, *prompt); err != nil {
		return 3, err
	}
	return 0, nil
}
