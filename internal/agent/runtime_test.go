package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupLaterDirWinsOnCollision(t *testing.T) {
	earlierDir := t.TempDir()
	laterDir := t.TempDir()
	writeTool(t, earlierDir, "shared_tool", echoTool)
	writeTool(t, earlierDir, "only_earlier", echoTool)
	writeTool(t, laterDir, "shared_tool", echoTool)

	// Capture stderr to check the shadow warning without polluting test output.
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	savedStderr := os.Stderr
	os.Stderr = writeEnd

	setupErr := Setup(earlierDir, laterDir)

	os.Stderr = savedStderr
	writeEnd.Close()
	captured := make([]byte, 4096)
	numRead, _ := readEnd.Read(captured)
	warning := string(captured[:numRead])

	if setupErr != nil {
		t.Fatalf("Setup() error: %v", setupErr)
	}
	defer func() {
		if err := Cleanup(); err != nil {
			t.Errorf("Cleanup() error: %v", err)
		}
	}()

	link, err := os.Readlink(filepath.Join(GetRuntimeDir(), "tools", "shared_tool"))
	if err != nil {
		t.Fatalf("read shared_tool link: %v", err)
	}
	want := filepath.Join(laterDir, "shared_tool")
	if link != want {
		t.Errorf("shared_tool links to %s, want the later dir's %s", link, want)
	}

	if _, err := os.Stat(filepath.Join(GetRuntimeDir(), "tools", "only_earlier")); err != nil {
		t.Errorf("only_earlier missing from runtime tools dir: %v", err)
	}

	if !strings.Contains(warning, "shadows") || !strings.Contains(warning, "shared_tool") {
		t.Errorf("stderr = %q, want a shadow warning naming shared_tool", warning)
	}
	if strings.Contains(warning, "only_earlier") {
		t.Errorf("stderr = %q, warned about only_earlier which has no collision", warning)
	}
}
