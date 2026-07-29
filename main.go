package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// bundledDir resolves a repo-relative dir (e.g. "tools", "agents/x"), preferring the copy next to the binary.
// It falls back to the current directory for `go run`, where the binary lives in a temp dir.
func bundledDir(relative string) (string, bool) {
	if executable, err := os.Executable(); err == nil {
		beside := filepath.Join(filepath.Dir(executable), relative)
		if _, err := os.Stat(beside); err == nil {
			return beside, true
		}
	}
	if _, err := os.Stat(relative); err == nil {
		return relative, true
	}
	return "", false
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
	agentName := flag.String("agent", "", "load a bundled agent from agents/<name>: its tools/ (as --tools-dir), prompt.md (as --prompt-file), and system_prompts/*.md (as --system-prompt-file)")
	var toolsDirs stringList
	flag.Var(&toolsDirs, "tools-dir", "tools directory to load; repeatable, later dirs win on name collisions (default: tools/ next to the binary)")
	var systemPromptFiles stringList
	flag.Var(&systemPromptFiles, "system-prompt-file", "file appended to the system prompt; repeatable, joined in flag order")
	flag.Parse()

	// --agent expands to the primitive flags: tools/ and system_prompts/*.md append to their flags.
	// Its prompt.md fills --prompt-file only when the user gave no explicit prompt, so a prompt replaces it.
	if *agentName != "" {
		agentDir, found := bundledDir(filepath.Join("agents", *agentName))
		if !found {
			return 3, fmt.Errorf("--agent %q: no such agent under agents/", *agentName)
		}
		toolsDirs = append(toolsDirs, filepath.Join(agentDir, "tools"))
		systemPromptGlob := filepath.Join(agentDir, "system_prompts", "*.md")
		matches, err := filepath.Glob(systemPromptGlob)
		if err != nil {
			return 3, fmt.Errorf("glob %s: %w", systemPromptGlob, err)
		}
		sort.Strings(matches)
		systemPromptFiles = append(systemPromptFiles, matches...)
		if *oneshotMode && *prompt == "" && *promptFile == "" {
			*promptFile = filepath.Join(agentDir, "prompt.md")
		}
	}

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

	// Without --tools-dir (and no --agent that added one), use the default tools
	// dir next to the binary. With either, only the named dirs load, so an
	// agent's tool set can be restricted.
	if len(toolsDirs) == 0 {
		defaultTools, found := bundledDir("tools")
		if !found {
			return 3, fmt.Errorf("no tools dir found next to the binary or in the current directory")
		}
		toolsDirs = []string{defaultTools}
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
