package orchestrator

import (
	"fmt"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

// maybeAutoDeclineRequestForApproverPolicy 实现 gateway 策略里的管理员审批人语义：
// 配置了 ApproverOpenID 时，只有审批人自己的 surface 走正常审批卡片流程；
// 其他用户 surface 上的越权审批请求（approval / permissions_request_approval）
// 会被自动拒绝（复用现有 decline 通道，codex/claude 继续在沙箱内工作），
// 请求方收到拒绝 notice，同时向审批人的同 gateway 单聊 surface 推送一条
// 信息性 notice（不做跨聊天可交互审批卡片）。
//
// 返回 (events, true) 表示该请求已被策略接管，调用方不应再呈现审批卡片；
// 返回 (nil, false) 表示不适用策略，走原有流程。
func (s *Service) maybeAutoDeclineRequestForApproverPolicy(surface *state.SurfaceConsoleRecord, record *state.RequestPromptRecord, inst *state.InstanceRecord) ([]eventcontract.Event, bool) {
	if s == nil || surface == nil || record == nil {
		return nil, false
	}
	policy, ok := s.surfaceGatewayPolicy(surface)
	if !ok || policy.ApproverOpenID == "" {
		return nil, false
	}
	if strings.TrimSpace(surface.ActorUserID) == policy.ApproverOpenID {
		// 审批人自己的 surface：越权审批流程完全照旧。
		return nil, false
	}
	semantic := control.NormalizeRequestSemanticKind(record.SemanticKind, record.RequestType)
	if semantic == control.RequestSemanticPlanConfirmation {
		// 计划确认不是越权操作，不纳入审批人拦截。
		return nil, false
	}
	response := approverPolicyDeclineResponse(record)
	if response == nil {
		// 非越权审批类请求（问答 / elicitation / tool_callback 等）不拦截。
		return nil, false
	}

	bridge := control.ResolveRequestBridgeContract(record.SemanticKind, record.RequestType)
	commandID := s.nextRequestDispatchCommandID()
	events := []eventcontract.Event{
		{
			Kind:             eventcontract.KindNotice,
			SurfaceSessionID: surface.SurfaceSessionID,
			SourceMessageID:  strings.TrimSpace(record.SourceMessageID),
			Notice: &control.Notice{
				Code: "request_auto_declined_by_approver_policy",
				Text: "越权操作需管理员（审批人）放行，已自动拒绝。请联系管理员处理。",
			},
		},
		{
			Kind:             eventcontract.KindAgentCommand,
			SurfaceSessionID: surface.SurfaceSessionID,
			Command: &agentproto.Command{
				CommandID: commandID,
				Kind:      agentproto.CommandRequestRespond,
				Origin: agentproto.Origin{
					Surface: surface.SurfaceSessionID,
					UserID:  surface.ActorUserID,
					ChatID:  surface.ChatID,
				},
				Target: agentproto.Target{
					ThreadID:               record.ThreadID,
					TurnID:                 record.TurnID,
					UseActiveTurnIfOmitted: record.TurnID == "",
				},
				Request: agentproto.Request{
					RequestID:          record.RequestID,
					Response:           response,
					BridgeKind:         string(bridge.Kind),
					SemanticKind:       semantic,
					InterruptOnDecline: control.RequestBridgeShouldInterruptOnDecline(bridge, response),
				},
			},
		},
	}
	if approverNotice := s.approverPolicyReportNoticeEvent(surface, record, inst, policy.ApproverOpenID); approverNotice != nil {
		events = append(events, *approverNotice)
	}
	return events, true
}

// approverPolicyDeclineResponse 为可自动拒绝的越权审批请求构造 decline 响应，
// 形状与用户点击"拒绝"按钮时 buildRequestResponse 产生的响应一致。
func approverPolicyDeclineResponse(record *state.RequestPromptRecord) map[string]any {
	switch normalizeRequestType(record.RequestType) {
	case "approval":
		return map[string]any{
			"type":     "approval",
			"decision": "decline",
		}
	case "permissions_request_approval":
		return map[string]any{
			"permissions": []any{},
			"scope":       "turn",
		}
	default:
		return nil
	}
}

// approverPolicyReportNoticeEvent 构造发给审批人 surface 的信息性 notice。
// 审批人 surface 定位不到（该用户从未与机器人对话）时返回 nil，跳过推送。
func (s *Service) approverPolicyReportNoticeEvent(surface *state.SurfaceConsoleRecord, record *state.RequestPromptRecord, inst *state.InstanceRecord, approverOpenID string) *eventcontract.Event {
	approverSurface := s.findGatewayUserSurface(surface.GatewayID, approverOpenID)
	if approverSurface == nil || approverSurface.SurfaceSessionID == surface.SurfaceSessionID {
		return nil
	}
	actor := strings.TrimSpace(surface.ActorUserID)
	if actor == "" {
		actor = "未知用户"
	}
	workspaceKey := ""
	if inst != nil {
		if thread := inst.Threads[record.ThreadID]; thread != nil {
			workspaceKey = state.ResolveWorkspaceKey(thread.CWD, thread.WorkspaceKey)
		}
		workspaceKey = state.ResolveWorkspaceKey(workspaceKey, inst.WorkspaceKey, inst.WorkspaceRoot)
	}
	if workspaceKey == "" {
		workspaceKey = "未知工作区"
	}
	operation := strings.TrimSpace(record.Title)
	if operation == "" {
		operation = normalizeRequestType(record.RequestType)
	}
	return &eventcontract.Event{
		Kind:             eventcontract.KindNotice,
		SurfaceSessionID: approverSurface.SurfaceSessionID,
		Notice: &control.Notice{
			Code:  "approver_policy_auto_declined_report",
			Title: "越权请求已自动拒绝",
			Text:  fmt.Sprintf("用户 %s 在工作区 %s 请求了越权操作（%s），已按策略自动拒绝。", actor, workspaceKey, operation),
		},
	}
}
