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
	"time"

	"github.com/toddky/todd-agent/internal/llm"
)

// defaultToolTimeout applies when a tool's schema omits timeout_secs.
// 10s is the agreed default for quick tools like read; the agent enforces
// the timeout around the exec, tools don't time themselves out.
const defaultToolTimeout = 10 * time.Second

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
func LoadAll(dir string) (*Registry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read tools dir %s: %w", dir, err)
	}

	registry := &Registry{Dir: dir, Tools: make(map[string]Tool)}
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

		tool, err := load(entry.Name(), path)
		if err != nil {
			return nil, err
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
			InputSchema: tool.InputSchema,
		})
	}
	return defs
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
// The tool's timeout is enforced here, around the exec, so individual tools
// never implement their own.
func (r *Registry) Run(name string, input json.RawMessage) (string, error) {
	tool, known := r.Tools[name]
	if !known {
		return "", fmt.Errorf("unknown tool %q; discovered tools live in %s", name, r.Dir)
	}
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}

	ctx, cancel := context.WithTimeout(context.Background(), tool.Timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, tool.Path)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("tool %s timed out after %s", name, tool.Timeout)
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
