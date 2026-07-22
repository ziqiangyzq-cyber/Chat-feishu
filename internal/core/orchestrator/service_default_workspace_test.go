package orchestrator

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func defaultWorkspaceTestInstance(instanceID, workspaceKey string) *state.InstanceRecord {
	return &state.InstanceRecord{
		InstanceID:    instanceID,
		DisplayName:   state.WorkspaceShortName(workspaceKey),
		WorkspaceRoot: workspaceKey,
		WorkspaceKey:  workspaceKey,
		ShortName:     state.WorkspaceShortName(workspaceKey),
		Source:        "headless",
		Managed:       true,
		Online:        true,
		Threads:       map[string]*state.ThreadRecord{},
	}
}

func defaultWorkspacePromptSend(events []eventcontract.Event) *agentproto.Command {
	for _, event := range events {
		if event.Command != nil && event.Command.Kind == agentproto.CommandPromptSend {
			return event.Command
		}
	}
	return nil
}

func defaultWorkspaceStartHeadless(events []eventcontract.Event) *control.DaemonCommand {
	for _, event := range events {
		if event.DaemonCommand != nil && event.DaemonCommand.Kind == control.DaemonCommandStartHeadless {
			return event.DaemonCommand
		}
	}
	return nil
}

func defaultWorkspaceKillHeadless(events []eventcontract.Event) *control.DaemonCommand {
	for _, event := range events {
		if event.DaemonCommand != nil && event.DaemonCommand.Kind == control.DaemonCommandKillHeadless {
			return event.DaemonCommand
		}
	}
	return nil
}

func TestDefaultWorkspaceFirstTextAutoAttachesAndStartsNewThread(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"efc-site": {
			WorkspaceRoots:       []string{"/data/efc-site"},
			DefaultWorkspaceRoot: "/data/efc-site",
		},
	})
	svc.UpsertInstance(defaultWorkspaceTestInstance("inst-site-user-1", "/data/efc-site"))

	events := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-1",
		ChatID:           "oc_user_1",
		ActorUserID:      "user-1",
		MessageID:        "om-user-1-first",
		Text:             "检查今天的现场记录",
	})

	surface := svc.root.Surfaces["feishu:efc-site:user:user-1"]
	if surface == nil || surface.AttachedInstanceID != "inst-site-user-1" {
		t.Fatalf("expected first text to attach the default workspace instance, got %#v", surface)
	}
	if surface.ClaimedWorkspaceKey != "/data/efc-site" {
		t.Fatalf("expected default workspace claim to persist, got %#v", surface)
	}
	prompt := defaultWorkspacePromptSend(events)
	if prompt == nil || !prompt.Target.CreateThreadIfMissing || prompt.Target.CWD != "/data/efc-site" {
		t.Fatalf("expected first text to start a new thread in the default workspace, got %#v", events)
	}
	if notice := firstNoticeEvent(events); notice != nil && notice.Code == "not_attached" {
		t.Fatalf("expected automatic default workspace bootstrap, got %#v", events)
	}
}

func TestDefaultWorkspaceDoesNotBootstrapAgainAfterFirstThreadStarts(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 5, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"efc-site": {
			WorkspaceRoots:       []string{"/data/efc-site"},
			DefaultWorkspaceRoot: "/data/efc-site",
		},
	})
	svc.UpsertInstance(defaultWorkspaceTestInstance("inst-site-user-1", "/data/efc-site"))
	surfaceID := "feishu:efc-site:user:user-1"

	svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: surfaceID,
		ActorUserID:      "user-1",
		MessageID:        "om-user-1-first",
		Text:             "第一条任务",
	})
	svc.ApplyAgentEvent("inst-site-user-1", agentproto.Event{
		Kind:      agentproto.EventTurnStarted,
		ThreadID:  "thread-user-1",
		TurnID:    "turn-user-1",
		Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: surfaceID},
	})
	svc.ApplyAgentEvent("inst-site-user-1", agentproto.Event{
		Kind:     agentproto.EventTurnCompleted,
		ThreadID: "thread-user-1",
		TurnID:   "turn-user-1",
	})

	events := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: surfaceID,
		ActorUserID:      "user-1",
		MessageID:        "om-user-1-second",
		Text:             "第二条任务",
	})

	if defaultWorkspaceStartHeadless(events) != nil {
		t.Fatalf("expected an attached surface not to bootstrap again, got %#v", events)
	}
	prompt := defaultWorkspacePromptSend(events)
	if prompt == nil || prompt.Target.ThreadID != "thread-user-1" || prompt.Target.CreateThreadIfMissing {
		t.Fatalf("expected the second text to stay on the user's existing thread, got %#v", events)
	}
	surface := svc.root.Surfaces[surfaceID]
	if surface.AttachedInstanceID != "inst-site-user-1" || surface.ClaimedWorkspaceKey != "/data/efc-site" {
		t.Fatalf("expected the original instance and workspace claim to remain stable, got %#v", surface)
	}
}

