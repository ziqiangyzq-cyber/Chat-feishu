package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestWeComPendingRequestUserInputConsumesFreeText(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	attachPendingRequestTestSurface(svc, "surface-wecom", "wecom:bot", "chat-wecom", "user-wecom")

	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventRequestStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		RequestID: "req-ui-wecom-1",
		Metadata: map[string]any{
			"requestType": "request_user_input",
			"questions": []map[string]any{
				{"id": "model", "header": "模型", "question": "请选择模型", "options": []map[string]any{{"label": "gpt-5.4"}, {"label": "gpt-5.3"}}},
				{"id": "notes", "header": "备注", "question": "补充说明"},
			},
		},
	})

	events := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionRespondRequest,
		SurfaceSessionID: "surface-wecom",
		Request: testRequestAction("req-ui-wecom-1", "", "", map[string][]string{
			"model": {"gpt-5.4"},
		}, 0),
		RequestAnswers: map[string][]string{
			"model": {"gpt-5.4"},
		},
	})
	if len(events) != 1 || !events[0].InlineReplaceCurrentCard {
		t.Fatalf("expected first answer to refresh current card inline, got %#v", events)
	}
	prompt := requestPromptFromEvent(t, events[0])
	if prompt.CurrentQuestionIndex != 1 {
		t.Fatalf("expected first answer to advance to text question, got %#v", prompt)
	}

	events = svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		SurfaceSessionID: "surface-wecom",
		GatewayID:        "wecom:bot",
		ChatID:           "chat-wecom",
		ActorUserID:      "user-wecom",
		MessageID:        "om-wecom-msg-1",
		Text:             "请用中文回复",
	})
	if len(events) != 2 || !events[0].InlineReplaceCurrentCard || events[1].Command == nil {
		t.Fatalf("expected wecom free text to seal card and dispatch command, got %#v", events)
	}
	prompt = requestPromptFromEvent(t, events[0])
	if !prompt.Sealed {
		t.Fatalf("expected completed prompt to render sealed request, got %#v", prompt)
	}
	answers, _ := events[1].Command.Request.Response["answers"].(map[string]any)
	if _, ok := answers["model"]; !ok {
		t.Fatalf("expected stored option answer in payload, got %#v", events[1].Command.Request.Response)
	}
	if _, ok := answers["notes"]; !ok {
		t.Fatalf("expected wecom free-text answer in payload, got %#v", events[1].Command.Request.Response)
	}
	if len(svc.root.Surfaces["surface-wecom"].QueueItems) != 0 {
		t.Fatalf("expected no queued text while request consumed directly, got %#v", svc.root.Surfaces["surface-wecom"].QueueItems)
	}
}

func TestFeishuPendingTextQuestionStillBlocksFreeText(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	attachPendingRequestTestSurface(svc, "surface-feishu", "app-1", "chat-feishu", "user-feishu")

	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventRequestStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		RequestID: "req-ui-feishu-1",
		Metadata: map[string]any{
			"requestType": "request_user_input",
			"questions": []map[string]any{
				{"id": "notes", "header": "备注", "question": "补充说明"},
			},
		},
	})

	events := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		SurfaceSessionID: "surface-feishu",
		GatewayID:        "app-1",
		ChatID:           "chat-feishu",
		ActorUserID:      "user-feishu",
		MessageID:        "om-feishu-msg-1",
		Text:             "请直接继续",
	})
	if len(events) != 1 || events[0].Notice == nil || events[0].Notice.Code != "request_pending" {
		t.Fatalf("expected feishu text reply to stay blocked, got %#v", events)
	}
	if len(svc.root.Surfaces["surface-feishu"].QueueItems) != 0 {
		t.Fatalf("expected no queued text while request is pending, got %#v", svc.root.Surfaces["surface-feishu"].QueueItems)
	}
}

