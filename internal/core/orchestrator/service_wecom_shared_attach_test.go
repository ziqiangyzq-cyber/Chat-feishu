package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestSharedAttachAllowsSecondSurfaceToQueueOnSameHeadlessInstance(t *testing.T) {
	now := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.UpsertInstance(&state.InstanceRecord{
		InstanceID:    "inst-1",
		DisplayName:   "repo",
		WorkspaceRoot: "/data/repo",
		WorkspaceKey:  "/data/repo",
		ShortName:     "repo",
		Online:        true,
		Threads: map[string]*state.ThreadRecord{
			"thread-1": {ThreadID: "thread-1", Name: "A", CWD: "/data/repo", Loaded: true},
		},
	})

	svc.MaterializeSurface("surface-feishu", "app-1", "chat-1", "user-1")
	svc.MaterializeSurface("surface-wecom", "wecom:bot", "chat-2", "user-2")

	svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionAttachWorkspace,
		SurfaceSessionID: "surface-feishu",
		GatewayID:        "app-1",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
		WorkspaceKey:     "/data/repo",
	})

	first := svc.root.Surfaces["surface-feishu"]
	second := svc.root.Surfaces["surface-wecom"]
	if first == nil || first.AttachedInstanceID != "inst-1" {
		t.Fatalf("expected first attach to succeed, got %#v", first)
	}
	second.SharedAttach = true
	second.ClaimedWorkspaceKey = "/data/repo"
	if !svc.transitionSurfaceRouteCore(second, svc.root.Instances["inst-1"], surfaceRouteCoreState{
		AttachedInstanceID: "inst-1",
		WorkspaceKey:       "/data/repo",
		RouteMode:          state.RouteModeUnbound,
	}) {
		t.Fatal("expected shared attach to succeed")
	}

	firstEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		SurfaceSessionID: "surface-feishu",
		MessageID:        "msg-1",
		Text:             "first",
	})
	var dispatchedFirst bool
	for _, event := range firstEvents {
		if event.Command != nil {
			dispatchedFirst = true
			break
		}
	}
	if !dispatchedFirst {
		t.Fatalf("expected first surface to dispatch immediately, got %#v", firstEvents)
	}

	secondEvents := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		SurfaceSessionID: "surface-wecom",
		MessageID:        "msg-2",
		Text:             "second",
	})
	if len(secondEvents) == 0 || secondEvents[0].PendingInput == nil || secondEvents[0].PendingInput.Status != string(state.QueueItemQueued) {
		t.Fatalf("expected second surface input to queue, got %#v", secondEvents)
	}

	_ = svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventTurnStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		Initiator: agentproto.Initiator{Kind: agentproto.InitiatorUnknown},
	})

	finished := svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventTurnCompleted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		Status:    "completed",
		Initiator: agentproto.Initiator{Kind: agentproto.InitiatorUnknown},
	})
	var dispatchedSecond bool
	for _, event := range finished {
		if event.Command != nil && event.SurfaceSessionID == "surface-wecom" {
			dispatchedSecond = true
			break
		}
	}
	if !dispatchedSecond {
		t.Fatalf("expected second surface to dispatch after first turn completion, got %#v", finished)
	}
}

func TestSharedAttachKeepsNewlyCreatedThreadRoutableWithoutExclusiveClaim(t *testing.T) {
	now := time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.UpsertInstance(&state.InstanceRecord{
		InstanceID:    "inst-1",
		DisplayName:   "repo",
		WorkspaceRoot: "/data/repo",
		WorkspaceKey:  "/data/repo",
		ShortName:     "repo",
		Online:        true,
		Threads: map[string]*state.ThreadRecord{
			"thread-owner": {ThreadID: "thread-owner", Name: "owner", CWD: "/data/repo", Loaded: true},
		},
	})

	svc.MaterializeSurface("surface-feishu", "app-1", "chat-1", "user-1")
	svc.MaterializeSurface("surface-wecom", "wecom:bot", "chat-2", "user-2")
	owner := svc.root.Surfaces["surface-feishu"]
	shared := svc.root.Surfaces["surface-wecom"]
	if !svc.transitionSurfaceRouteCore(owner, svc.root.Instances["inst-1"], surfaceRouteCoreState{
		AttachedInstanceID: "inst-1",
		WorkspaceKey:       "/data/repo",
		RouteMode:          state.RouteModePinned,
		SelectedThreadID:   "thread-owner",
		ThreadClaimPolicy:  surfaceRouteThreadClaimVisible,
	}) {
		t.Fatal("expected owner attach to succeed")
	}
	shared.SharedAttach = true
	shared.ClaimedWorkspaceKey = "/data/repo"
	if !svc.transitionSurfaceRouteCore(shared, svc.root.Instances["inst-1"], surfaceRouteCoreState{
		AttachedInstanceID: "inst-1",
		WorkspaceKey:       "/data/repo",
		RouteMode:          state.RouteModeNewThreadReady,
		PreparedThreadCWD:  "/data/repo",
	}) {
		t.Fatal("expected shared new-thread-ready attach to succeed")
	}

	first := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		SurfaceSessionID: "surface-wecom",
		MessageID:        "msg-1",
		Text:             "first",
	})
	var sawCommand bool
	for _, event := range first {
		if event.Command != nil {
			sawCommand = true
		}
	}
	if !sawCommand {
		t.Fatalf("expected first text to create a thread, got %#v", first)
	}
	commandID := "cmd-1"
	svc.BindPendingRemoteCommand("surface-wecom", commandID)

	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventTurnStarted,
		CommandID: commandID,
		ThreadID:  "thread-created",
		TurnID:    "turn-1",
		Initiator: agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: "surface-wecom"},
	})
	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:                 agentproto.EventTurnCompleted,
		CommandID:            commandID,
		ThreadID:             "thread-created",
		TurnID:               "turn-1",
		Status:               "completed",
		Initiator:            agentproto.Initiator{Kind: agentproto.InitiatorRemoteSurface, SurfaceSessionID: "surface-wecom"},
		TurnCompletionOrigin: agentproto.TurnCompletionOriginRuntime,
	})

	if shared.RouteMode != state.RouteModePinned || shared.SelectedThreadID != "thread-created" {
		t.Fatalf("expected shared surface to keep created thread selected, got %#v", shared)
	}
	if !svc.surfaceOwnsThread(shared, "thread-created") {
		t.Fatal("expected lease-less shared selection to remain routable")
	}
	second := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		SurfaceSessionID: "surface-wecom",
		MessageID:        "msg-2",
		Text:             "continue",
	})
	for _, event := range second {
		if event.Notice != nil && event.Notice.Code == "thread_not_ready" {
			t.Fatalf("expected follow-up text to use the created thread, got %#v", second)
		}
	}
}
