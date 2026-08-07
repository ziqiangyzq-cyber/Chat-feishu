//go:build !windows

package wrapper

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestChildBootstrapSupervisorCapturesStderrAndStopsProcess(t *testing.T) {
	cmd := exec.Command("sh", "-c", "printf 'resume rejected' >&2; sleep 30")
	_, _, stderr, err := startChild(cmd)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := superviseStartedChild(cmd, stderr)
	_, err = supervisor.run(context.Background(), agentproto.BackendClaude, func() (io.Reader, error) {
		time.Sleep(100 * time.Millisecond)
		return nil, errors.New("initialize failed")
	})
	var problem agentproto.ErrorInfo
	if !errors.As(err, &problem) {
		t.Fatalf("error = %T %v, want ErrorInfo", err, err)
	}
	if problem.Code != "provider_bootstrap_failed" || !strings.Contains(problem.Details, "resume rejected") {
		t.Fatalf("problem = %#v", problem)
	}
	select {
	case <-supervisor.waitErr:
	case <-time.After(2 * time.Second):
		t.Fatal("provider process was not reaped after bootstrap failure")
	}
}

func TestBoundedTailKeepsNewestBytes(t *testing.T) {
	tail := &boundedTail{limit: 5}
	_, _ = tail.Write([]byte("abcdefgh"))
	if got := tail.String(); got != "defgh" {
		t.Fatalf("tail = %q", got)
	}
}
