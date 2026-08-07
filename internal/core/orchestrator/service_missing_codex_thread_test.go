package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestMissingCodexRolloutUnpinsSurfaceAndPreparesNewThread(t *testing.T) {
	now := time.Date(2026, 8, 7, 11, 15, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	svc.UpsertInstance(&state.InstanceRecord{
		InstanceID:    "inst-1",
		Backend:       agentproto.BackendCodex,
		WorkspaceRoot: "/data/project",
		WorkspaceKey:  "/data/project",
		Online:        true,
		Threads: map[string]*state.ThreadRecord{
			"thread-stale": {ThreadID: "thread-stale", CWD: "/data/project", Loaded: true},
		},
	})
	svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionAttachInstance,
		SurfaceSessionID: "surface-1",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
		InstanceID:       "inst-1",
	})
	surface := svc.root.Surfaces["surface-1"]
	surface.SelectedThreadID = "thread-stale"
	surface.RouteMode = state.RouteModePinned
	surface.PreparedThreadCWD = ""
	surface.PreparedFromThreadID = ""
	svc.threadClaims["thread-stale"] = &threadClaimRecord{
		ThreadID:         "thread-stale",
		InstanceID:       "inst-1",
		SurfaceSessionID: "surface-1",
	}
	svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		SurfaceSessionID: "surface-1",
		MessageID:        "msg-1",
		Text:             "hello",
	})
	svc.BindPendingRemoteCommand("surface-1", "cmd-1")

	events := svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:                 agentproto.EventTurnCompleted,
		CommandID:            "cmd-1",
		ThreadID:             "thread-stale",
		Status:               "failed",
		ErrorMessage:         "no rollout found for thread id thread-stale",
		TurnCompletionOrigin: agentproto.TurnCompletionOriginThreadResumeRejected,
		Problem: &agentproto.ErrorInfo{
			Code:     "codex_thread_not_found",
			Layer:    "server",
			Stage:    "thread_resume_response",
			ThreadID: "thread-stale",
		},
	})

	if surface.RouteMode != state.RouteModeNewThreadReady || surface.SelectedThreadID != "" || surface.PreparedThreadCWD != "/data/project" {
		t.Fatalf("expected stale thread to recover into prepared state, got surface=%#v events=%#v item=%#v", surface, events, surface.QueueItems["queue-1"])
	}
	if item := surface.QueueItems["queue-1"]; item == nil || item.Status != state.QueueItemFailed {
		t.Fatalf("expected original message to remain failed, got %#v", item)
	}
	var sawRecoveryNotice bool
	for _, event := range events {
		if event.Notice != nil && event.Notice.Code == "codex_thread_not_found" {
			sawRecoveryNotice = true
		}
		if event.Notice != nil && event.Notice.Text == "no rollout found for thread id thread-stale" {
			t.Fatalf("raw rollout error should not be exposed after recovery: %#v", events)
		}
	}
	if !sawRecoveryNotice {
		t.Fatalf("expected recovery notice, got %#v", events)
	}

	retry := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		SurfaceSessionID: "surface-1",
		MessageID:        "msg-2",
		Text:             "retry in a new thread",
	})
	for _, event := range retry {
		if event.Command != nil && event.Command.Kind == agentproto.CommandPromptSend && event.Command.Target.CreateThreadIfMissing {
			return
		}
	}
	t.Fatalf("expected next message to create a new thread, got %#v", retry)
}