func TestWeComPendingMCPElicitationConsumesFreeText(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	attachPendingRequestTestSurface(svc, "surface-wecom", "wecom:bot", "chat-wecom", "user-wecom")

	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventRequestStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		RequestID: "req-mcp-wecom-1",
		RequestPrompt: &agentproto.RequestPrompt{
			Type:  agentproto.RequestTypeMCPServerElicitation,
			Title: "需要处理 MCP 请求",
			MCPElicitation: &agentproto.MCPElicitationPrompt{
				ServerName: "docs",
				Mode:       "form",
				Message:    "请补充返回内容",
				RequestedSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"mode":  map[string]any{"type": "string", "title": "模式", "enum": []any{"auto", "manual"}},
						"token": map[string]any{"type": "string", "title": "Token"},
					},
					"required": []any{"mode", "token"},
				},
			},
		},
		Metadata: map[string]any{"requestType": "mcp_server_elicitation"},
	})

	events := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionRespondRequest,
		SurfaceSessionID: "surface-wecom",
		Request: testRequestAction("req-mcp-wecom-1", "mcp_server_elicitation", "", map[string][]string{
			"mode": {"auto"},
		}, 0),
		RequestAnswers: map[string][]string{
			"mode": {"auto"},
		},
	})
	if len(events) != 1 || !events[0].InlineReplaceCurrentCard {
		t.Fatalf("expected first MCP answer to refresh current card inline, got %#v", events)
	}
	prompt := requestPromptFromEvent(t, events[0])
	if prompt.CurrentQuestionIndex != 1 {
		t.Fatalf("expected first MCP answer to advance to text field, got %#v", prompt)
	}

	events = svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		SurfaceSessionID: "surface-wecom",
		GatewayID:        "wecom:bot",
		ChatID:           "chat-wecom",
		ActorUserID:      "user-wecom",
		MessageID:        "om-wecom-msg-2",
		Text:             "secret-token",
	})
	if len(events) != 2 || !events[0].InlineReplaceCurrentCard || events[1].Command == nil {
		t.Fatalf("expected wecom free text to complete mcp request, got %#v", events)
	}
	prompt = requestPromptFromEvent(t, events[0])
	if !prompt.Sealed {
		t.Fatalf("expected completed mcp prompt to render sealed request, got %#v", prompt)
	}
	response := events[1].Command.Request.Response
	if response["action"] != "accept" {
		t.Fatalf("expected accept action, got %#v", response)
	}
	content, _ := response["content"].(map[string]any)
	if content["mode"] != "auto" || content["token"] != "secret-token" {
		t.Fatalf("expected merged mcp content, got %#v", response)
	}
}

func TestWeComDirectResponseOptionQuestionStillBlocksFreeText(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	attachPendingRequestTestSurface(svc, "surface-wecom", "wecom:bot", "chat-wecom", "user-wecom")

	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventRequestStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		RequestID: "req-ui-wecom-2",
		Metadata: map[string]any{
			"requestType": "request_user_input",
			"questions": []map[string]any{
				{"id": "model", "header": "模型", "question": "请选择模型", "options": []map[string]any{{"label": "gpt-5.4"}, {"label": "gpt-5.3"}}},
			},
		},
	})

	events := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionTextMessage,
		SurfaceSessionID: "surface-wecom",
		GatewayID:        "wecom:bot",
		ChatID:           "chat-wecom",
		ActorUserID:      "user-wecom",
		MessageID:        "om-wecom-msg-3",
		Text:             "gpt-5.4",
	})
	if len(events) != 1 || events[0].Notice == nil || events[0].Notice.Code != "request_pending" {
		t.Fatalf("expected direct-response option question to stay blocked, got %#v", events)
	}
	if len(svc.root.Surfaces["surface-wecom"].QueueItems) != 0 {
		t.Fatalf("expected no queued text while option-only question is pending, got %#v", svc.root.Surfaces["surface-wecom"].QueueItems)
	}
}

func attachPendingRequestTestSurface(svc *Service, surfaceID, gatewayID, chatID, actorUserID string) {
	svc.MaterializeSurface(surfaceID, gatewayID, chatID, actorUserID)
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
	svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionAttachInstance,
		SurfaceSessionID: surfaceID,
		GatewayID:        gatewayID,
		ChatID:           chatID,
		ActorUserID:      actorUserID,
		InstanceID:       "inst-1",
	})
	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventTurnStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		Initiator: agentproto.Initiator{Kind: agentproto.InitiatorLocalUI},
	})
}
