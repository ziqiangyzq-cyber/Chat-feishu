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
