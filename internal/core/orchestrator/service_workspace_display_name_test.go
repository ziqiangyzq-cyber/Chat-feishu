package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestRetargetManagedHeadlessPreservesCustomDisplayName(t *testing.T) {
	now := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	inst := &state.InstanceRecord{
		InstanceID:    "inst-headless",
		DisplayName:   "claude-remote",
		WorkspaceRoot: "/tmp/old",
		WorkspaceKey:  "/tmp/old",
		ShortName:     "old",
		Source:        "headless",
		Managed:       true,
	}

	svc.retargetManagedHeadlessInstance(inst, "/home/demo/site")

	if inst.DisplayName != "claude-remote" {
		t.Fatalf("display name = %q, want %q", inst.DisplayName, "claude-remote")
	}
	if inst.ShortName != "site" {
		t.Fatalf("short name = %q, want %q", inst.ShortName, "site")
	}
}
