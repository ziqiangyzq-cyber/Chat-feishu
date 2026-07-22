package install

import (
	"fmt"
	"path/filepath"
	"strings"
)

const binaryMutationLockSuffix = ".codex-remote-mutation.lock"

// acquireBinaryMutationLock serializes every writer of a live binary path.
// Callers must acquire the lock before checking unified-release ownership and
// hold it until all forward and rollback writes are complete.
func acquireBinaryMutationLock(binaryPath string) (func() error, error) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return nil, fmt.Errorf("binary mutation lock path is empty")
	}
	lockPath := filepath.Clean(binaryPath) + binaryMutationLockSuffix
	release, err := acquirePlatformBinaryMutationLock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("acquire binary mutation lock %s: %w", lockPath, err)
	}
	return release, nil
}