func TestDefaultWorkspaceKeepsUsersOnDistinctInstancesAndThreads(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 10, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"efc-site": {
			WorkspaceRoots:                   []string{"/data/efc-site"},
			DefaultWorkspaceRoot:             "/data/efc-site",
			AllowConcurrentWorkspaceSurfaces: true,
		},
	})
	svc.UpsertInstance(defaultWorkspaceTestInstance("inst-site-user-1", "/data/efc-site"))

	firstEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-1",
		ChatID:           "oc_user_1",
		ActorUserID:      "user-1",
		MessageID:        "om-user-1-first",
		Text:             "用户一的任务",
	})
	if defaultWorkspacePromptSend(firstEvents) == nil {
		t.Fatalf("expected user 1 prompt dispatch, got %#v", firstEvents)
	}
	svc.ApplyAgentEvent("inst-site-user-1", agentproto.Event{
		Kind:      agentproto.EventTurnStarted,
		ThreadID:  "thread-user-1",
		TurnID:    "turn-user-1",
		Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: "feishu:efc-site:user:user-1"},
	})

	secondEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-2",
		ChatID:           "oc_user_2",
		ActorUserID:      "user-2",
		MessageID:        "om-user-2-first",
		Text:             "用户二的任务",
	})
	start := defaultWorkspaceStartHeadless(secondEvents)
	if start == nil || start.WorkspaceKey != "/data/efc-site" {
		t.Fatalf("expected user 2 to start an independent headless instance, got %#v", secondEvents)
	}
	second := svc.root.Surfaces["feishu:efc-site:user:user-2"]
	if second == nil || second.PendingHeadless == nil || second.PendingHeadless.InstanceID != start.InstanceID {
		t.Fatalf("expected user 2 pending headless launch, got %#v", second)
	}
	if second.AttachedInstanceID != "" || second.ClaimedWorkspaceKey != "/data/efc-site" {
		t.Fatalf("expected user 2 to claim only the shared directory while launch is pending, got %#v", second)
	}
	if len(second.QueuedQueueItemIDs) != 1 {
		t.Fatalf("expected user 2 first text to remain queued through startup, got %#v", second)
	}

	svc.UpsertInstance(defaultWorkspaceTestInstance(start.InstanceID, "/data/efc-site"))
	connectEvents := svc.ApplyInstanceConnected(start.InstanceID)
	prompt := defaultWorkspacePromptSend(connectEvents)
	if prompt == nil || prompt.Origin.UserID != "user-2" || prompt.Origin.MessageID != "om-user-2-first" {
		t.Fatalf("expected queued user 2 text to dispatch after its instance connects, got %#v", connectEvents)
	}
	svc.ApplyAgentEvent(start.InstanceID, agentproto.Event{
		Kind:      agentproto.EventTurnStarted,
		ThreadID:  "thread-user-2",
		TurnID:    "turn-user-2",
		Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: "feishu:efc-site:user:user-2"},
	})
	svc.ApplyAgentEvent("inst-site-user-1", agentproto.Event{
		Kind:     agentproto.EventTurnCompleted,
		ThreadID: "thread-user-1",
		TurnID:   "turn-user-1",
	})
	svc.ApplyAgentEvent(start.InstanceID, agentproto.Event{
		Kind:     agentproto.EventTurnCompleted,
		ThreadID: "thread-user-2",
		TurnID:   "turn-user-2",
	})

	first := svc.root.Surfaces["feishu:efc-site:user:user-1"]
	second = svc.root.Surfaces["feishu:efc-site:user:user-2"]
	if first.AttachedInstanceID == second.AttachedInstanceID {
		t.Fatalf("expected distinct instances, got first=%#v second=%#v", first, second)
	}
	if first.SelectedThreadID != "thread-user-1" || second.SelectedThreadID != "thread-user-2" {
		t.Fatalf("expected distinct thread bindings, got first=%#v second=%#v", first, second)
	}
	if first.ActorUserID == second.ActorUserID {
		t.Fatalf("expected distinct user surfaces, got first=%#v second=%#v", first, second)
	}
}

