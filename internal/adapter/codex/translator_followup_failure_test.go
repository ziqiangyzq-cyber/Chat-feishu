package codex

import (
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

func TestObserveServerThreadStartFollowupFailureCompletesTurn(t *testing.T) {
	tr := NewTranslator("inst-1")
	if _, err := tr.TranslateCommand(planModePromptCommand("", "/tmp/project")); err != nil {
		t.Fatalf("translate command: %v", err)
	}

	result, err := tr.ObserveServer([]byte(`{"id":"relay-thread-start-0","result":{"thread":{"id":"thread-created"}}}`))
	if err != nil {
		t.Fatalf("observe thread/start response: %v", err)
	}
	assertFollowupTurnStartFailure(t, result, "thread-created")
}

func TestObserveServerThreadResumeFollowupFailureCompletesTurnAndKeepsLocalAttribution(t *testing.T) {
	tr := NewTranslator("inst-1")
	if _, err := tr.ObserveClient([]byte(`{"method":"turn/start","params":{"threadId":"thread-existing","cwd":"/tmp/project"}}`)); err != nil {
		t.Fatalf("observe local turn/start: %v", err)
	}
	if _, err := tr.ObserveClient([]byte(`{"method":"thread/resume","params":{"threadId":"thread-other","cwd":"/tmp/other"}}`)); err != nil {
		t.Fatalf("move current thread: %v", err)
	}
	if _, err := tr.TranslateCommand(planModePromptCommand("thread-existing", "/tmp/project")); err != nil {
		t.Fatalf("translate command: %v", err)
	}

	result, err := tr.ObserveServer([]byte(`{"id":"relay-thread-resume-0","result":{"thread":{"id":"thread-existing"}}}`))
	if err != nil {
		t.Fatalf("observe thread/resume response: %v", err)
	}
	assertFollowupTurnStartFailure(t, result, "thread-existing")

	started, err := tr.ObserveServer([]byte(`{"method":"turn/started","params":{"threadId":"thread-existing","turn":{"id":"turn-local"}}}`))
	if err != nil {
		t.Fatalf("observe local turn/started: %v", err)
	}
	if len(started.Events) != 1 || started.Events[0].Initiator.Kind != agentproto.InitiatorLocalUI {
		t.Fatalf("expected preserved local attribution, got %#v", started.Events)
	}
}

func assertFollowupTurnStartFailure(t *testing.T, result Result, wantThreadID string) {
	t.Helper()
	if !result.Suppress || len(result.OutboundToCodex) != 0 || len(result.Events) != 1 {
		t.Fatalf("expected suppressed failed completion without followup, got %#v", result)
	}
	event := result.Events[0]
	if event.Kind != agentproto.EventTurnCompleted || event.Status != "failed" || event.ThreadID != wantThreadID {
		t.Fatalf("unexpected failed completion: %#v", event)
	}
	if event.TurnCompletionOrigin != agentproto.TurnCompletionOriginTurnStartRejected {
		t.Fatalf("unexpected completion origin: %#v", event)
	}
	if !strings.Contains(event.ErrorMessage, "requires a model") {
		t.Fatalf("expected model resolution error, got %#v", event)
	}
}
