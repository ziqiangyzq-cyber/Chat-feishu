package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/testutil"
)

func policyTestInstance(instanceID, workspaceKey, threadID string, lastUsedAt time.Time) *state.InstanceRecord {
	return &state.InstanceRecord{
		InstanceID:    instanceID,
		DisplayName:   state.WorkspaceShortName(workspaceKey),
		WorkspaceRoot: workspaceKey,
		WorkspaceKey:  workspaceKey,
		ShortName:     state.WorkspaceShortName(workspaceKey),
		Online:        true,
		Threads: map[string]*state.ThreadRecord{
			threadID: {ThreadID: threadID, Name: "会话-" + threadID, CWD: workspaceKey, LastUsedAt: lastUsedAt, Loaded: true},
		},
	}
}

func firstNoticeEvent(events []eventcontract.Event) *control.Notice {
	for _, event := range events {
		if event.Kind == eventcontract.KindNotice && event.Notice != nil {
			return event.Notice
		}
	}
	return nil
}

func TestWorkspaceKeyWithinPolicyRoots(t *testing.T) {
	roots := []string{"/home/admin/site", "/data/dl/"}
	cases := []struct {
		path string
		want bool
	}{
		{"/home/admin/site", true},
		{"/home/admin/site/sub/dir", true},
		{"/home/admin/site-evil", false},
		{"/home/admin/site-evil/sub", false},
		{"/home/admin", false},
		{"/data/dl/repo", true},
		{"/data/dl", true},
		{"/data/dl2/repo", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := workspaceKeyWithinPolicyRoots(tc.path, roots); got != tc.want {
			t.Fatalf("workspaceKeyWithinPolicyRoots(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestClampAccessModeToMax(t *testing.T) {
	cases := []struct {
		mode, max, want string
	}{
		{agentproto.AccessModeFullAccess, agentproto.AccessModeAcceptEdits, agentproto.AccessModeAcceptEdits},
		{agentproto.AccessModeFullAccess, agentproto.AccessModeConfirm, agentproto.AccessModeConfirm},
		{agentproto.AccessModeAcceptEdits, agentproto.AccessModeConfirm, agentproto.AccessModeConfirm},
		{agentproto.AccessModeConfirm, agentproto.AccessModeAcceptEdits, agentproto.AccessModeConfirm},
		{agentproto.AccessModeAcceptEdits, agentproto.AccessModeFullAccess, agentproto.AccessModeAcceptEdits},
		{"", agentproto.AccessModeConfirm, ""},
	}
	for _, tc := range cases {
		if got := clampAccessModeToMax(tc.mode, tc.max); got != tc.want {
			t.Fatalf("clampAccessModeToMax(%q, %q) = %q, want %q", tc.mode, tc.max, got, tc.want)
		}
	}
}

func TestGatewayPolicyClampsDefaultAndOverrideAccessMode(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"app-locked": {MaxAccessMode: agentproto.AccessModeAcceptEdits},
	})
	svc.UpsertInstance(policyTestInstance("inst-1", "/data/dl/droid", "thread-1", now.Add(-time.Minute)))
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionAttachInstance, SurfaceSessionID: "surface-1", GatewayID: "app-locked", ChatID: "chat-1", ActorUserID: "user-1", InstanceID: "inst-1"})

	snapshot := svc.SurfaceSnapshot("surface-1")
	if snapshot == nil {
		t.Fatal("expected surface snapshot")
	}
	if snapshot.NextPrompt.EffectiveAccessMode != agentproto.AccessModeAcceptEdits {
		t.Fatalf("expected default full_access clamped to accept_edits, got %#v", snapshot.NextPrompt)
	}

	svc.ApplySurfaceAction(control.Action{Kind: control.ActionAccessCommand, SurfaceSessionID: "surface-1", GatewayID: "app-locked", ChatID: "chat-1", ActorUserID: "user-1", Text: "/access full"})
	snapshot = svc.SurfaceSnapshot("surface-1")
	if snapshot.NextPrompt.EffectiveAccessMode != agentproto.AccessModeAcceptEdits {
		t.Fatalf("expected /access full clamped to accept_edits, got %#v", snapshot.NextPrompt)
	}
	if snapshot.NextPrompt.OverrideAccessMode != agentproto.AccessModeAcceptEdits {
		t.Fatalf("expected override access mode clamped to accept_edits, got %#v", snapshot.NextPrompt)
	}
	if snapshot.NextPrompt.EffectiveAccessModeSource != "surface_override" {
		t.Fatalf("expected clamped override to keep surface_override source, got %#v", snapshot.NextPrompt)
	}

	svc.ApplySurfaceAction(control.Action{Kind: control.ActionAccessCommand, SurfaceSessionID: "surface-1", GatewayID: "app-locked", ChatID: "chat-1", ActorUserID: "user-1", Text: "/access confirm"})
	snapshot = svc.SurfaceSnapshot("surface-1")
	if snapshot.NextPrompt.EffectiveAccessMode != agentproto.AccessModeConfirm {
		t.Fatalf("expected confirm (below max) to stay confirm, got %#v", snapshot.NextPrompt)
	}
}