func TestWorkspaceRootsWithoutDefaultKeepDetachedTextBehavior(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 20, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"app-no-default": {WorkspaceRoots: []string{"/data/one", "/data/two"}},
	})
	svc.UpsertInstance(defaultWorkspaceTestInstance("inst-one", "/data/one"))
	svc.UpsertInstance(defaultWorkspaceTestInstance("inst-two", "/data/two"))

	events := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "app-no-default",
		SurfaceSessionID: "feishu:app-no-default:user:user-1",
		ActorUserID:      "user-1",
		MessageID:        "om-no-default",
		Text:             "保持旧行为",
	})

	notice := firstNoticeEvent(events)
	if notice == nil || notice.Code != "not_attached" || !strings.Contains(notice.Text, "/list") {
		t.Fatalf("expected detached text to keep the explicit selection requirement, got %#v", events)
	}
}

func TestDefaultWorkspaceWithoutConcurrencyRejectsSecondSurface(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 25, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"efc-site": {
			WorkspaceRoots:       []string{"/data/efc-site"},
			DefaultWorkspaceRoot: "/data/efc-site",
		},
	})
	svc.UpsertInstance(defaultWorkspaceTestInstance("inst-site-user-1", "/data/efc-site"))
	svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-1",
		ActorUserID:      "user-1",
		MessageID:        "om-user-1-first",
		Text:             "用户一的任务",
	})

	events := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-2",
		ActorUserID:      "user-2",
		MessageID:        "om-user-2-first",
		Text:             "用户二的任务",
	})
	notice := firstNoticeEvent(events)
	if notice == nil || notice.Code != "workspace_busy" {
		t.Fatalf("expected concurrency-disabled default workspace to remain exclusive, got %#v", events)
	}
	if defaultWorkspaceStartHeadless(events) != nil {
		t.Fatalf("expected no second headless launch without explicit concurrency policy, got %#v", events)
	}
}

func TestDefaultWorkspaceLaunchFailureReleasesClaimAndQueuedInput(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 27, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"efc-site": {
			WorkspaceRoots:                   []string{"/data/efc-site"},
			DefaultWorkspaceRoot:             "/data/efc-site",
			AllowConcurrentWorkspaceSurfaces: true,
		},
	})
	svc.UpsertInstance(defaultWorkspaceTestInstance("inst-site-user-1", "/data/efc-site"))
	svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-1",
		ActorUserID:      "user-1",
		MessageID:        "om-user-1-first",
		Text:             "用户一的任务",
	})
	startEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-2",
		ActorUserID:      "user-2",
		MessageID:        "om-user-2-first",
		Text:             "用户二的任务",
	})
	start := defaultWorkspaceStartHeadless(startEvents)
	if start == nil {
		t.Fatalf("expected pending default workspace launch, got %#v", startEvents)
	}
	surface := svc.root.Surfaces["feishu:efc-site:user:user-2"]
	queueID := surface.QueuedQueueItemIDs[0]

	failureEvents := svc.HandleHeadlessLaunchFailed(surface.SurfaceSessionID, start.InstanceID, errors.New("start failed"))
	if surface.PendingHeadless != nil || surface.ClaimedWorkspaceKey != "" {
		t.Fatalf("expected failed bootstrap to release pending launch and workspace claim, got %#v", surface)
	}
	if len(surface.QueuedQueueItemIDs) != 0 || surface.QueueItems[queueID].Status != state.QueueItemDiscarded {
		t.Fatalf("expected failed bootstrap to terminate the queued first input, got %#v", surface)
	}
	if notice := firstNoticeEvent(failureEvents); notice == nil || notice.Code != "workspace_create_start_failed" {
		t.Fatalf("expected launch failure notice after queue cleanup, got %#v", failureEvents)
	}
}

