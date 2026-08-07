package relayruntime

import (
	"os/exec"
	"time"
)

func TerminateProcess(pid int, grace time.Duration) error {
	return terminateProcess(pid, grace)
}

// PrepareManagedProcess isolates a provider process so its complete descendant
// tree can be stopped when bootstrap or the wrapper session fails.
func PrepareManagedProcess(cmd *exec.Cmd) {
	prepareManagedProcess(cmd)
}

func TerminateManagedProcess(pid int, grace time.Duration) error {
	return terminateManagedProcess(pid, grace)
}