func TestGatewayPolicyLeavesUnpolicedGatewayUntouched(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"app-locked": {MaxAccessMode: agentproto.AccessModeConfirm},
	})
	svc.UpsertInstance(policyTestInstance("inst-1", "/data/dl/droid", "thread-1", now.Add(-time.Minute)))
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionAttachInstance, SurfaceSessionID: "surface-free", GatewayID: "app-free", ChatID: "chat-1", ActorUserID: "user-1", InstanceID: "inst-1"})

	snapshot := svc.SurfaceSnapshot("surface-free")
	if snapshot == nil || snapshot.NextPrompt.EffectiveAccessMode != agentproto.AccessModeFullAccess {
		t.Fatalf("expected unpoliced gateway default full_access, got %#v", snapshot)
	}

	svc.ApplySurfaceAction(control.Action{Kind: control.ActionAccessCommand, SurfaceSessionID: "surface-free", GatewayID: "app-free", ChatID: "chat-1", ActorUserID: "user-1", Text: "/access full"})
	snapshot = svc.SurfaceSnapshot("surface-free")
	if snapshot.NextPrompt.EffectiveAccessMode != agentproto.AccessModeFullAccess {
		t.Fatalf("expected unpoliced gateway /access full to stay full_access, got %#v", snapshot.NextPrompt)
	}
}

func TestGatewayPolicyFiltersTargetPickerWorkspaceList(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"app-locked": {WorkspaceRoots: []string{"/data/allowed"}},
	})
	svc.UpsertInstance(policyTestInstance("inst-allowed", "/data/allowed/repo", "thread-a", now.Add(-time.Minute)))
	svc.UpsertInstance(policyTestInstance("inst-other", "/data/other/repo", "thread-b", now.Add(-2*time.Minute)))

	events := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionListInstances,
		SurfaceSessionID: "surface-locked",
		GatewayID:        "app-locked",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
	})
	view := singleTargetPickerEvent(t, events)
	if len(view.WorkspaceOptions) != 1 || !testutil.SamePath(view.WorkspaceOptions[0].Value, "/data/allowed/repo") {
		t.Fatalf("expected only allowed workspace in target picker, got %#v", view.WorkspaceOptions)
	}

	events = svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionListInstances,
		SurfaceSessionID: "surface-free",
		GatewayID:        "app-free",
		ChatID:           "chat-2",
		ActorUserID:      "user-2",
	})
	view = singleTargetPickerEvent(t, events)
	if len(view.WorkspaceOptions) != 2 {
		t.Fatalf("expected unpoliced gateway to keep both workspaces, got %#v", view.WorkspaceOptions)
	}
}

