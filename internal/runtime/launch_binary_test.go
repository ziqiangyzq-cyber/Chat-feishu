package relayruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStableLaunchBinaryPathPreservesSymlinkEntrypoint(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "release", "codex-remote")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	entrypoint := filepath.Join(dir, "bin", "codex-remote")
	if err := os.MkdirAll(filepath.Dir(entrypoint), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, entrypoint); err != nil {
		t.Fatal(err)
	}
	if got := StableLaunchBinaryPath(entrypoint, target); got != entrypoint {
		t.Fatalf("StableLaunchBinaryPath() = %q, want %q", got, entrypoint)
	}
}

func TestStableLaunchBinaryPathRejectsDifferentEntrypoint(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	other := filepath.Join(dir, "other")
	for _, path := range []string{target, other} {
		if err := os.WriteFile(path, []byte(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := StableLaunchBinaryPath(other, target); got != target {
		t.Fatalf("StableLaunchBinaryPath() = %q, want %q", got, target)
	}
}