func TestDefaultWorkspaceLaunchCancelReleasesClaimAndQueuedInput(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 27, 30, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"efc-site": {
			WorkspaceRoots:                   []string{"/data/efc-site"},
			DefaultWorkspaceRoot:             "/data/efc-site",
			AllowConcurrentWorkspaceSurfaces: true,
		},
	})
	svc.UpsertInstance(defaultWorkspaceTestInstance("inst-site-user-1", "/data/efc-site"))
	svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-1",
		ActorUserID:      "user-1",
		MessageID:        "om-user-1-first",
		Text:             "用户一的任务",
	})
	startEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-2",
		ActorUserID:      "user-2",
		MessageID:        "om-user-2-first",
		Text:             "用户二的任务",
	})
	start := defaultWorkspaceStartHeadless(startEvents)
	if start == nil {
		t.Fatalf("expected pending default workspace launch, got %#v", startEvents)
	}
	surface := svc.root.Surfaces["feishu:efc-site:user:user-2"]
	queueID := surface.QueuedQueueItemIDs[0]

	cancelEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionDetach,
		GatewayID:        "efc-site",
		SurfaceSessionID: surface.SurfaceSessionID,
		ActorUserID:      "user-2",
	})
	if surface.PendingHeadless != nil || surface.ClaimedWorkspaceKey != "" {
		t.Fatalf("expected cancelled bootstrap to release pending launch and workspace claim, got %#v", surface)
	}
	if len(surface.QueuedQueueItemIDs) != 0 || surface.QueueItems[queueID].Status != state.QueueItemDiscarded {
		t.Fatalf("expected cancelled bootstrap to terminate the queued first input, got %#v", surface)
	}
	if notice := firstNoticeEvent(cancelEvents); notice == nil || notice.Code != "detached" {
		t.Fatalf("expected detach notice after bootstrap cancellation, got %#v", cancelEvents)
	}
	if kill := defaultWorkspaceKillHeadless(cancelEvents); kill == nil || kill.InstanceID != start.InstanceID {
		t.Fatalf("expected bootstrap cancellation to stop the pending instance, got %#v", cancelEvents)
	}
}

func TestDefaultWorkspaceLaunchTimeoutReleasesClaimAndQueuedInput(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 28, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"efc-site": {
			WorkspaceRoots:                   []string{"/data/efc-site"},
			DefaultWorkspaceRoot:             "/data/efc-site",
			AllowConcurrentWorkspaceSurfaces: true,
		},
	})
	svc.UpsertInstance(defaultWorkspaceTestInstance("inst-site-user-1", "/data/efc-site"))
	svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-1",
		ActorUserID:      "user-1",
		MessageID:        "om-user-1-first",
		Text:             "用户一的任务",
	})
	startEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-2",
		ActorUserID:      "user-2",
		MessageID:        "om-user-2-first",
		Text:             "用户二的任务",
	})
	start := defaultWorkspaceStartHeadless(startEvents)
	if start == nil {
		t.Fatalf("expected pending default workspace launch, got %#v", startEvents)
	}
	surface := svc.root.Surfaces["feishu:efc-site:user:user-2"]
	queueID := surface.QueuedQueueItemIDs[0]

	timeoutEvents := svc.Tick(surface.PendingHeadless.ExpiresAt)
	if surface.PendingHeadless != nil || surface.ClaimedWorkspaceKey != "" {
		t.Fatalf("expected timed-out bootstrap to release pending launch and workspace claim, got %#v", surface)
	}
	if len(surface.QueuedQueueItemIDs) != 0 || surface.QueueItems[queueID].Status != state.QueueItemDiscarded {
		t.Fatalf("expected timed-out bootstrap to terminate the queued first input, got %#v", surface)
	}
	if notice := firstNoticeEvent(timeoutEvents); notice == nil || notice.Code != "workspace_create_start_timeout" {
		t.Fatalf("expected workspace timeout notice after queue cleanup, got %#v", timeoutEvents)
	}
}