func TestGatewayPolicyDeniesWorkspaceOpenPaths(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"app-locked": {WorkspaceRoots: []string{"/data/allowed"}},
	})
	svc.UpsertInstance(policyTestInstance("inst-other", "/data/other/repo", "thread-b", now.Add(-time.Minute)))
	svc.UpsertInstance(policyTestInstance("inst-evil", "/data/allowed-evil", "thread-e", now.Add(-2*time.Minute)))

	// /attach 到 roots 外的工作区被拒。
	events := svc.ApplySurfaceAction(control.Action{Kind: control.ActionAttachWorkspace, SurfaceSessionID: "surface-1", GatewayID: "app-locked", ChatID: "chat-1", ActorUserID: "user-1", WorkspaceKey: "/data/other/repo"})
	if notice := firstNoticeEvent(events); notice == nil || notice.Code != workspacePolicyDeniedNoticeCode {
		t.Fatalf("expected workspace_policy_denied notice, got %#v", events)
	}

	// 前缀伪匹配（/data/allowed-evil 不在 /data/allowed 下）也被拒。
	events = svc.ApplySurfaceAction(control.Action{Kind: control.ActionAttachWorkspace, SurfaceSessionID: "surface-1", GatewayID: "app-locked", ChatID: "chat-1", ActorUserID: "user-1", WorkspaceKey: "/data/allowed-evil"})
	if notice := firstNoticeEvent(events); notice == nil || notice.Code != workspacePolicyDeniedNoticeCode {
		t.Fatalf("expected prefix pseudo-match to be denied, got %#v", events)
	}

	// /use 到 roots 外工作区的会话被拒。
	events = svc.ApplySurfaceAction(control.Action{Kind: control.ActionUseThread, SurfaceSessionID: "surface-1", GatewayID: "app-locked", ChatID: "chat-1", ActorUserID: "user-1", ThreadID: "thread-b", AllowCrossWorkspace: true})
	if notice := firstNoticeEvent(events); notice == nil || notice.Code != workspacePolicyDeniedNoticeCode {
		t.Fatalf("expected /use into denied workspace to be rejected, got %#v", events)
	}

	// headless 新工作区（目录接入 / path picker 确认后走的启动路径）被拒。
	surface := svc.root.Surfaces["surface-1"]
	if surface == nil {
		t.Fatal("expected surface record")
	}
	events = svc.startFreshWorkspaceHeadless(surface, "/data/other/newdir")
	if notice := firstNoticeEvent(events); notice == nil || notice.Code != workspacePolicyDeniedNoticeCode {
		t.Fatalf("expected fresh workspace launch outside roots to be denied, got %#v", events)
	}

	// roots 内目录不受策略拒绝（继续走正常流程）。
	events = svc.startFreshWorkspaceHeadless(surface, "/data/allowed/newdir")
	if notice := firstNoticeEvent(events); notice != nil && notice.Code == workspacePolicyDeniedNoticeCode {
		t.Fatalf("expected allowed workspace not to be policy-denied, got %#v", events)
	}
}

func TestGatewayPolicyAutoResumeOutsideRootsFails(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"app-locked": {WorkspaceRoots: []string{"/data/allowed"}},
	})
	svc.MaterializeSurface("surface-resume", "app-locked", "chat-1", "user-1")
	events, result := svc.TryAutoResumeHeadlessSurface("surface-resume", SurfaceResumeAttempt{
		ThreadID:       "thread-x",
		ThreadCWD:      "/data/other/repo",
		WorkspaceKey:   "/data/other/repo",
		ResumeHeadless: true,
	}, true)
	if result.Status != SurfaceResumeStatusFailed || result.FailureCode != "workspace_policy_denied" {
		t.Fatalf("expected policy-denied resume failure, got %#v", result)
	}
	// headless 恢复分支要求早退门自带失败 notice（daemon tick 不会为
	// ResumeHeadless 条目补发 surface-resume 族通知）。
	notice := firstNoticeEvent(events)
	if notice == nil || notice.Code != "headless_restore_workspace_policy_denied" {
		t.Fatalf("expected policy-denied restore notice, got %#v", events)
	}
}

