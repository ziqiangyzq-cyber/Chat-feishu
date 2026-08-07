package wrapper

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func startHangWatchForTest(t *testing.T, watch *childActivityWatch, activeGeneration *int64, generation int64, checkInterval, hangTimeout time.Duration) (chan error, context.CancelFunc) {
	t.Helper()
	errCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	reported := make(chan struct{}, 1)
	go childHangWatchLoop(ctx, watch, activeGeneration, generation, 1234, errCh, func(agentproto.ErrorInfo) {
		reported <- struct{}{}
	}, nil, checkInterval, hangTimeout)
	t.Cleanup(cancel)
	return errCh, cancel
}

func expectHungErr(t *testing.T, errCh chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected provider_hung error, got nil")
		}
		if !strings.Contains(err.Error(), "挂死") {
			t.Fatalf("expected hung-provider error, got %q", err.Error())
		}
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("expected provider_hung error, got none")
		return nil
	}
}

func expectNoHungErr(t *testing.T, errCh chan error) {
	t.Helper()
	select {
	case err := <-errCh:
		t.Fatalf("expected no hang error, got %v", err)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestChildHangWatchLoopFiresOnUnansweredWrite(t *testing.T) {
	watch := newChildActivityWatch()
	errCh, _ := startHangWatchForTest(t, watch, nil, 1, 10*time.Millisecond, 50*time.Millisecond)

	watch.NoteWrite()
	expectHungErr(t, errCh)
}

func TestChildHangWatchLoopActivityAnswersWrite(t *testing.T) {
	watch := newChildActivityWatch()
	errCh, _ := startHangWatchForTest(t, watch, nil, 1, 10*time.Millisecond, 50*time.Millisecond)

	watch.NoteWrite()
	time.Sleep(20 * time.Millisecond)
	watch.NoteActivity()
	expectNoHungErr(t, errCh)
}

func TestChildHangWatchLoopStaleGenerationSkips(t *testing.T) {
	watch := newChildActivityWatch()
	var activeGeneration int64 = 2
	errCh, _ := startHangWatchForTest(t, watch, &activeGeneration, 1, 10*time.Millisecond, 30*time.Millisecond)

	watch.NoteWrite()
	// Loop observes a stale generation and exits without firing.
	time.Sleep(80 * time.Millisecond)
	expectNoHungErr(t, errCh)
}

func TestChildHangWatchLoopNoWriteNeverFires(t *testing.T) {
	watch := newChildActivityWatch()
	errCh, _ := startHangWatchForTest(t, watch, nil, 1, 10*time.Millisecond, 30*time.Millisecond)

	// Never write; idle child is not a hang.
	time.Sleep(80 * time.Millisecond)
	expectNoHungErr(t, errCh)
}

func TestChildHangWatchLoopGenerationAdvanceStopsLoop(t *testing.T) {
	watch := newChildActivityWatch()
	var activeGeneration int64 = 1
	errCh, cancel := startHangWatchForTest(t, watch, &activeGeneration, 1, 10*time.Millisecond, 30*time.Millisecond)

	watch.NoteWrite()
	atomic.StoreInt64(&activeGeneration, 2)
	time.Sleep(80 * time.Millisecond)
	expectNoHungErr(t, errCh)

	// A later write after the loop exited must not surface either.
	watch.NoteWrite()
	expectNoHungErr(t, errCh)
	cancel()
}

func TestChildActivityWatchActivityOlderThanWriteStillHangs(t *testing.T) {
	watch := newChildActivityWatch()
	errCh, _ := startHangWatchForTest(t, watch, nil, 1, 10*time.Millisecond, 40*time.Millisecond)

	// Activity that predates the write (e.g. last idle output) must not answer it.
	watch.NoteActivity()
	time.Sleep(5 * time.Millisecond)
	watch.NoteWrite()
	expectHungErr(t, errCh)
}
