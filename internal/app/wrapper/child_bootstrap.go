package wrapper

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	relayruntime "github.com/kxn/codex-remote-feishu/internal/runtime"
)

const (
	providerBootstrapTimeout = 30 * time.Second
	providerStderrTailLimit  = 32 * 1024
)

// childBootstrapSupervisor owns the provider process from cmd.Start until the
// normal session I/O loops take over. In particular, stderr must be consumed
// while bootstrap is waiting on stdout or a noisy provider can deadlock before
// the wrapper ever connects to relay.
type childBootstrapSupervisor struct {
	cmd        *exec.Cmd
	waitErr    chan error
	stderrDone chan struct{}
	stderrTail *boundedTail
}

func superviseStartedChild(cmd *exec.Cmd, stderr io.Reader) *childBootstrapSupervisor {
	s := &childBootstrapSupervisor{
		cmd:        cmd,
		waitErr:    make(chan error, 1),
		stderrDone: make(chan struct{}),
		stderrTail: &boundedTail{limit: providerStderrTailLimit},
	}
	go func() {
		if stderr != nil {
			// Preserve the wrapper's normal stderr diagnostics while also keeping a
			// bounded tail for a typed bootstrap failure.
			_, _ = io.Copy(io.MultiWriter(os.Stderr, s.stderrTail), stderr)
		}
		close(s.stderrDone)
	}()
	go func() { s.waitErr <- cmd.Wait() }()
	return s
}

func (s *childBootstrapSupervisor) run(ctx context.Context, backend agentproto.Backend, bootstrap func() (io.Reader, error)) (io.Reader, error) {
	bootstrapCtx, cancel := context.WithTimeout(ctx, providerBootstrapTimeout)
	defer cancel()
	type result struct {
		stdout io.Reader
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		stdout, err := bootstrap()
		resultCh <- result{stdout: stdout, err: err}
	}()

	var code, message string
	select {
	case result := <-resultCh:
		if result.err == nil {
			return result.stdout, nil
		}
		code, message = "provider_bootstrap_failed", result.err.Error()
	case waitErr := <-s.waitErr:
		code, message = "provider_bootstrap_process_exited", fmt.Sprintf("provider exited before bootstrap completed: %v", waitErr)
	case <-bootstrapCtx.Done():
		code, message = "provider_bootstrap_timeout", "provider bootstrap timed out"
	}

	if s.cmd != nil && s.cmd.Process != nil {
		_ = relayruntime.TerminateManagedProcess(s.cmd.Process.Pid, wrapperChildStopGrace)
	}
	select {
	case <-s.stderrDone:
	case <-time.After(time.Second):
	}
	problem := agentproto.ErrorInfo{
		Code:      code,
		Layer:     "wrapper",
		Stage:     "provider_bootstrap",
		Operation: "initialize",
		Message:   message,
		Details:   strings.TrimSpace(s.stderrTail.String()),
		Retryable: code == "provider_bootstrap_timeout",
	}
	if backend != "" {
		problem.Operation = string(backend) + ".initialize"
	}
	return nil, problem.Normalize()
}

type boundedTail struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (b *boundedTail) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	b.buf = append(b.buf, p...)
	if b.limit > 0 && len(b.buf) > b.limit {
		b.buf = bytes.Clone(b.buf[len(b.buf)-b.limit:])
	}
	return written, nil
}

func (b *boundedTail) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(bytes.Clone(b.buf))
}