// approverPolicyRequestTestService 搭一个"远端 surface 发起的 turn"现场：
// actorUserID 通过普通文本消息发起 turn-1（发起者身份被冻结在 queue item 上），
// 随后的审批请求按 turn 发起者判定审批人语义。
func approverPolicyRequestTestService(t *testing.T, now *time.Time, actorUserID string) *Service {
	t.Helper()
	svc := newServiceForTest(now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"app-locked": {ApproverOpenID: "admin-1"},
	})
	svc.MaterializeSurface("feishu:app-locked:user:admin-1", "app-locked", "chat-admin", "admin-1")
	svc.UpsertInstance(policyTestInstance("inst-1", "/data/dl/droid", "thread-1", now.Add(-time.Minute)))
	base := control.Action{SurfaceSessionID: "surface-1", GatewayID: "app-locked", ChatID: "chat-1", ActorUserID: actorUserID}
	attach := base
	attach.Kind = control.ActionAttachInstance
	attach.InstanceID = "inst-1"
	svc.ApplySurfaceAction(attach)
	use := base
	use.Kind = control.ActionUseThread
	use.ThreadID = "thread-1"
	svc.ApplySurfaceAction(use)
	text := base
	text.Kind = control.ActionTextMessage
	text.MessageID = "om-turn-source"
	text.Text = "执行任务"
	if events := svc.ApplySurfaceAction(text); len(events) == 0 {
		t.Fatal("expected text dispatch events")
	}
	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventTurnStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		Initiator: agentproto.Initiator{Kind: agentproto.InitiatorUnknown},
	})
	return svc
}

func approvalRequestStartedEvent(requestID string) agentproto.Event {
	return agentproto.Event{
		Kind:      agentproto.EventRequestStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		RequestID: requestID,
		Metadata: map[string]any{
			"requestType": "approval",
			"command":     "rm -rf /tmp/x",
		},
	}
}

func TestApproverPolicyAutoDeclinesNonApproverRequests(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	svc := approverPolicyRequestTestService(t, &now, "user-2")

	// 群聊场景回归：审批人随后在同一聊天里发过言（surface.ActorUserID 被覆写成
	// admin-1），判定必须仍按 turn 发起者 user-2 走自动拒绝，不能被绕过。
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionStatus, SurfaceSessionID: "surface-1", GatewayID: "app-locked", ChatID: "chat-1", ActorUserID: "admin-1"})

	events := svc.ApplyAgentEvent("inst-1", approvalRequestStartedEvent("req-1"))
	if len(events) != 3 {
		t.Fatalf("expected requester notice + decline command + approver notice, got %#v", events)
	}
	requesterNotice := events[0]
	if requesterNotice.Kind != eventcontract.KindNotice || requesterNotice.Notice == nil ||
		requesterNotice.Notice.Code != "request_auto_declined_by_approver_policy" ||
		requesterNotice.SurfaceSessionID != "surface-1" {
		t.Fatalf("expected requester auto-decline notice, got %#v", requesterNotice)
	}
	if !strings.Contains(requesterNotice.Notice.Text, "已自动拒绝") {
		t.Fatalf("unexpected requester notice text: %q", requesterNotice.Notice.Text)
	}
	command := events[1]
	if command.Command == nil || command.Command.Kind != agentproto.CommandRequestRespond {
		t.Fatalf("expected request respond command, got %#v", command)
	}
	if command.Command.Request.RequestID != "req-1" {
		t.Fatalf("expected decline for req-1, got %#v", command.Command.Request)
	}
	if decision, _ := command.Command.Request.Response["decision"].(string); decision != "decline" {
		t.Fatalf("expected decline decision, got %#v", command.Command.Request.Response)
	}
	if command.Command.Request.InterruptOnDecline {
		t.Fatalf("expected auto-decline not to interrupt the turn, got %#v", command.Command.Request)
	}
	approverNotice := events[2]
	if approverNotice.Kind != eventcontract.KindNotice || approverNotice.Notice == nil ||
		approverNotice.SurfaceSessionID != "feishu:app-locked:user:admin-1" ||
		approverNotice.Notice.Code != "approver_policy_auto_declined_report" {
		t.Fatalf("expected approver report notice, got %#v", approverNotice)
	}
	if !strings.Contains(approverNotice.Notice.Text, "user-2") || !strings.Contains(approverNotice.Notice.Text, "/data/dl/droid") {
		t.Fatalf("expected approver notice to name actor and workspace, got %q", approverNotice.Notice.Text)
	}
	// 自动 decline 走与手动 decline 相同的 pending 生命周期：
	// 记录保留在 submitting 状态，派发失败可按 commandID 回滚。
	surface := svc.root.Surfaces["surface-1"]
	request := surface.PendingRequests["req-1"]
	if request == nil || strings.TrimSpace(request.PendingDispatchCommandID) == "" {
		t.Fatalf("expected auto-declined request tracked in submitting lifecycle, got %#v", surface.PendingRequests)
	}
	if request.PendingDispatchCommandID != command.Command.CommandID {
		t.Fatalf("expected pending dispatch command id %q to match dispatched command %q", request.PendingDispatchCommandID, command.Command.CommandID)
	}
}

