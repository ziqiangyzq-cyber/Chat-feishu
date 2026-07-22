package install

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBinaryMutationLockSerializesOwnershipRecheck(t *testing.T) {
	dir := t.TempDir()
	livePath := filepath.Join(dir, "bin", "codex-remote")
	if err := os.MkdirAll(filepath.Dir(livePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(livePath, []byte("legacy"), 0o755); err != nil {
		t.Fatal(err)
	}

	releaseFirst, err := acquireBinaryMutationLock(livePath)
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		releaseSecond, lockErr := acquireBinaryMutationLock(livePath)
		if lockErr != nil {
			result <- lockErr
			return
		}
		defer func() { _ = releaseSecond() }()
		result <- EnsureStandaloneUpgradeAllowed(livePath)
	}()
	<-started
	select {
	case err := <-result:
		t.Fatalf("second writer acquired lock early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseDir := filepath.Join(dir, "unified", "releases", "commit-sha")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	releaseBinary := filepath.Join(releaseDir, "codex-remote")
	if err := os.WriteFile(releaseBinary, []byte("unified"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, UnifiedReleaseMarkerFilename), []byte("managed\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(livePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(releaseBinary, livePath); err != nil {
		t.Fatal(err)
	}
	if err := releaseFirst(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-result:
		if !errors.Is(err, ErrUnifiedReleaseManaged) {
			t.Fatalf("waiting writer error = %v, want ErrUnifiedReleaseManaged", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting writer did not resume")
	}
}
