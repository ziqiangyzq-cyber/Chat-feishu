package relayruntime

import (
	"os"
	"path/filepath"
	"strings"
)

// StableLaunchBinaryPath preserves the executable entrypoint used to start the
// current process when it points at the same binary. Release managers commonly
// expose a stable symlink and garbage-collect versioned targets after upgrades;
// managed children must therefore launch through the entrypoint, not a resolved
// release path captured at daemon startup.
func StableLaunchBinaryPath(argv0, resolvedBinary string) string {
	resolvedBinary = strings.TrimSpace(resolvedBinary)
	candidate := strings.TrimSpace(argv0)
	if candidate == "" {
		return resolvedBinary
	}
	if !filepath.IsAbs(candidate) {
		return resolvedBinary
	}
	candidateInfo, candidateErr := os.Stat(candidate)
	resolvedInfo, resolvedErr := os.Stat(resolvedBinary)
	if candidateErr != nil || resolvedErr != nil || !os.SameFile(candidateInfo, resolvedInfo) {
		return resolvedBinary
	}
	return filepath.Clean(candidate)
}
