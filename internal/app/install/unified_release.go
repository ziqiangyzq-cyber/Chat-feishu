package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const UnifiedReleaseMarkerFilename = ".codex-remote-unified-release"

var ErrUnifiedReleaseManaged = errors.New("binary is managed by the unified local release operator")

// EnsureStandaloneUpgradeAllowed prevents the single-instance copy-based
// upgrader from mutating a content-addressed unified release.
func EnsureStandaloneUpgradeAllowed(binaryPath string) error {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(binaryPath))
	if err != nil {
		if os.IsNotExist(err) {
			info, lstatErr := os.Lstat(binaryPath)
			if lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
				target, readlinkErr := os.Readlink(binaryPath)
				if readlinkErr != nil {
					return fmt.Errorf("inspect unresolved binary symlink: %w", readlinkErr)
				}
				if isUnifiedReleaseSymlinkTarget(binaryPath, target) {
					return fmt.Errorf("%w: unified binary path is an unresolved symlink", ErrUnifiedReleaseManaged)
				}
				return nil
			}
			if lstatErr != nil && !os.IsNotExist(lstatErr) {
				return fmt.Errorf("inspect unresolved binary ownership: %w", lstatErr)
			}
			return nil
		}
		return fmt.Errorf("resolve current binary ownership: %w", err)
	}
	markerPath := filepath.Join(filepath.Dir(resolved), UnifiedReleaseMarkerFilename)
	if _, err := os.Lstat(markerPath); err == nil {
		return ErrUnifiedReleaseManaged
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect current binary ownership: %w", err)
	}
	return nil
}

func isUnifiedReleaseSymlinkTarget(linkPath, target string) bool {
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(linkPath), target)
	}
	target = filepath.Clean(target)
	if filepath.Base(target) != "codex-remote" {
		return false
	}
	currentDir := filepath.Dir(target)
	if filepath.Base(currentDir) != "current" {
		return false
	}
	stackDir := filepath.Dir(currentDir)
	stackID := filepath.Base(stackDir)
	return stackID != "" && stackID != "." && stackID != string(filepath.Separator) && filepath.Base(filepath.Dir(stackDir)) == "stacks"
}
