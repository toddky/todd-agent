package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTool drops an executable fake tool script into dir.
func writeTool(t *testing.T, dir, name, body string) {
	t.Helper()
	script := "#!/usr/bin/env bash\n" + body
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tool %s: %v", name, err)
	}
}

// echoTool is a minimal contract-compliant tool: valid schema, echoes its stdin back.
const echoTool = `if [[ "${1:-}" == "--schema" ]]; then
	echo '{"description": "echo input", "input_schema": {"type": "object"}, "timeout_secs": 5}'
	exit 0
fi
cat
`

func TestLoadAllDiscoversTools(t *testing.T) {
	dir := t.TempDir()
	writeTool(t, dir, "echo_tool", echoTool)

	registry, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}

	tool, known := registry.Tools["echo_tool"]
	if !known {
		t.Fatalf("LoadAll() did not discover echo_tool; got %v", registry.Tools)
	}
	if tool.Description != "echo input" {
		t.Errorf("Description = %q, want \"echo input\"", tool.Description)
	}
	if tool.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s from timeout_secs", tool.Timeout)
	}
}

func TestLoadAllAppliesDefaultTimeout(t *testing.T) {
	dir := t.TempDir()
	writeTool(t, dir, "no_timeout", `if [[ "${1:-}" == "--schema" ]]; then
	echo '{"description": "d", "input_schema": {"type": "object"}}'
	exit 0
fi
cat
`)

	registry, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}
	if got := registry.Tools["no_timeout"].Timeout; got != defaultToolTimeout {
		t.Errorf("Timeout = %v, want default %v", got, defaultToolTimeout)
	}
}

func TestLoadAllSkipsNonExecutables(t *testing.T) {
	dir := t.TempDir()
	writeTool(t, dir, "real_tool", echoTool)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a tool"), 0o644); err != nil {
		t.Fatalf("write non-executable: %v", err)
	}

	registry, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}
	if _, known := registry.Tools["notes.txt"]; known {
		t.Error("LoadAll() discovered a non-executable file")
	}
	if _, known := registry.Tools["real_tool"]; !known {
		t.Error("LoadAll() missed the executable tool")
	}
}

func TestLoadAllRejectsBadSchema(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "invalid json",
			body: `echo 'not json'` + "\n",
		},
		{
			name: "missing description",
			body: `echo '{"input_schema": {"type": "object"}}'` + "\n",
		},
		{
			name: "schema exec fails",
			body: `exit 1` + "\n",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTool(t, dir, "bad_tool", testCase.body)
			if _, err := LoadAll(dir); err == nil {
				t.Error("LoadAll() accepted a tool with a bad schema")
			}
		})
	}
}

func TestRunReturnsStdout(t *testing.T) {
	dir := t.TempDir()
	writeTool(t, dir, "echo_tool", echoTool)
	registry, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}

	got, err := registry.Run("echo_tool", json.RawMessage(`{"key": "value"}`))
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if got != `{"key": "value"}` {
		t.Errorf("Run() = %q, want the echoed input", got)
	}
}

func TestRunUnknownTool(t *testing.T) {
	registry := &Registry{Dir: "/nowhere", Tools: map[string]Tool{}}
	if _, err := registry.Run("ghost", nil); err == nil {
		t.Error("Run() accepted an unknown tool")
	}
}

func TestRunExitCodes(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantSubstr string
	}{
		{
			name: "exit 1 runtime failure",
			body: `if [[ "${1:-}" == "--schema" ]]; then
	echo '{"description": "d", "input_schema": {"type": "object"}}'
	exit 0
fi
echo "it broke" 1>&2
exit 1
`,
			wantSubstr: "failed: it broke",
		},
		{
			name: "exit 2 malformed call",
			body: `if [[ "${1:-}" == "--schema" ]]; then
	echo '{"description": "d", "input_schema": {"type": "object"}}'
	exit 0
fi
echo "bad args" 1>&2
exit 2
`,
			wantSubstr: "malformed",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTool(t, dir, "fail_tool", testCase.body)
			registry, err := LoadAll(dir)
			if err != nil {
				t.Fatalf("LoadAll() error: %v", err)
			}

			_, err = registry.Run("fail_tool", nil)
			if err == nil {
				t.Fatal("Run() succeeded, want error")
			}
			if !strings.Contains(err.Error(), testCase.wantSubstr) {
				t.Errorf("Run() error = %q, want substring %q", err, testCase.wantSubstr)
			}
		})
	}
}

func TestRunEnforcesTimeout(t *testing.T) {
	dir := t.TempDir()
	writeTool(t, dir, "slow_tool", `if [[ "${1:-}" == "--schema" ]]; then
	echo '{"description": "d", "input_schema": {"type": "object"}, "timeout_secs": 1}'
	exit 0
fi
sleep 10
`)
	registry, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}

	start := time.Now()
	_, err = registry.Run("slow_tool", nil)
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("Run() error = %v, want timeout error", err)
	}
	// 9s: above the 1s timeout + 5s grace + 1s WaitDelay, but below the 10s sleep, so a hang is caught.
	if elapsed > 9*time.Second {
		t.Errorf("Run() took %v, timeout was not enforced", elapsed)
	}
}

func TestRunTimeoutSecsOverridesDefault(t *testing.T) {
	dir := t.TempDir()
	// Declared default is 60s; the call requests 1s, which must win so the sleep is cut short.
	writeTool(t, dir, "slow_tool", `if [[ "${1:-}" == "--schema" ]]; then
	echo '{"description": "d", "input_schema": {"type": "object"}, "timeout_secs": 60}'
	exit 0
fi
sleep 30
`)
	registry, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}

	start := time.Now()
	_, err = registry.Run("slow_tool", json.RawMessage(`{"timeout_secs": 1}`))
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("Run() error = %v, want timeout error", err)
	}
	// 9s: above the requested 1s + 5s grace + 1s WaitDelay, well below the tool's 60s default and 30s sleep.
	if elapsed > 9*time.Second {
		t.Errorf("Run() took %v, requested timeout_secs was not applied", elapsed)
	}
}

func TestRunRejectsNonPositiveTimeoutSecs(t *testing.T) {
	dir := t.TempDir()
	writeTool(t, dir, "echo_tool", echoTool)
	registry, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}

	if _, err := registry.Run("echo_tool", json.RawMessage(`{"timeout_secs": 0}`)); err == nil {
		t.Error("Run() accepted timeout_secs of 0")
	}
}

func TestDefinitionsInjectsTimeoutProperty(t *testing.T) {
	dir := t.TempDir()
	writeTool(t, dir, "echo_tool", echoTool)
	registry, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll() error: %v", err)
	}

	defs := registry.Definitions()
	if len(defs) != 1 {
		t.Fatalf("Definitions() returned %d defs, want 1", len(defs))
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(defs[0].InputSchema, &schema); err != nil {
		t.Fatalf("advertised schema is not valid JSON: %v", err)
	}
	if _, ok := schema.Properties["timeout_secs"]; !ok {
		t.Errorf("advertised schema missing injected timeout_secs; got %s", defs[0].InputSchema)
	}
}
