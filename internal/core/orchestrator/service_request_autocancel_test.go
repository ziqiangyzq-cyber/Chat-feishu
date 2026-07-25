package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/renderer"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func newAutoCancelTestService(now *time.Time, ttl time.Duration) *Service {
	return NewService(func() time.Time { return *now }, Config{
		TurnHandoffWait:                800 * time.Millisecond,
		RequestUserInputAutoCancelWait: ttl,
		GitAvailable:                   true,
	}, renderer.NewPlanner())
}

func seedPendingRequestUserInput(t *testing.T, svc *Service, now time.Time) *state.SurfaceConsoleRecord {
	t.Helper()
	svc.UpsertInstance(&state.InstanceRecord{
		InstanceID:              "inst-1",
		DisplayName:             "droid",
		WorkspaceRoot:           "/data/dl/droid",
		WorkspaceKey:            "/data/dl/droid",
		ShortName:               "droid",
		Online:                  true,
		ObservedFocusedThreadID: "thread-1",
		Threads: map[string]*state.ThreadRecord{
			"thread-1": {ThreadID: "thread-1", Name: "修复登录流程", CWD: "/data/dl/droid", Loaded: true},
		},
	})
	svc.ApplySurfaceAction(control.Action{Kind: control.ActionAttachInstance, SurfaceSessionID: "surface-1", ChatID: "chat-1", ActorUserID: "user-1", InstanceID: "inst-1"})
	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventTurnStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		Initiator: agentproto.Initiator{Kind: agentproto.InitiatorLocalUI},
	})
	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventRequestStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		RequestID: "req-ui-1",
		Metadata: map[string]any{
			"requestType": "request_user_input",
			"title":       "需要补充输入",
			"itemId":      "item-1",
			"questions": []map[string]any{
				{"id": "model", "header": "模型", "question": "请选择模型", "options": []map[string]any{{"label": "gpt-5.4"}, {"label": "opus"}}},
			},
		},
	})
	surface := svc.root.Surfaces["surface-1"]
	if surface == nil || surface.PendingRequests["req-ui-1"] == nil {
		t.Fatalf("expected seeded pending request_user_input, got %#v", surface)
	}
	return surface
}

func countTurnInterrupts(events []eventcontract.Event) int {
	n := 0
	for _, event := range events {
		if event.Command != nil && event.Command.Kind == agentproto.CommandTurnInterrupt {
			n++
		}
	}
	return n
}

func hasNoticeCode(events []eventcontract.Event, code string) bool {
	for _, event := range events {
		if event.Notice != nil && event.Notice.Code == code {
			return true
		}
	}
	return false
}

func TestTickAutoCancelsStaleRequestUserInputCard(t *testing.T) {
	now := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	svc := newAutoCancelTestService(&now, 10*time.Minute)
	surface := seedPendingRequestUserInput(t, svc, now)

	// Before the deadline the sweep leaves the card untouched.
	now = now.Add(9 * time.Minute)
	if events := svc.Tick(now); countTurnInterrupts(events) != 0 || hasNoticeCode(events, "request_user_input_auto_cancelled") {
		t.Fatalf("did not expect auto-cancel before TTL, got %#v", events)
	}
	if surface.PendingRequests["req-ui-1"] == nil {
		t.Fatalf("expected pending request to survive before TTL")
	}

	// Past the deadline the card is cancelled: the turn is interrupted, a notice is
	// posted, and the pending request is cleared.
	now = now.Add(2 * time.Minute)
	events := svc.Tick(now)
	if countTurnInterrupts(events) != 1 {
		t.Fatalf("expected exactly one turn interrupt on expiry, got %#v", events)
	}
	if !hasNoticeCode(events, "request_user_input_auto_cancelled") {
		t.Fatalf("expected auto-cancel notice on expiry, got %#v", events)
	}
	if surface.PendingRequests["req-ui-1"] != nil {
		t.Fatalf("expected pending request cleared after auto-cancel, got %#v", surface.PendingRequests)
	}

	// A second sweep must not re-fire once the card is gone.
	now = now.Add(20 * time.Minute)
	if events := svc.Tick(now); countTurnInterrupts(events) != 0 {
		t.Fatalf("did not expect a repeated auto-cancel, got %#v", events)
	}
}

func TestRequestUserInputPromptExpiredGuards(t *testing.T) {
	base := time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)
	ttl := 10 * time.Minute
	past := base.Add(ttl + time.Minute)

	if requestUserInputPromptExpired(nil, past, ttl) {
		t.Fatalf("nil request must never expire")
	}
	if requestUserInputPromptExpired(&state.RequestPromptRecord{RequestType: "approval", CreatedAt: base}, past, ttl) {
		t.Fatalf("non request_user_input must not expire via this sweep")
	}
	if requestUserInputPromptExpired(&state.RequestPromptRecord{RequestType: "request_user_input"}, past, ttl) {
		t.Fatalf("zero CreatedAt must not expire")
	}
	if requestUserInputPromptExpired(&state.RequestPromptRecord{RequestType: "request_user_input", CreatedAt: base, LifecycleState: requestLifecycleSubmitting}, past, ttl) {
		t.Fatalf("already-submitting request must not expire")
	}
	if requestUserInputPromptExpired(&state.RequestPromptRecord{RequestType: "request_user_input", CreatedAt: base, LifecycleState: requestLifecycleResolved}, past, ttl) {
		t.Fatalf("resolved request must not expire")
	}
	if !requestUserInputPromptExpired(&state.RequestPromptRecord{RequestType: "request_user_input", CreatedAt: base}, past, ttl) {
		t.Fatalf("an unanswered request past its TTL must expire")
	}
	if requestUserInputPromptExpired(&state.RequestPromptRecord{RequestType: "request_user_input", CreatedAt: base}, base.Add(ttl-time.Second), ttl) {
		t.Fatalf("request before its TTL must not expire")
	}
}
