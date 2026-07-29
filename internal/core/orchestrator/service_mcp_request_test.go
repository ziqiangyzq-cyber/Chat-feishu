package orchestrator

import (
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func TestPermissionsRequestPromptBecomesRenderableCard(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	attachMCPRequestTestSurface(svc)

	events := svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventRequestStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		RequestID: "req-perm-1",
		RequestPrompt: &agentproto.RequestPrompt{
			Type:  agentproto.RequestTypePermissionsRequestApproval,
			Title: "需要授予权限",
			Permissions: &agentproto.PermissionsRequestPrompt{
				Reason: "需要访问 docs.read",
				Permissions: []map[string]any{
					{"name": "docs.read", "title": "Read docs"},
				},
			},
		},
		Metadata: map[string]any{
			"requestType": "permissions_request_approval",
		},
	})

	if len(events) != 1 {
		t.Fatalf("expected renderable permissions request card, got %#v", events)
	}
	prompt := requestPromptFromEvent(t, events[0])
	if prompt.RequestType != "permissions_request_approval" || len(prompt.Options) != 3 {
		t.Fatalf("unexpected permissions prompt: %#v", prompt)
	}
	if prompt.Options[0].OptionID != "accept" || prompt.Options[1].OptionID != "acceptForSession" || prompt.Options[2].OptionID != "decline" {
		t.Fatalf("unexpected permissions request options: %#v", prompt.Options)
	}
	if record := svc.root.Surfaces["surface-1"].PendingRequests["req-perm-1"]; record == nil || record.RequestType != "permissions_request_approval" {
		t.Fatalf("expected pending permissions request state, got %#v", svc.root.Surfaces["surface-1"].PendingRequests)
	}
}

func TestRespondPermissionsRequestBuildsStructuredGrantPayload(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	attachMCPRequestTestSurface(svc)
	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventRequestStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		RequestID: "req-perm-1",
		RequestPrompt: &agentproto.RequestPrompt{
			Type: agentproto.RequestTypePermissionsRequestApproval,
			Permissions: &agentproto.PermissionsRequestPrompt{
				Permissions: []map[string]any{
					{"name": "docs.read", "title": "Read docs"},
				},
			},
		},
		Metadata: map[string]any{"requestType": "permissions_request_approval"},
	})

	events := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionRespondRequest,
		SurfaceSessionID: "surface-1",
		MessageID:        "om-card-1",
		Request:          testRequestAction("req-perm-1", "permissions_request_approval", "acceptForSession", nil, 0),
	})

	if len(events) != 2 || !events[0].InlineReplaceCurrentCard || events[1].Command == nil {
		t.Fatalf("expected sealed request replacement plus one request respond command, got %#v", events)
	}
	prompt := requestPromptFromEvent(t, events[0])
	if !prompt.Sealed || prompt.Phase != "waiting_dispatch" {
		t.Fatalf("expected permissions request to seal before dispatch, got %#v", prompt)
	}
	response := events[1].Command.Request.Response
	if response["scope"] != "session" {
		t.Fatalf("expected session-scoped permission grant, got %#v", response)
	}
	permissions, _ := response["permissions"].([]map[string]any)
	if len(permissions) != 1 || permissions[0]["name"] != "docs.read" {
		t.Fatalf("unexpected granted permissions payload: %#v", response)
	}
}

func TestRespondMCPElicitationFormBuildsStructuredResponse(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	attachMCPRequestTestSurface(svc)
	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventRequestStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		RequestID: "req-mcp-form-1",
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
						"token":    map[string]any{"type": "string", "title": "Token", "description": "OAuth token"},
						"remember": map[string]any{"type": "boolean", "title": "Remember", "description": "Remember this grant"},
					},
					"required": []any{"token"},
				},
				Meta: map[string]any{"flow": "oauth"},
			},
		},
		Metadata: map[string]any{"requestType": "mcp_server_elicitation"},
	})

	events := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionRespondRequest,
		SurfaceSessionID: "surface-1",
		MessageID:        "om-card-2",
		Request: testRequestAction("req-mcp-form-1", "mcp_server_elicitation", "", map[string][]string{
			"token":    {"secret-token"},
			"remember": {"true"},
		}, 0),
		RequestAnswers: map[string][]string{
			"token":    []string{"secret-token"},
			"remember": []string{"true"},
		},
	})

	if len(events) != 2 || !events[0].InlineReplaceCurrentCard || events[1].Command == nil {
		t.Fatalf("expected sealed inline replacement plus one mcp request respond command, got %#v", events)
	}
	prompt := requestPromptFromEvent(t, events[0])
	if !prompt.Sealed {
		t.Fatalf("expected completed mcp form to render sealed prompt, got %#v", prompt)
	}
	response := events[1].Command.Request.Response
	if response["action"] != "accept" {
		t.Fatalf("expected accept action, got %#v", response)
	}
	content, _ := response["content"].(map[string]any)
	if content["token"] != "secret-token" || content["remember"] != true {
		t.Fatalf("unexpected mcp form content: %#v", response)
	}
	meta, _ := response["_meta"].(map[string]any)
	if meta["flow"] != "oauth" {
		t.Fatalf("expected mcp response to carry prompt meta, got %#v", response)
	}
}

