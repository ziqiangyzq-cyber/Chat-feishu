package install

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureStandaloneUpgradeAllowedRejectsUnifiedReleaseThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	releaseDir := filepath.Join(dir, "releases", "commit-sha")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(releaseDir, "codex-remote")
	if err := os.WriteFile(binaryPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, UnifiedReleaseMarkerFilename), []byte("managed\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "current", "codex-remote")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binaryPath, linkPath); err != nil {
		t.Fatal(err)
	}

	if err := EnsureStandaloneUpgradeAllowed(linkPath); !errors.Is(err, ErrUnifiedReleaseManaged) {
		t.Fatalf("EnsureStandaloneUpgradeAllowed() error = %v, want ErrUnifiedReleaseManaged", err)
	}
}

func TestEnsureStandaloneUpgradeAllowedPermitsStandaloneBinary(t *testing.T) {
	binaryPath := filepath.Join(t.TempDir(), "codex-remote")
	if err := os.WriteFile(binaryPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureStandaloneUpgradeAllowed(binaryPath); err != nil {
		t.Fatalf("EnsureStandaloneUpgradeAllowed() error = %v", err)
	}
}

func TestEnsureStandaloneUpgradeAllowedPermitsOrdinaryUnresolvedSymlink(t *testing.T) {
	dir := t.TempDir()
	linkPath := filepath.Join(dir, "codex-remote")
	if err := os.Symlink(filepath.Join(dir, "missing", "codex-remote"), linkPath); err != nil {
		t.Fatal(err)
	}

	if err := EnsureStandaloneUpgradeAllowed(linkPath); err != nil {
		t.Fatalf("EnsureStandaloneUpgradeAllowed() error = %v", err)
	}
}

func TestEnsureStandaloneUpgradeAllowedRejectsUnresolvedUnifiedSymlink(t *testing.T) {
	dir := t.TempDir()
	linkPath := filepath.Join(dir, "bin", "codex-remote")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "unified", "stacks", "claude-remote", "current", "codex-remote")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatal(err)
	}

	if err := EnsureStandaloneUpgradeAllowed(linkPath); !errors.Is(err, ErrUnifiedReleaseManaged) {
		t.Fatalf("EnsureStandaloneUpgradeAllowed() error = %v, want ErrUnifiedReleaseManaged", err)
	}
}

func TestInstallBinaryRejectsUnifiedManagedTarget(t *testing.T) {
	dir := t.TempDir()
	releaseDir := filepath.Join(dir, "unified", "releases", "commit-sha")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	releaseBinary := filepath.Join(releaseDir, "codex-remote")
	if err := os.WriteFile(releaseBinary, []byte("managed-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, UnifiedReleaseMarkerFilename), []byte("managed\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	installDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(installDir, "codex-remote")
	if err := os.Symlink(releaseBinary, targetPath); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(dir, "package", "codex-remote")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("replacement"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := installBinary(sourcePath, installDir); !errors.Is(err, ErrUnifiedReleaseManaged) {
		t.Fatalf("installBinary() error = %v, want ErrUnifiedReleaseManaged", err)
	}
	info, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("unified-managed target was replaced instead of rejected")
	}
}
