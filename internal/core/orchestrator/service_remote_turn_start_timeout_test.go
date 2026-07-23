package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/renderer"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func newRemoteTurnStartTimeoutService(now *time.Time) *Service {
	svc := NewService(func() time.Time { return *now }, Config{
		RemoteTurnStartWait: 5 * time.Second,
		GitAvailable:        true,
	}, renderer.NewPlanner())
	svc.UpsertInstance(&state.InstanceRecord{
		InstanceID:              "inst-1",
		DisplayName:             "repo",
		WorkspaceRoot:           "/data/repo",
		WorkspaceKey:            "/data/repo",
		ShortName:               "repo",
		Source:                  "headless",
		Managed:                 true,
		Online:                  true,
		ObservedFocusedThreadID: "thread-1",
		Threads: map[string]*state.ThreadRecord{
			"thread-1": {
				ThreadID: "thread-1",
				Name:     "旧会话",
				CWD:      "/data/repo",
				Loaded:   true,
			},
		},
	})
	svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionAttachInstance,
		SurfaceSessionID: "surface-1",
		ChatID:           "chat-1",
		ActorUserID:      "user-1",
		InstanceID:       "inst-1",
	})
	svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionUseThread,
		SurfaceSessionID: "surface-1",
		ThreadID:         "thread-1",
	})
	svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		SurfaceSessionID: "surface-1",
		MessageID:        "msg-1",
		Text:             "继续",
	})
	svc.BindPendingRemoteCommand("surface-1", "cmd-1")
	return svc
}

func TestNewServiceDefaultsRemoteTurnStartWaitToSixtySeconds(t *testing.T) {
	svc := NewService(time.Now, Config{}, renderer.NewPlanner())
	if got := svc.config.RemoteTurnStartWait; got != 60*time.Second {
		t.Fatalf("RemoteTurnStartWait = %s, want 60s", got)
	}
}

func TestTickExpiresPendingRemoteTurnThatNeverStarts(t *testing.T) {
	now := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	svc := newRemoteTurnStartTimeoutService(&now)

	statuses := svc.PendingRemoteTurns()
	if len(statuses) != 1 || statuses[0].CommandID != "cmd-1" || statuses[0].DispatchedAt == "" {
		t.Fatalf("expected pending turn dispatch timestamp, got %#v", statuses)
	}
	if events := svc.Tick(now.Add(4 * time.Second)); len(events) != 0 {
		t.Fatalf("expected pending turn to remain before deadline, got %#v", events)
	}

	events := svc.Tick(now.Add(6 * time.Second))
	surface := svc.root.Surfaces["surface-1"]
	item := surface.QueueItems["queue-1"]
	if item == nil || item.Status != state.QueueItemFailed {
		t.Fatalf("expected timed-out queue item to fail, got %#v", item)
	}
	if surface.ActiveQueueItemID != "" || len(svc.PendingRemoteTurns()) != 0 {
		t.Fatalf("expected timeout to clear remote ownership, surface=%#v pending=%#v", surface, svc.PendingRemoteTurns())
	}

	var sawNotice, sawKill bool
	for _, event := range events {
		if event.Notice != nil && event.Notice.Code == "remote_turn_start_timeout" {
			sawNotice = true
		}
		if event.DaemonCommand != nil &&
			event.DaemonCommand.Kind == control.DaemonCommandKillHeadless &&
			event.DaemonCommand.InstanceID == "inst-1" {
			sawKill = true
		}
	}
	if !sawNotice || !sawKill {
		t.Fatalf("expected timeout notice and managed-headless recycle, got %#v", events)
	}
}

func TestTickDoesNotExpireRemoteTurnAfterTurnStarted(t *testing.T) {
	now := time.Date(2026, 7, 24, 1, 10, 0, 0, time.UTC)
	svc := newRemoteTurnStartTimeoutService(&now)

	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventTurnStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		CommandID: "cmd-1",
		Initiator: agentproto.Initiator{
			Kind:             agentproto.InitiatorRemoteSurface,
			SurfaceSessionID: "surface-1",
		},
	})

	events := svc.Tick(now.Add(6 * time.Second))
	for _, event := range events {
		if event.Notice != nil && event.Notice.Code == "remote_turn_start_timeout" {
			t.Fatalf("did not expect timeout after turn.started, got %#v", events)
		}
		if event.DaemonCommand != nil && event.DaemonCommand.Kind == control.DaemonCommandKillHeadless {
			t.Fatalf("did not expect headless recycle after turn.started, got %#v", events)
		}
	}
	if got := svc.root.Surfaces["surface-1"].QueueItems["queue-1"]; got == nil || got.Status != state.QueueItemRunning {
		t.Fatalf("expected queue item to remain running, got %#v", got)
	}
}
