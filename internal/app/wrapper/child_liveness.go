package wrapper

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

// providerRuntimeHangTimeout bounds how long the wrapper tolerates a child
// that neither produces stdout output nor exits after the last write to it.
// The waitErr path only observes process exits, so a provider that hangs (alive
// but unresponsive) would otherwise leave the wrapper silent indefinitely and
// rely solely on the daemon's slow dispatch timeout to recover. The wrapper
// reports the problem and terminates itself via errCh; the daemon's existing
// managed-headless recovery path then rebuilds the instance.
const providerRuntimeHangTimeout = 90 * time.Second

// childHangCheckInterval is the cadence at which the hang watchdog re-examines
// the write/activity timestamps.
const childHangCheckInterval = 5 * time.Second

// childActivityWatch tracks, for one child session generation, the last time
// the wrapper wrote to the child and the last time the child produced stdout
// activity. A write that goes unanswered for providerRuntimeHangTimeout
// indicates a hung provider.
type childActivityWatch struct {
	lastWrite    atomic.Int64 // unix nanos since epoch; 0 = never wrote
	lastActivity atomic.Int64 // unix nanos since epoch; 0 = never read
}

func newChildActivityWatch() *childActivityWatch {
	return &childActivityWatch{}
}

// NoteWrite records a successful write to the child. The hang window restarts
// from this instant; any subsequent stdout activity satisfies it.
func (w *childActivityWatch) NoteWrite() {
	w.lastWrite.Store(time.Now().UnixNano())
}

// NoteActivity records stdout activity from the child. It must only be called
// for frames from the current session generation (after the stale-generation
// check in stdoutLoop).
func (w *childActivityWatch) NoteActivity() {
	w.lastActivity.Store(time.Now().UnixNano())
}

// childHangWatchLoop reports a hung provider through errCh once the child has
// been silent for hangTimeout after the most recent write, then returns. It is
// scoped to one session generation: when activeGeneration moves on (child
// restart) or ctx is cancelled (session IO torn down) it exits without firing.
func childHangWatchLoop(ctx context.Context, watch *childActivityWatch, activeGeneration *int64, generation int64, pid int, errCh chan<- error, reportProblem func(agentproto.ErrorInfo), debugf func(string, ...any), checkInterval, hangTimeout time.Duration) {
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if activeGeneration != nil && atomic.LoadInt64(activeGeneration) != generation {
			return
		}
		lastWrite := watch.lastWrite.Load()
		if lastWrite == 0 || watch.lastActivity.Load() >= lastWrite {
			// Nothing outstanding, or every write has been answered by stdout
			// activity since.
			continue
		}
		hungFor := time.Since(time.Unix(0, lastWrite))
		if hungFor < hangTimeout {
			continue
		}
		problem := agentproto.ErrorInfo{
			Code:      "provider_hung",
			Layer:     "wrapper",
			Stage:     "provider_runtime_hang",
			Operation: "codex.stdout",
			Message:   fmt.Sprintf("Codex 子进程已无输出超过 %s，判定挂死并终止实例。", hangTimeout),
			Details: fmt.Sprintf(
				"pid=%d last_write=%s last_activity=%s",
				pid,
				time.Unix(0, lastWrite).Format(time.RFC3339),
				time.Unix(0, watch.lastActivity.Load()).Format(time.RFC3339),
			),
			Retryable: true,
		}
		if reportProblem != nil {
			reportProblem(problem)
		}
		if debugf != nil {
			debugf("provider hung: pid=%d no stdout activity for %s since last write", pid, hungFor.Round(time.Second))
		}
		select {
		case errCh <- problem:
		case <-ctx.Done():
		}
		return
	}
}
