package orchestrator

import (
	"fmt"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

// requestGuardedByApproverPolicy 判断该请求是否属于审批人策略管辖的越权审批类：
// approval / permissions_request_approval，但不含计划确认（不是越权操作）。
func requestGuardedByApproverPolicy(record *state.RequestPromptRecord) bool {
	if record == nil {
		return false
	}
	if control.NormalizeRequestSemanticKind(record.SemanticKind, record.RequestType) == control.RequestSemanticPlanConfirmation {
		return false
	}
	switch normalizeRequestType(record.RequestType) {
	case "approval", "permissions_request_approval":
		return true
	default:
		return false
	}
}

// requestRemoteInitiatorActor 解析该请求归属 turn 的远端发起者。
// 返回 (发起 surfaceID, 发起者 open_id, 是否远端 surface 发起)。
// 本地（VS Code 等）发起的 turn 没有 remote binding，返回 remote=false。
// 发起者取 enqueue 时冻结在 queue item 上的 ActorUserID——群聊里
// surface.ActorUserID 会被每条入站消息覆写成"最近发言者"，不能作为发起者依据。
func (s *Service) requestRemoteInitiatorActor(record *state.RequestPromptRecord) (string, string, bool) {
	if s == nil || record == nil {
		return "", "", false
	}
	binding := s.lookupRemoteTurn(record.InstanceID, record.ThreadID, record.TurnID)
	if binding == nil {
		return "", "", false
	}
	surfaceID := strings.TrimSpace(binding.SurfaceSessionID)
	actor := ""
	if bindingSurface := s.root.Surfaces[surfaceID]; bindingSurface != nil {
		if item := bindingSurface.QueueItems[strings.TrimSpace(binding.QueueItemID)]; item != nil {
			actor = strings.TrimSpace(item.ActorUserID)
		}
	}
	return surfaceID, actor, true
}

// approverPolicyActorIsApprover 判断 turn 发起者是否就是审批人。
// actor 缺失（如系统 lane 的自动继续）时退回判断发起 surface 是否就是
// 审批人的单聊 surface（surface id 不可变，群聊共享问题不影响它）。
func approverPolicyActorIsApprover(actor, initiatorSurfaceID, gatewayID, approverOpenID string) bool {
	if actor != "" {
		return actor == approverOpenID
	}
	exact := approverP2PSurfaceID(gatewayID, approverOpenID)
	if exact == "" {
		return false
	}
	return initiatorSurfaceID == exact || strings.HasPrefix(initiatorSurfaceID, exact+"#")
}

func approverP2PSurfaceID(gatewayID, approverOpenID string) string {
	gatewayID = strings.TrimSpace(gatewayID)
	approverOpenID = strings.TrimSpace(approverOpenID)
	if gatewayID == "" || approverOpenID == "" {
		return ""
	}
	return "feishu:" + gatewayID + ":user:" + approverOpenID
}

// maybeAutoDeclineRequestForApproverPolicy 实现 gateway 策略里的管理员审批人语义：
// 配置了 ApproverOpenID 时，只有审批人本人发起的 turn 才走正常审批卡片流程；
// 其他用户从远端 surface 发起的越权审批请求（approval / permissions_request_approval）
// 会被自动拒绝（复用现有 decline 通道，codex/claude 继续在沙箱内工作），
// 请求方收到拒绝 notice，同时向审批人的同 gateway 单聊 surface 推送一条
// 信息性 notice（不做跨聊天可交互审批卡片）。
//
// 本地（VS Code 等）发起的 turn 一律不拦截：那是桌面用户自己的审批，
// relay 抢先 decline 会破坏本地流程。
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
	if !requestGuardedByApproverPolicy(record) {
		return nil, false
	}
	initiatorSurfaceID, initiatorActor, remoteTurn := s.requestRemoteInitiatorActor(record)
	if !remoteTurn {
		return nil, false
	}
	if approverPolicyActorIsApprover(initiatorActor, initiatorSurfaceID, surface.GatewayID, policy.ApproverOpenID) {
		return nil, false
	}
	response := approverPolicyDeclineResponse(record)
	if response == nil {
		return nil, false
	}

	// 与用户手动 decline 走同一生命周期：先入队并 markRequestSubmitting，
	// 这样 relay 派发失败时 HandleCommandDispatchFailure ->
	// restorePendingRequestDispatch 能按 commandID 找到这条记录做回滚提示，
	// decline 不会静默丢失、turn 不会无提示悬挂；成功路径由 RequestResolved
	// 事件按现有流程清理这条 pending 记录。
	enqueuePendingRequest(surface, record)
	bridge := control.ResolveRequestBridgeContract(record.SemanticKind, record.RequestType)
	commandID := s.nextRequestDispatchCommandID()
	markRequestSubmitting(record, commandID)
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
					UserID:  initiatorActor,
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
					SemanticKind:       control.NormalizeRequestSemanticKind(record.SemanticKind, record.RequestType),
					InterruptOnDecline: control.RequestBridgeShouldInterruptOnDecline(bridge, response),
				},
			},
		},
	}
	if approverNotice := s.approverPolicyReportNoticeEvent(surface, record, inst, policy.ApproverOpenID, initiatorActor); approverNotice != nil {
		events = append(events, *approverNotice)
	}
	return events, true
}

// approverPolicyResponderBlocked 是审批响应侧的强制点：配置了 ApproverOpenID 的
// gateway 上，越权审批类请求只允许审批人本人点击处理。群聊 surface 全群共享、
// surface.ActorUserID 会被覆写，因此必须在 respondRequest 按"点击者 open_id"
// 兜底校验，而不是依赖卡片呈现前的拦截。
// 返回非 nil 表示该次响应被拒绝（附带说明 notice）。
func (s *Service) approverPolicyResponderBlocked(surface *state.SurfaceConsoleRecord, request *state.RequestPromptRecord, responderOpenID string) []eventcontract.Event {
	if s == nil || surface == nil || request == nil {
		return nil
	}
	policy, ok := s.surfaceGatewayPolicy(surface)
	if !ok || policy.ApproverOpenID == "" {
		return nil
	}
	if !requestGuardedByApproverPolicy(request) {
		return nil
	}
	if strings.TrimSpace(responderOpenID) == policy.ApproverOpenID {
		return nil
	}
	return notice(surface, "request_approver_required", "该越权请求只允许管理员（审批人）处理，你的操作未生效。")
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

// approverPolicyReportNoticeEvent 构造发给审批人单聊 surface 的信息性 notice。
// 只投递到审批人的 user-scope surface；定位不到（该用户从未与机器人单聊）时
// 返回 nil，跳过推送——越权详情不允许落进群聊 surface。
func (s *Service) approverPolicyReportNoticeEvent(surface *state.SurfaceConsoleRecord, record *state.RequestPromptRecord, inst *state.InstanceRecord, approverOpenID, initiatorActor string) *eventcontract.Event {
	approverSurface := s.findGatewayUserSurface(surface.GatewayID, approverOpenID)
	if approverSurface == nil || approverSurface.SurfaceSessionID == surface.SurfaceSessionID {
		return nil
	}
	actor := strings.TrimSpace(initiatorActor)
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
