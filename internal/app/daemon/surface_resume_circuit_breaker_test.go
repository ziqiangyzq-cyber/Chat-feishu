package daemon

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/daemon/surfaceresume"
)

func TestSurfaceResumeCircuitBreakerStopsThirdRepeatedTimeout(t *testing.T) {
	app := &App{}
	app.surfaceResumeRuntime.recovery = map[string]*surfaceResumeRecoveryState{
		"surface-1": {Entry: surfaceresume.Entry{SurfaceSessionID: "surface-1", ProductMode: "normal"}},
	}
	now := time.Now()
	for attempt := 1; attempt <= 3; attempt++ {
		_, emit := app.recordSurfaceResumeFailureLocked("surface-1", "headless_restore_start_timeout", now.Add(time.Duration(attempt)*time.Second))
		if emit != (attempt == 1 || attempt == 3) {
			t.Fatalf("attempt %d emit = %t", attempt, emit)
		}
	}
	recovery := app.surfaceResumeRuntime.recovery["surface-1"]
	if recovery.TerminalFailureCode != "headless_restore_start_timeout" || surfaceResumeRecoveryDue(recovery, now.Add(time.Hour)) {
		t.Fatalf("circuit breaker did not stop recovery: %#v", recovery)
	}
	app.clearSurfaceResumeBackoffLocked("surface-1")
	if recovery.TerminalFailureCode != "" || recovery.ConsecutiveFailures != 0 || !surfaceResumeRecoveryDue(recovery, now.Add(time.Hour)) {
		t.Fatalf("explicit reset did not clear circuit breaker: %#v", recovery)
	}
}

func TestStartingAttemptDoesNotClearFailureStreak(t *testing.T) {
	app := &App{}
	recovery := &surfaceResumeRecoveryState{
		Entry:               surfaceresume.Entry{SurfaceSessionID: "surface-1", ResumeFailureCode: "headless_restore_start_timeout", ResumeFailureCount: 2},
		LastFailureCode:     "headless_restore_start_timeout",
		ConsecutiveFailures: 2,
	}
	app.surfaceResumeRuntime.recovery = map[string]*surfaceResumeRecoveryState{"surface-1": recovery}
	app.clearSurfaceResumeAttemptProgressLocked("surface-1")
	if recovery.LastFailureCode != "headless_restore_start_timeout" || recovery.ConsecutiveFailures != 2 || recovery.Entry.ResumeFailureCount != 2 {
		t.Fatalf("starting attempt cleared durable failure streak: %#v", recovery)
	}
}

func TestSyncRestoresDeterministicTerminalFailure(t *testing.T) {
	store := surfaceresume.NewStore(t.TempDir() + "/surface-resume-state.json")
	entry := surfaceresume.Entry{
		SurfaceSessionID:   "surface-1",
		ProductMode:        "normal",
		Backend:            "claude",
		ResumeThreadID:     "thread-1",
		ResumeThreadCWD:    t.TempDir(),
		ResumeWorkspaceKey: t.TempDir(),
		ResumeHeadless:     true,
		ResumeFailureCode:  "headless_restore_workspace_missing",
		ResumeFailureCount: 1,
	}
	if err := store.Put(entry); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	app.surfaceResumeRuntime.store = store
	app.syncSurfaceResumeRecoveryStateLocked()
	recovery := app.surfaceResumeRuntime.recovery["surface-1"]
	if recovery == nil || recovery.TerminalFailureCode != entry.ResumeFailureCode {
		t.Fatalf("terminal failure was not restored: %#v", recovery)
	}
}
