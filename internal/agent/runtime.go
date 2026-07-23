package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// runtimeDir is this process's private runtime directory, set by Setup.
// One agent process is exactly one instance, so package state is fine here.
var runtimeDir string

// GetRuntimeDir returns the directory created by Setup, or "" before Setup runs.
func GetRuntimeDir() string {
	return runtimeDir
}

// Setup creates this agent instance's private runtime directory and symlinks
// every executable tool script from sourceDir into its tools subdir.
// Every running agent gets its own directory (keyed by pid) because each
// instance can be allowed a different tool set.
func Setup(sourceDir string) error {
	baseDir := os.Getenv("XDG_RUNTIME_DIR")
	if baseDir == "" {
		// logind puts the per-user runtime dir here; try it before falling back to tmp.
		baseDir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}

	instanceName := fmt.Sprintf("agent-%d", os.Getpid())
	runtimeDir = filepath.Join(baseDir, instanceName)
	if err := makePrivateDir(runtimeDir); err != nil {
		// No usable runtime dir (containers, cron); tmp is the XDG-sanctioned fallback.
		runtimeDir = filepath.Join(os.TempDir(), instanceName)
		if err := makePrivateDir(runtimeDir); err != nil {
			return fmt.Errorf("create runtime dir %s: %w", runtimeDir, err)
		}
	}

	toolsDir := filepath.Join(runtimeDir, "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		return fmt.Errorf("create tools dir %s: %w", toolsDir, err)
	}

	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("read source tools dir %s: %w", sourceDir, err)
	}
	for _, entry := range entries {
		src, err := filepath.Abs(filepath.Join(sourceDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("resolve tool %s: %w", entry.Name(), err)
		}
		info, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("stat tool %s: %w", src, err)
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}

		dst := filepath.Join(toolsDir, entry.Name())
		// A crashed agent with the same pid can leave a stale link; replace it.
		if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("clear stale tool link %s: %w", dst, err)
		}
		if err := os.Symlink(src, dst); err != nil {
			return fmt.Errorf("link tool %s -> %s: %w", dst, src, err)
		}
	}
	return nil
}

// Cleanup deletes the directory created by Setup so exited agents don't
// accumulate stale dirs.
func Cleanup() error {
	if runtimeDir == "" {
		return nil
	}
	if err := os.RemoveAll(runtimeDir); err != nil {
		return fmt.Errorf("remove runtime dir %s: %w", runtimeDir, err)
	}
	runtimeDir = ""
	return nil
}

// makePrivateDir creates dir with 0700 permissions.
// MkdirAll keeps existing modes, so an already-existing dir is re-chmodded to stay private.
func makePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}
