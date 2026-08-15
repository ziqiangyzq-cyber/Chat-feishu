package wrapper

import (
	"context"
	"os"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/debuglog"
	"github.com/kxn/codex-remote-feishu/internal/execlaunch"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
)

func (a *App) launchGrokChildSession(ctx context.Context, rawLogger *debuglog.RawLogger, reportProblem func(agentproto.ErrorInfo), resume *claudeLaunchResumeTarget) (*childSession, error) {
	childCtx, childCancel := context.WithCancel(ctx)
	binary, err := os.Executable()
	if err != nil {
		childCancel()
		return nil, err
	}
	binary = relayruntime.StableLaunchBinaryPath(os.Args[0], binary)
	args := []string{"grok-bridge", "--cwd", a.config.WorkspaceRoot}
	if resume != nil && strings.TrimSpace(resume.ThreadID) != "" {
		args = append(args, "--session", strings.TrimSpace(resume.ThreadID))
	}
	cmd := execlaunch.CommandContext(childCtx, binary, args...)
	cmd.Dir = a.config.WorkspaceRoot
	cmd.Env = os.Environ()
	childStdin, childStdout, childStderr, err := startChild(cmd)
	if err != nil {
		childCancel()
		return nil, err
	}
	bootstrappedStdout, err := a.bootstrapClaude(childStdin, childStdout, rawLogger, reportProblem)
	if err != nil {
		childCancel()
		_ = cmd.Wait()
		return nil, err
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	return &childSession{cmd: cmd, stdin: childStdin, stdout: bootstrappedStdout, stderr: childStderr, waitErr: waitErr, cancel: childCancel}, nil
}
