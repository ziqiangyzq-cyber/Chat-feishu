package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/app/daemon/surfaceresume"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/orchestrator"
)

// 工作区策略拒绝的 pinned headless 恢复目标是永久失败：
// 必须发一条失败 notice、清除 pinned 恢复目标并终止重试，
// 不允许再出现 2026-07-11 事故式的无终态 30s 重试。
func TestHeadlessResumePolicyDeniedClearsPinnedTarget(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	putSurfaceResumeStateForTest(t, stateDir, surfaceresume.Entry{
		SurfaceSessionID:   "surface-1",
		GatewayID:          "app-1",
		ChatID:             "chat-1",
		ActorUserID:        "user-1",
		ProductMode:        "normal",
		Backend:            "codex",
		CodexProviderID:    "default",
		Verbosity:          "normal",
		ResumeThreadID:     "thread-1",
		ResumeThreadTitle:  "修复登录流程",
		ResumeThreadCWD:    "/data/other/repo",
		ResumeWorkspaceKey: "/data/other/repo",
		ResumeRouteMode:    "pinned",
		ResumeHeadless:     true,
	})
	app := newRestoreHintTestApp(stateDir)
	app.service.SetGatewaySurfacePolicies(map[string]orchestrator.GatewaySurfacePolicy{
		"app-1": {WorkspaceRoots: []string{"/data/allowed"}},
	})
	if app.surfaceResumeRuntime.recovery["surface-1"] == nil {
		t.Fatal("expected recovery entry before policy-denied attempt")
	}

	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	events := app.maybeRecoverHeadlessSurfacesLocked(now)

	foundNotice := false
	for _, event := range events {
		if event.Kind == eventcontract.KindNotice && event.Notice != nil &&
			strings.TrimSpace(event.Notice.Code) == "headless_restore_workspace_policy_denied" {
			foundNotice = true
		}
	}
	if !foundNotice {
		t.Fatalf("expected policy-denied restore notice, got %#v", events)
	}
	if recovery := app.surfaceResumeRuntime.recovery["surface-1"]; recovery != nil {
		t.Fatalf("expected recovery entry cleared after permanent policy denial, got %#v", recovery)
	}
	entry, ok := app.surfaceResumeRuntime.store.Get("surface-1")
	if ok && (strings.TrimSpace(entry.ResumeThreadID) != "" || strings.TrimSpace(entry.ResumeWorkspaceKey) != "") {
		t.Fatalf("expected pinned resume target cleared, got %#v", entry)
	}

	// 再次 tick：不再重试、不再发事件。
	if more := app.maybeRecoverHeadlessSurfacesLocked(now.Add(time.Minute)); len(more) != 0 {
		t.Fatalf("expected no further resume attempts after clearing, got %#v", more)
	}
}
