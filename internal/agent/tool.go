// Package agent is the engine: the agent loop plus tool discovery and
// dispatch. It never touches the terminal; frontends consume its events.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/toddky/todd-agent/internal/llm"
)

// defaultToolTimeout applies when a tool's schema omits timeout_secs.
// 10s is the agreed default for quick tools like read.
const defaultToolTimeout = 10 * time.Second

// graceTimeout is added to a tool's declared timeout to form the agent's hard-kill deadline.
// The 5s grace lets a tool exit cleanly before the agent force-kills it.
const graceTimeout = 5 * time.Second

// maxToolTimeout caps a model-requested timeout_secs so a bad call can't wedge the
// agent for hours. 600s (10m) is a guess: longer than any tool here should need.
const maxToolTimeout = 600 * time.Second

// schemaTimeout caps how long a tool may take to answer --schema during
// discovery. 5s: discovery runs across every tool at startup, so a hung
// script must not stall the session for long.
const schemaTimeout = 5 * time.Second

// Tool is one discovered tool script.
type Tool struct {
	Name        string
	Path        string
	Description string
	InputSchema json.RawMessage
	Timeout     time.Duration
}

// toolSchema is what a tool script prints on stdout for "--schema".
type toolSchema struct {
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	TimeoutSecs int             `json:"timeout_secs"`
}

// Registry holds the tools discovered in one directory. Dispatch re-execs
// the script on every call, so tool behavior can change while the agent
// runs; schema changes need a reload (fresh LoadAll).
type Registry struct {
	Dir   string
	Tools map[string]Tool
}

// LoadAll discovers tools by running "<script> --schema" on every
// executable in dir and parsing the JSON it prints.
// Schema execs run in parallel so startup costs one exec, not one per tool.
func LoadAll(dir string) (*Registry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read tools dir %s: %w", dir, err)
	}

	var paths []string
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		// os.Stat follows symlinks, so linked tool scripts resolve here.
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat tool %s: %w", path, err)
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		paths = append(paths, path)
	}

	// Each goroutine writes only its own slot, so no lock is needed.
	tools := make([]Tool, len(paths))
	loadErrs := make([]error, len(paths))
	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			tools[i], loadErrs[i] = load(filepath.Base(path), path)
		}(i, path)
	}
	wg.Wait()

	registry := &Registry{Dir: dir, Tools: make(map[string]Tool, len(paths))}
	for i, tool := range tools {
		if loadErrs[i] != nil {
			return nil, loadErrs[i]
		}
		registry.Tools[tool.Name] = tool
	}
	return registry, nil
}

func load(name, path string) (Tool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), schemaTimeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, "--schema")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Tool{}, fmt.Errorf(
			"tool %s: --schema failed (%w): %s; every tool script must print its JSON schema for --schema",
			name, err, stderr.String())
	}

	var schema toolSchema
	if err := json.Unmarshal(stdout.Bytes(), &schema); err != nil {
		return Tool{}, fmt.Errorf("tool %s: --schema printed invalid JSON: %w; raw: %s", name, err, stdout.String())
	}
	if schema.Description == "" || len(schema.InputSchema) == 0 {
		return Tool{}, fmt.Errorf(
			"tool %s: --schema output must include \"description\" and \"input_schema\"; got: %s",
			name, stdout.String())
	}

	timeout := defaultToolTimeout
	if schema.TimeoutSecs > 0 {
		timeout = time.Duration(schema.TimeoutSecs) * time.Second
	}
	return Tool{
		Name:        name,
		Path:        path,
		Description: schema.Description,
		InputSchema: schema.InputSchema,
		Timeout:     timeout,
	}, nil
}

// Definitions returns the tool definitions to advertise to the model,
// sorted by name so requests are deterministic.
func (r *Registry) Definitions() []llm.ToolDef {
	names := make([]string, 0, len(r.Tools))
	for name := range r.Tools {
		names = append(names, name)
	}
	sort.Strings(names)

	defs := make([]llm.ToolDef, 0, len(names))
	for _, name := range names {
		tool := r.Tools[name]
		defs = append(defs, llm.ToolDef{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: withTimeoutProperty(tool.InputSchema, tool.Timeout),
		})
	}
	return defs
}

// withTimeoutProperty advertises an optional "timeout_secs" integer so the model can override the per-call deadline.
// The registry injects it uniformly rather than each tool declaring it, adding a "properties" object if none exists.
func withTimeoutProperty(schema json.RawMessage, defaultTimeout time.Duration) json.RawMessage {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return schema
	}
	// A missing "properties" starts empty so the field still lands on a bare object schema.
	props := map[string]json.RawMessage{}
	if properties, ok := parsed["properties"]; ok {
		if err := json.Unmarshal(properties, &props); err != nil {
			return schema
		}
	}
	description := fmt.Sprintf(
		"Optional seconds before this call is killed; defaults to %d, max %d. The tool self-times below the deadline for a graceful partial result.",
		int(defaultTimeout.Seconds()), int(maxToolTimeout.Seconds()))
	// These marshals cannot fail: every value is a string or an already-valid schema fragment.
	props["timeout_secs"], _ = json.Marshal(map[string]string{"type": "integer", "description": description})
	parsed["properties"], _ = json.Marshal(props)
	rebuilt, _ := json.Marshal(parsed)
	return rebuilt
}

// Tool script exit-code contract: 0 = success (stdout is the result),
// 1 = runtime failure (e.g. file not found), 2 = malformed call (input JSON
// does not match the tool's schema). Stderr carries the reason for both
// failure kinds.
const (
	exitRuntimeFailure = 1
	exitMalformedCall  = 2
)

// Run executes a tool with the JSON input on stdin and returns its stdout.
// The agent hard-kills at the resolved timeout plus graceTimeout; the tool reads the same timeout_secs to self-time first.
func (r *Registry) Run(name string, input json.RawMessage) (string, error) {
	tool, known := r.Tools[name]
	if !known {
		return "", fmt.Errorf("unknown tool %q; discovered tools live in %s", name, r.Dir)
	}
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}

	// The model can override the deadline via timeout_secs in the input, else the tool's default applies.
	// The tool reads the same field to self-time below the deadline.
	timeout := tool.Timeout
	var requested struct {
		TimeoutSecs *int `json:"timeout_secs"`
	}
	if err := json.Unmarshal(input, &requested); err == nil && requested.TimeoutSecs != nil {
		seconds := *requested.TimeoutSecs
		if seconds < 1 {
			return "", fmt.Errorf("tool %s: timeout_secs must be at least 1, got %d", name, seconds)
		}
		timeout = time.Duration(seconds) * time.Second
		if timeout > maxToolTimeout {
			timeout = maxToolTimeout
		}
	}

	// The deadline adds graceTimeout so a tool has room to exit cleanly before this hard kill.
	ctx, cancel := context.WithTimeout(context.Background(), timeout+graceTimeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, tool.Path)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// 1s after the timeout kill, Wait abandons the output pipes so a tool's hung child cannot stall the agent.
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("tool %s timed out after %s", name, tool.Timeout+graceTimeout)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case exitRuntimeFailure:
				return "", fmt.Errorf("tool %s failed: %s", name, stderr.String())
			case exitMalformedCall:
				return "", fmt.Errorf("tool %s rejected the call as malformed, fix the arguments: %s", name, stderr.String())
			}
		}
		return "", fmt.Errorf("tool %s failed (%w): %s", name, err, stderr.String())
	}
	return stdout.String(), nil
}