func TestApproverPolicyLocalTurnNotIntercepted(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"app-locked": {ApproverOpenID: "admin-1"},
	})
	svc.UpsertInstance(policyTestInstance("inst-1", "/data/dl/droid", "thread-1", now.Add(-time.Minute)))
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionAttachInstance, SurfaceSessionID: "surface-1", GatewayID: "app-locked", ChatID: "chat-1", ActorUserID: "user-2", InstanceID: "inst-1"})
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionUseThread, SurfaceSessionID: "surface-1", GatewayID: "app-locked", ChatID: "chat-1", ActorUserID: "user-2", ThreadID: "thread-1"})
	// 本地（VS Code）发起的 turn：审批必须照常呈现，relay 不得抢先 decline。
	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventTurnStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		Initiator: agentproto.Initiator{Kind: agentproto.InitiatorLocalUI},
	})

	events := svc.ApplyAgentEvent("inst-1", approvalRequestStartedEvent("req-local"))
	prompt := singleRequestPromptEvent(t, events)
	if prompt.RequestID != "req-local" {
		t.Fatalf("expected local-initiated approval to keep normal card, got %#v", prompt)
	}
}

func TestApproverPolicyResponderEnforcement(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	svc := approverPolicyRequestTestService(t, &now, "admin-1")
	events := svc.ApplyAgentEvent("inst-1", approvalRequestStartedEvent("req-resp"))
	if prompt := singleRequestPromptEvent(t, events); prompt.RequestID != "req-resp" {
		t.Fatalf("expected approval card for approver-initiated turn, got %#v", prompt)
	}

	// 非审批人点击"允许"：响应被兜底强制点拒绝。
	blocked := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionRespondRequest,
		SurfaceSessionID: "surface-1",
		GatewayID:        "app-locked",
		ChatID:           "chat-1",
		ActorUserID:      "user-3",
		MessageID:        "om-click-1",
		Request:          testRequestAction("req-resp", "approval", "accept", nil, 0),
	})
	if notice := firstNoticeEvent(blocked); notice == nil || notice.Code != "request_approver_required" {
		t.Fatalf("expected non-approver response to be blocked, got %#v", blocked)
	}
	if request := svc.root.Surfaces["surface-1"].PendingRequests["req-resp"]; request == nil || strings.TrimSpace(request.PendingDispatchCommandID) != "" {
		t.Fatalf("expected request untouched after blocked response, got %#v", request)
	}

	// 审批人本人点击：正常派发响应。
	allowed := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionRespondRequest,
		SurfaceSessionID: "surface-1",
		GatewayID:        "app-locked",
		ChatID:           "chat-1",
		ActorUserID:      "admin-1",
		MessageID:        "om-click-2",
		Request:          testRequestAction("req-resp", "approval", "accept", nil, 0),
	})
	foundCommand := false
	for _, event := range allowed {
		if event.Command != nil && event.Command.Kind == agentproto.CommandRequestRespond {
			foundCommand = true
		}
	}
	if !foundCommand {
		t.Fatalf("expected approver response to dispatch, got %#v", allowed)
	}
}