func TestDefaultWorkspaceLaunchDisconnectReleasesClaimAndQueuedInput(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 29, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"efc-site": {
			WorkspaceRoots:                   []string{"/data/efc-site"},
			DefaultWorkspaceRoot:             "/data/efc-site",
			AllowConcurrentWorkspaceSurfaces: true,
		},
	})
	svc.UpsertInstance(defaultWorkspaceTestInstance("inst-site-user-1", "/data/efc-site"))
	svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-1",
		ActorUserID:      "user-1",
		MessageID:        "om-user-1-first",
		Text:             "用户一的任务",
	})
	startEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-2",
		ActorUserID:      "user-2",
		MessageID:        "om-user-2-first",
		Text:             "用户二的任务",
	})
	start := defaultWorkspaceStartHeadless(startEvents)
	if start == nil {
		t.Fatalf("expected pending default workspace launch, got %#v", startEvents)
	}
	surface := svc.root.Surfaces["feishu:efc-site:user:user-2"]
	queueID := surface.QueuedQueueItemIDs[0]
	svc.UpsertInstance(defaultWorkspaceTestInstance(start.InstanceID, "/data/efc-site"))

	disconnectEvents := svc.ApplyInstanceDisconnected(start.InstanceID)
	if surface.PendingHeadless != nil || surface.ClaimedWorkspaceKey != "" {
		t.Fatalf("expected interrupted bootstrap to release pending launch and workspace claim, got %#v", surface)
	}
	if len(surface.QueuedQueueItemIDs) != 0 || surface.QueueItems[queueID].Status != state.QueueItemDiscarded {
		t.Fatalf("expected interrupted bootstrap to terminate the queued first input, got %#v", surface)
	}
	if notice := firstNoticeEvent(disconnectEvents); notice == nil || notice.Code != "workspace_create_start_failed" {
		t.Fatalf("expected workspace failure notice after disconnect cleanup, got %#v", disconnectEvents)
	}
}

func TestDefaultWorkspaceAutoAttachesImageAndFileInputs(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 30, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"efc-site": {
			WorkspaceRoots:       []string{"/data/efc-site"},
			DefaultWorkspaceRoot: "/data/efc-site",
		},
	})
	svc.UpsertInstance(defaultWorkspaceTestInstance("inst-site-user-1", "/data/efc-site"))

	imageEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionImageMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-1",
		ActorUserID:      "user-1",
		MessageID:        "om-image",
		LocalPath:        "/tmp/site.jpg",
		MIMEType:         "image/jpeg",
	})
	if len(imageEvents) == 0 || imageEvents[len(imageEvents)-1].PendingInput == nil {
		t.Fatalf("expected detached image to auto-attach and stage, got %#v", imageEvents)
	}
	fileEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionFileMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-1",
		ActorUserID:      "user-1",
		MessageID:        "om-file",
		LocalPath:        "/tmp/site.pdf",
		FileName:         "site.pdf",
	})
	if len(fileEvents) == 0 || fileEvents[len(fileEvents)-1].PendingInput == nil {
		t.Fatalf("expected detached file to auto-attach and stage, got %#v", fileEvents)
	}
}

func TestDefaultWorkspaceDoesNotReuseIdleAttachedInstance(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 35, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"efc-site": {
			WorkspaceRoots:                   []string{"/data/efc-site"},
			DefaultWorkspaceRoot:             "/data/efc-site",
			AllowConcurrentWorkspaceSurfaces: true,
		},
	})
	svc.UpsertInstance(defaultWorkspaceTestInstance("inst-site-user-1", "/data/efc-site"))
	firstSurfaceID := "feishu:efc-site:user:user-1"
	svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: firstSurfaceID,
		ActorUserID:      "user-1",
		MessageID:        "om-user-1-first",
		Text:             "用户一的任务",
	})
	svc.ApplyAgentEvent("inst-site-user-1", agentproto.Event{
		Kind:      agentproto.EventTurnStarted,
		ThreadID:  "thread-user-1",
		TurnID:    "turn-user-1",
		Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: firstSurfaceID},
	})
	svc.ApplyAgentEvent("inst-site-user-1", agentproto.Event{
		Kind:     agentproto.EventTurnCompleted,
		ThreadID: "thread-user-1",
		TurnID:   "turn-user-1",
	})

	secondEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-2",
		ActorUserID:      "user-2",
		MessageID:        "om-user-2-first",
		Text:             "用户二的任务",
	})
	start := defaultWorkspaceStartHeadless(secondEvents)
	if start == nil || start.InstanceID == "inst-site-user-1" {
		t.Fatalf("expected idle user-1 instance to remain exclusive, got %#v", secondEvents)
	}
	first := svc.root.Surfaces[firstSurfaceID]
	second := svc.root.Surfaces["feishu:efc-site:user:user-2"]
	if first.AttachedInstanceID != "inst-site-user-1" || second.AttachedInstanceID != "" || second.PendingHeadless == nil {
		t.Fatalf("expected user 2 to wait for an independent instance, got first=%#v second=%#v", first, second)
	}
}

