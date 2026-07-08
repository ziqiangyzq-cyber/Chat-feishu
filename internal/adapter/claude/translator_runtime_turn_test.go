package claude

import (
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
)

// A background task-notification resumes the Claude CLI on its own, producing a
// full message/result stream with NO daemon prompt.send behind it. Before the
// fix the translator early-returned on activeTurn==nil and dropped the entire
// wakeup turn (the "subagent result never reaches Feishu" bug). It must now
// synthesize a runtime-initiated turn so the output flows through the normal
// pipeline.
func TestClaudeTranslatorSynthesizesRuntimeTurnForOrphanWakeup(t *testing.T) {
	tr := NewTranslator("inst-1")
	// Wakeup turns carry their own system/init, so the session id is authoritative.
	observeClaude(t, tr, map[string]any{
		"type":           "system",
		"subtype":        "init",
		"session_id":     "session-wake-1",
		"cwd":            "/data/dl/droid",
		"model":          "mimo-v2.5-pro",
		"permissionMode": "default",
	})

	started := observeClaude(t, tr, map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":      "msg-wake-1",
				"type":    "message",
				"role":    "assistant",
				"model":   "mimo-v2.5-pro",
				"content": []any{},
			},
		},
	})
	if len(started.Events) != 1 || started.Events[0].Kind != agentproto.EventTurnStarted {
		t.Fatalf("expected synthesized turn.started, got %#v", started.Events)
	}
	turnStarted := started.Events[0]
	if turnStarted.CommandID != "" {
		t.Fatalf("synthesized runtime turn must have empty CommandID, got %q", turnStarted.CommandID)
	}
	if turnStarted.Initiator.Kind != agentproto.InitiatorUnknown {
		t.Fatalf("expected unknown initiator, got %#v", turnStarted.Initiator)
	}
	if turnStarted.ThreadID != "session-wake-1" {
		t.Fatalf("expected canonical session thread id, got %q", turnStarted.ThreadID)
	}
	turnID := turnStarted.TurnID

	// Output that was previously dropped must now flow.
	observeClaude(t, tr, map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]any{
				"type": "text",
			},
		},
	})
	delta := observeClaude(t, tr, map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{
				"type": "text_delta",
				"text": "TABDELIVERYPROBE",
			},
		},
	})
	sawDelta := false
	for _, ev := range delta.Events {
		if ev.Kind == agentproto.EventItemDelta && ev.Delta == "TABDELIVERYPROBE" && ev.TurnID == turnID {
			sawDelta = true
		}
	}
	if !sawDelta {
		t.Fatalf("expected wakeup text delta to surface on synthesized turn, got %#v", delta.Events)
	}

	result := observeClaude(t, tr, map[string]any{
		"type":     "result",
		"subtype":  "success",
		"is_error": false,
		"result":   "done",
	})
	last := result.Events[len(result.Events)-1]
	if last.Kind != agentproto.EventTurnCompleted || last.TurnID != turnID {
		t.Fatalf("expected wakeup turn to complete, got %#v", result.Events)
	}
}

// A subagent's own message carries a non-empty parent_tool_use_id. With no
// active turn, it must NOT be promoted into a user-facing turn (its content
// belongs to the parent tool call, not the surface).
func TestClaudeTranslatorDoesNotSurfaceSubagentMessageWithoutTurn(t *testing.T) {
	tr := NewTranslator("inst-1")
	observeClaude(t, tr, map[string]any{
		"type":           "system",
		"subtype":        "init",
		"session_id":     "session-sub-1",
		"cwd":            "/data/dl/droid",
		"model":          "mimo-v2.5-pro",
		"permissionMode": "default",
	})

	res := observeClaude(t, tr, map[string]any{
		"type":               "assistant",
		"parent_tool_use_id": "toolu_subagent_1",
		"message": map[string]any{
			"id":   "msg-sub-1",
			"type": "message",
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "text", "text": "internal subagent chatter"},
			},
		},
	})
	if len(res.Events) != 0 {
		t.Fatalf("expected subagent message without a turn to stay hidden, got %#v", res.Events)
	}
	if tr.activeTurn != nil {
		t.Fatalf("subagent message must not synthesize a turn, activeTurn=%#v", tr.activeTurn)
	}
}

// A can_use_tool control_request arriving with no active turn (e.g. a wakeup
// turn requesting a tool) must not be silently swallowed — that hangs the CLI
// forever waiting for an approval reply, starving the run. It must synthesize a
// turn so the approval surfaces.
func TestClaudeTranslatorSynthesizesTurnForOrphanControlRequest(t *testing.T) {
	tr := NewTranslator("inst-1")
	observeClaude(t, tr, map[string]any{
		"type":           "system",
		"subtype":        "init",
		"session_id":     "session-ctrl-1",
		"cwd":            "/data/dl/droid",
		"model":          "mimo-v2.5-pro",
		"permissionMode": "default",
	})

	res := observeClaude(t, tr, map[string]any{
		"type":       "control_request",
		"request_id": "req-orphan-1",
		"request": map[string]any{
			"subtype":     "can_use_tool",
			"tool_name":   "Bash",
			"tool_use_id": "call-orphan-1",
			"input":       map[string]any{"command": "echo hi", "description": "echo hi"},
		},
	})
	if tr.activeTurn == nil {
		t.Fatalf("orphan control_request must synthesize a turn, activeTurn is nil")
	}
	sawTurnStarted := false
	for _, ev := range res.Events {
		if ev.Kind == agentproto.EventTurnStarted {
			sawTurnStarted = true
		}
	}
	if !sawTurnStarted {
		t.Fatalf("expected synthesized turn.started before the approval, got %#v", res.Events)
	}
	if len(res.Events) < 2 {
		t.Fatalf("expected the approval request to surface alongside turn.started, got %#v", res.Events)
	}
}

// A subagent's own control_request (non-empty parent) must not be promoted into
// a user-facing turn.
func TestClaudeTranslatorDoesNotSurfaceSubagentControlRequest(t *testing.T) {
	tr := NewTranslator("inst-1")
	observeClaude(t, tr, map[string]any{
		"type":           "system",
		"subtype":        "init",
		"session_id":     "session-ctrl-2",
		"cwd":            "/data/dl/droid",
		"model":          "mimo-v2.5-pro",
		"permissionMode": "default",
	})
	res := observeClaude(t, tr, map[string]any{
		"type":               "control_request",
		"request_id":         "req-sub-1",
		"parent_tool_use_id": "toolu_subagent_1",
		"request": map[string]any{
			"subtype":     "can_use_tool",
			"tool_name":   "Bash",
			"tool_use_id": "call-sub-1",
			"input":       map[string]any{"command": "echo hi"},
		},
	})
	if tr.activeTurn != nil {
		t.Fatalf("subagent control_request must not synthesize a turn, activeTurn=%#v", tr.activeTurn)
	}
	if len(res.Events) != 0 {
		t.Fatalf("expected subagent control_request to stay hidden, got %#v", res.Events)
	}
}