func TestGatewayPolicyVSCodeEmptyOverrideClampedToMax(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"app-locked": {MaxAccessMode: agentproto.AccessModeAcceptEdits},
	})
	svc.MaterializeSurface("surface-vscode", "app-locked", "chat-1", "user-1")
	surface := svc.root.Surfaces["surface-vscode"]
	surface.ProductMode = state.ProductModeVSCode

	frozen := svc.resolveFrozenPromptOverride(nil, surface, "", "", state.ModelConfigRecord{})
	if frozen.AccessMode != agentproto.AccessModeAcceptEdits {
		t.Fatalf("expected empty vscode override to be pinned to policy max, got %#v", frozen)
	}

	frozen = svc.resolveFrozenPromptOverride(nil, surface, "", "", state.ModelConfigRecord{AccessMode: agentproto.AccessModeFullAccess})
	if frozen.AccessMode != agentproto.AccessModeAcceptEdits {
		t.Fatalf("expected vscode full_access override clamped, got %#v", frozen)
	}

	frozen = svc.resolveFrozenPromptOverride(nil, surface, "", "", state.ModelConfigRecord{AccessMode: agentproto.AccessModeConfirm})
	if frozen.AccessMode != agentproto.AccessModeConfirm {
		t.Fatalf("expected vscode confirm override untouched, got %#v", frozen)
	}
}

func TestFindGatewayUserSurfaceNeverFallsBackToChatSurface(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	// 群聊 surface 的 ActorUserID 恰好是审批人（刚发过言）——不允许被选中。
	svc.MaterializeSurface("feishu:app-locked:chat:oc_group", "app-locked", "oc_group", "admin-1")
	if got := svc.findGatewayUserSurface("app-locked", "admin-1"); got != nil {
		t.Fatalf("expected chat-scope surface to be excluded, got %#v", got.SurfaceSessionID)
	}
	// tab 变体可作为 fallback。
	svc.MaterializeSurface("feishu:app-locked:user:admin-1#tab2", "app-locked", "", "admin-1")
	if got := svc.findGatewayUserSurface("app-locked", "admin-1"); got == nil || got.SurfaceSessionID != "feishu:app-locked:user:admin-1#tab2" {
		t.Fatalf("expected tab variant fallback, got %#v", got)
	}
	// base 单聊 surface 优先。
	svc.MaterializeSurface("feishu:app-locked:user:admin-1", "app-locked", "", "admin-1")
	if got := svc.findGatewayUserSurface("app-locked", "admin-1"); got == nil || got.SurfaceSessionID != "feishu:app-locked:user:admin-1" {
		t.Fatalf("expected exact p2p surface preferred, got %#v", got)
	}
}

func TestApproverPolicyApproverOwnSurfaceKeepsNormalFlow(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	svc := approverPolicyRequestTestService(t, &now, "admin-1")

	events := svc.ApplyAgentEvent("inst-1", approvalRequestStartedEvent("req-2"))
	prompt := singleRequestPromptEvent(t, events)
	if prompt.RequestID != "req-2" {
		t.Fatalf("expected normal approval card for approver, got %#v", prompt)
	}
}

func TestApproverPolicyAbsentKeepsNormalFlow(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.UpsertInstance(policyTestInstance("inst-1", "/data/dl/droid", "thread-1", now.Add(-time.Minute)))
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionAttachInstance, SurfaceSessionID: "surface-1", GatewayID: "app-free", ChatID: "chat-1", ActorUserID: "user-2", InstanceID: "inst-1"})
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionUseThread, SurfaceSessionID: "surface-1", GatewayID: "app-free", ChatID: "chat-1", ActorUserID: "user-2", ThreadID: "thread-1"})
	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventTurnStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		Initiator: agentproto.Initiator{Kind: agentproto.InitiatorLocalUI},
	})

	events := svc.ApplyAgentEvent("inst-1", approvalRequestStartedEvent("req-3"))
	prompt := singleRequestPromptEvent(t, events)
	if prompt.RequestID != "req-3" {
		t.Fatalf("expected normal approval card without approver policy, got %#v", prompt)
	}
}