func TestDefaultWorkspaceConcurrentPendingLaunchesStayPerSurface(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 40, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"efc-site": {
			WorkspaceRoots:                   []string{"/data/efc-site"},
			DefaultWorkspaceRoot:             "/data/efc-site",
			AllowConcurrentWorkspaceSurfaces: true,
		},
	})

	firstEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-1",
		ActorUserID:      "user-1",
		MessageID:        "om-user-1-first",
		Text:             "用户一的任务",
	})
	secondEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-2",
		ActorUserID:      "user-2",
		MessageID:        "om-user-2-first",
		Text:             "用户二的任务",
	})
	firstStart := defaultWorkspaceStartHeadless(firstEvents)
	secondStart := defaultWorkspaceStartHeadless(secondEvents)
	if firstStart == nil || secondStart == nil || firstStart.InstanceID == secondStart.InstanceID {
		t.Fatalf("expected distinct pending instances, got first=%#v second=%#v", firstEvents, secondEvents)
	}

	svc.UpsertInstance(defaultWorkspaceTestInstance(secondStart.InstanceID, "/data/efc-site"))
	secondConnectEvents := svc.ApplyInstanceConnected(secondStart.InstanceID)
	secondPrompt := defaultWorkspacePromptSend(secondConnectEvents)
	if secondPrompt == nil || secondPrompt.Origin.UserID != "user-2" || secondPrompt.Origin.MessageID != "om-user-2-first" {
		t.Fatalf("expected the second launch to dispatch only user 2 input, got %#v", secondConnectEvents)
	}
	first := svc.root.Surfaces["feishu:efc-site:user:user-1"]
	if first.PendingHeadless == nil || first.PendingHeadless.InstanceID != firstStart.InstanceID || len(first.QueuedQueueItemIDs) != 1 {
		t.Fatalf("expected user 1 launch and queue to remain untouched, got %#v", first)
	}

	svc.UpsertInstance(defaultWorkspaceTestInstance(firstStart.InstanceID, "/data/efc-site"))
	firstConnectEvents := svc.ApplyInstanceConnected(firstStart.InstanceID)
	firstPrompt := defaultWorkspacePromptSend(firstConnectEvents)
	if firstPrompt == nil || firstPrompt.Origin.UserID != "user-1" || firstPrompt.Origin.MessageID != "om-user-1-first" {
		t.Fatalf("expected the first launch to dispatch only user 1 input, got %#v", firstConnectEvents)
	}
}

