package main

import (
	"errors"
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

// stringList collects a repeatable string flag in the order given.
type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

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
	promptFile := flag.String("prompt-file", "", "file whose contents become the first prompt; --prompt wins if both are set")
	oneshotMode := flag.Bool("oneshot", false, "answer the first prompt in a single turn and exit (requires --prompt or --prompt-file)")
	allowExit := flag.Bool("allow-exit", false, "advertise an internal exit tool so the model can end the agent with any exit code")
	var toolsDirs stringList
	flag.Var(&toolsDirs, "tools-dir", "tools directory to load; repeatable, later dirs win on name collisions (default: tools/ next to the binary)")
	var systemPromptFiles stringList
	flag.Var(&systemPromptFiles, "system-prompt-file", "file appended to the system prompt; repeatable, joined in flag order")
	flag.Parse()

	if *prompt == "" && *promptFile != "" {
		content, err := os.ReadFile(*promptFile)
		if err != nil {
			return 3, fmt.Errorf("read --prompt-file: %w", err)
		}
		*prompt = string(content)
	} else if *prompt != "" && *promptFile != "" {
		fmt.Fprintln(os.Stderr, "warning: both --prompt and --prompt-file are set; using --prompt and ignoring the file")
	}
	if *oneshotMode && *prompt == "" {
		return 3, fmt.Errorf("--oneshot requires a first prompt; pass it with --prompt '...' or --prompt-file <file>")
	}

	var systemPrompts []string
	for _, file := range systemPromptFiles {
		content, err := os.ReadFile(file)
		if err != nil {
			return 3, fmt.Errorf("read --system-prompt-file: %w", err)
		}
		// The header tells the model where each part came from, so files stay distinguishable after joining.
		part := fmt.Sprintf("<!-- system prompt file: %s -->\n%s", file, strings.TrimSpace(string(content)))
		systemPrompts = append(systemPrompts, part)
	}
	combinedSystemPrompt := strings.Join(systemPrompts, "\n\n")

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

	// Without --tools-dir, use the default tools dir next to the binary.
	// With it, only the named dirs load, so an agent's tool set can be restricted.
	if len(toolsDirs) == 0 {
		executable, err := os.Executable()
		if err != nil {
			return 3, fmt.Errorf("locate executable to find tools dir: %w", err)
		}
		sourceTools := filepath.Join(filepath.Dir(executable), "tools")
		if _, err := os.Stat(sourceTools); err != nil {
			// `go run` puts the binary in a temp dir; fall back to ./tools.
			sourceTools = "tools"
		}
		toolsDirs = []string{sourceTools}
	}

	if err := agent.Setup(toolsDirs...); err != nil {
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
		Client:       &llm.Client{APIKey: apiKey, BaseURL: baseURL, Model: model},
		Tools:        registry,
		SystemPrompt: combinedSystemPrompt,
		AllowExit:    *allowExit,
	}
	if *oneshotMode {
		return oneshot.Run(engine, *prompt)
	}
	err = repl.Run(engine, *prompt)
	var exitRequest *agent.ExitRequest
	if errors.As(err, &exitRequest) {
		// The model called the exit tool; the reason was printed by the REPL.
		return exitRequest.Code, nil
	}
	if err != nil {
		return 3, err
	}
	return 0, nil
}