func TestEmptyObjectMCPElicitationFormBecomesConfirmationWithoutJSONQuestion(t *testing.T) {
	now := time.Date(2026, 7, 29, 9, 13, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	attachMCPRequestTestSurface(svc)

	events := svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventRequestStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		RequestID: "req-mcp-empty-form-1",
		RequestPrompt: &agentproto.RequestPrompt{
			Type:  agentproto.RequestTypeMCPServerElicitation,
			Title: "需要处理 MCP 请求",
			MCPElicitation: &agentproto.MCPElicitationPrompt{
				ServerName: "codex_apps",
				Mode:       "form",
				Message:    "连接 GitHub 可帮助确认目标仓库。",
				RequestedSchema: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		},
		Metadata: map[string]any{"requestType": "mcp_server_elicitation"},
	})

	if len(events) != 1 {
		t.Fatalf("expected one renderable request event, got %#v", events)
	}
	prompt := requestPromptFromEvent(t, events[0])
	if len(prompt.Questions) != 0 {
		t.Fatalf("expected no JSON question for empty object schema, got %#v", prompt.Questions)
	}
	if len(prompt.Options) != 3 || prompt.Options[0].OptionID != "accept" || prompt.Options[1].OptionID != "decline" || prompt.Options[2].OptionID != "cancel" {
		t.Fatalf("expected confirmation controls for empty object schema, got %#v", prompt.Options)
	}

	events = svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionRespondRequest,
		SurfaceSessionID: "surface-1",
		MessageID:        "om-card-empty-form",
		Request:          testRequestAction("req-mcp-empty-form-1", "mcp_server_elicitation", "accept", nil, 0),
	})
	if len(events) != 2 || events[1].Command == nil {
		t.Fatalf("expected request response dispatch, got %#v", events)
	}
	response := events[1].Command.Request.Response
	if response["action"] != "accept" {
		t.Fatalf("expected accept action, got %#v", response)
	}
	content, ok := response["content"].(map[string]any)
	if !ok || len(content) != 0 {
		t.Fatalf("expected empty object content, got %#v", response["content"])
	}
}

func TestRespondMCPElicitationFormPartialSaveRefreshesCurrentStepInline(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	attachMCPRequestTestSurface(svc)
	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventRequestStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		RequestID: "req-mcp-form-step-1",
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
		SurfaceSessionID: "surface-1",
		Request: testRequestAction("req-mcp-form-step-1", "mcp_server_elicitation", "", map[string][]string{
			"mode": {"auto"},
		}, 0),
		RequestAnswers: map[string][]string{
			"mode": {"auto"},
		},
	})
	if len(events) != 1 || !events[0].InlineReplaceCurrentCard {
		t.Fatalf("expected mcp partial save to refresh current card inline, got %#v", events)
	}
	prompt := requestPromptFromEvent(t, events[0])
	if prompt.RequestRevision != 2 || prompt.CurrentQuestionIndex != 1 {
		t.Fatalf("expected mcp partial save to advance to next question, got %#v", prompt)
	}
	if !prompt.Questions[0].Answered || prompt.Questions[0].DefaultValue != "auto" {
		t.Fatalf("expected saved mcp answer to remain in refreshed prompt, got %#v", prompt.Questions[0])
	}
}

func TestRespondMCPElicitationURLAcceptBuildsContinuePayload(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	svc := newServiceForTest(&now)
	attachMCPRequestTestSurface(svc)
	svc.ApplyAgentEvent("inst-1", agentproto.Event{
		Kind:      agentproto.EventRequestStarted,
		ThreadID:  "thread-1",
		TurnID:    "turn-1",
		RequestID: "req-mcp-url-1",
		RequestPrompt: &agentproto.RequestPrompt{
			Type: agentproto.RequestTypeMCPServerElicitation,
			MCPElicitation: &agentproto.MCPElicitationPrompt{
				ServerName:    "docs",
				Mode:          "url",
				Message:       "请完成外部授权",
				URL:           "https://example.com/approve",
				ElicitationID: "eli-1",
				Meta:          map[string]any{"flow": "oauth"},
			},
		},
		Metadata: map[string]any{"requestType": "mcp_server_elicitation"},
	})

	events := svc.ApplySurfaceAction(control.Action{
		Kind:             control.ActionRespondRequest,
		SurfaceSessionID: "surface-1",
		MessageID:        "om-card-3",
		Request:          testRequestAction("req-mcp-url-1", "mcp_server_elicitation", "accept", nil, 0),
	})

	if len(events) != 2 || !events[0].InlineReplaceCurrentCard || events[1].Command == nil {
		t.Fatalf("expected sealed request replacement plus one mcp url respond command, got %#v", events)
	}
	prompt := requestPromptFromEvent(t, events[0])
	if !prompt.Sealed || prompt.Phase != "waiting_dispatch" {
		t.Fatalf("expected url elicitation request to seal before dispatch, got %#v", prompt)
	}
	response := events[1].Command.Request.Response
	if response["action"] != "accept" {
		t.Fatalf("expected accept action for url elicitation, got %#v", response)
	}
	if _, ok := response["content"]; !ok {
		t.Fatalf("expected url elicitation response to include content field, got %#v", response)
	}
}

func attachMCPRequestTestSurface(svc *Service) {
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
}