func TestDefaultWorkspacePendingMediaStaysOnOriginSurface(t *testing.T) {
	tests := []struct {
		name        string
		media       control.Action
		assertInput func(*testing.T, []agentproto.Input)
	}{
		{
			name: "image",
			media: control.Action{
				Kind:      control.ActionImageMessage,
				MessageID: "om-user-2-image",
				LocalPath: "/tmp/user-2-site.jpg",
				MIMEType:  "image/jpeg",
			},
			assertInput: func(t *testing.T, inputs []agentproto.Input) {
				t.Helper()
				if len(inputs) != 2 || inputs[0].Type != agentproto.InputLocalImage || inputs[0].Path != "/tmp/user-2-site.jpg" || inputs[1].Text != "结合附件处理" {
					t.Fatalf("unexpected image prompt inputs: %#v", inputs)
				}
			},
		},
		{
			name: "file",
			media: control.Action{
				Kind:      control.ActionFileMessage,
				MessageID: "om-user-2-file",
				LocalPath: "/tmp/user-2-site.pdf",
				FileName:  "user-2-site.pdf",
			},
			assertInput: func(t *testing.T, inputs []agentproto.Input) {
				t.Helper()
				if len(inputs) != 2 || inputs[0].Type != agentproto.InputText || !strings.Contains(inputs[0].Text, "/tmp/user-2-site.pdf") || inputs[1].Text != "结合附件处理" {
					t.Fatalf("unexpected file prompt inputs: %#v", inputs)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 7, 23, 9, 45, 0, 0, time.UTC)
			svc := newServiceForTest(&now)
			svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
				"efc-site": {
					WorkspaceRoots:                   []string{"/data/efc-site"},
					DefaultWorkspaceRoot:             "/data/efc-site",
					AllowConcurrentWorkspaceSurfaces: true,
				},
			})
			svc.UpsertInstance(defaultWorkspaceTestInstance("inst-site-user-1", "/data/efc-site"))
			svc.ApplySurfaceAction(control.Action{
				Kind:             control.ActionTextMessage,
				GatewayID:        "efc-site",
				SurfaceSessionID: "feishu:efc-site:user:user-1",
				ActorUserID:      "user-1",
				MessageID:        "om-user-1-first",
				Text:             "用户一的任务",
			})

			media := tt.media
			media.GatewayID = "efc-site"
			media.SurfaceSessionID = "feishu:efc-site:user:user-2"
			media.ActorUserID = "user-2"
			mediaEvents := svc.ApplySurfaceAction(media)
			start := defaultWorkspaceStartHeadless(mediaEvents)
			second := svc.root.Surfaces[media.SurfaceSessionID]
			if start == nil || second == nil || second.PendingHeadless == nil || !second.PendingHeadless.PreserveQueuedInputs {
				t.Fatalf("expected pending default workspace bootstrap for user 2 media, got events=%#v surface=%#v", mediaEvents, second)
			}
			if len(second.StagedImages)+len(second.StagedFiles) != 1 {
				t.Fatalf("expected media staged only on user 2 surface, got %#v", second)
			}
			if owner := svc.root.Surfaces["feishu:efc-site:user:user-1"]; len(owner.StagedImages) != 0 || len(owner.StagedFiles) != 0 {
				t.Fatalf("expected user 1 surface to remain free of user 2 media, got %#v", owner)
			}

			svc.UpsertInstance(defaultWorkspaceTestInstance(start.InstanceID, "/data/efc-site"))
			svc.ApplyInstanceConnected(start.InstanceID)
			if second.PendingHeadless != nil || second.RouteMode != state.RouteModeNewThreadReady || len(second.StagedImages)+len(second.StagedFiles) != 1 {
				t.Fatalf("expected media to survive startup on user 2 new-thread route, got %#v", second)
			}

			textEvents := svc.ApplySurfaceAction(control.Action{
				Kind:             control.ActionTextMessage,
				GatewayID:        "efc-site",
				SurfaceSessionID: media.SurfaceSessionID,
				ActorUserID:      "user-2",
				MessageID:        "om-user-2-text",
				Text:             "结合附件处理",
			})
			prompt := defaultWorkspacePromptSend(textEvents)
			if prompt == nil || prompt.Origin.UserID != "user-2" || prompt.Origin.MessageID != "om-user-2-text" {
				t.Fatalf("expected user 2 media prompt dispatch, got %#v", textEvents)
			}
			tt.assertInput(t, prompt.Prompt.Inputs)
		})
	}
}

func TestDefaultWorkspaceDetachCancelsQueuedFirstInput(t *testing.T) {
	now := time.Date(2026, 7, 23, 9, 50, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.SetGatewaySurfacePolicies(map[string]GatewaySurfacePolicy{
		"efc-site": {
			WorkspaceRoots:       []string{"/data/efc-site"},
			DefaultWorkspaceRoot: "/data/efc-site",
		},
	})
	startEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		GatewayID:        "efc-site",
		SurfaceSessionID: "feishu:efc-site:user:user-1",
		ActorUserID:      "user-1",
		MessageID:        "om-user-1-first",
		Text:             "用户一的任务",
	})
	start := defaultWorkspaceStartHeadless(startEvents)
	surface := svc.root.Surfaces["feishu:efc-site:user:user-1"]
	if start == nil || surface == nil || len(surface.QueuedQueueItemIDs) != 1 {
		t.Fatalf("expected pending first input, got events=%#v surface=%#v", startEvents, surface)
	}
	queueID := surface.QueuedQueueItemIDs[0]

	cancelEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionDetach,
		GatewayID:        "efc-site",
		SurfaceSessionID: surface.SurfaceSessionID,
		ActorUserID:      "user-1",
	})
	if surface.PendingHeadless != nil || surface.ClaimedWorkspaceKey != "" || len(surface.QueuedQueueItemIDs) != 0 || surface.QueueItems[queueID].Status != state.QueueItemDiscarded {
		t.Fatalf("expected detach to terminate the pending bootstrap, got %#v", surface)
	}
	if notice := firstNoticeEvent(cancelEvents); notice == nil || notice.Code != "detached" {
		t.Fatalf("expected detach terminal notice, got %#v", cancelEvents)
	}
}
