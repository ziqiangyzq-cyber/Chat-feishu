package orchestrator

import (
	"fmt"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func (s *Service) consumeCapturedRequestFeedback(surface *state.SurfaceConsoleRecord, action control.Action, text string) []eventcontract.Event {
	capture := surface.ActiveRequestCapture
	if requestCaptureExpired(s.now(), capture) {
		clearSurfaceRequestCapture(surface)
		return notice(surface, "request_capture_expired", "上一条确认反馈已过期，请重新点击卡片按钮后再发送处理意见。")
	}
	if capture == nil {
		clearSurfaceRequestCapture(surface)
		return notice(surface, "request_capture_expired", "当前反馈模式已失效，请重新处理确认卡片。")
	}
	request := surface.PendingRequests[capture.RequestID]
	if request == nil {
		clearSurfaceRequestCapture(surface)
		return notice(surface, "request_expired", "这个确认请求已经结束或过期了。请重新发送消息。")
	}
	inst := s.root.Instances[request.InstanceID]
	if inst == nil {
		clearSurfaceRequestCapture(surface)
		return notice(surface, "not_attached", s.attachedTargetUnavailableText(surface))
	}
	if capture.Mode == requestCaptureModePlanReviseFeedback {
		clearSurfaceRequestCapture(surface)
		return s.dispatchRequestResponse(surface, request, action, map[string]any{
			"type":     "approval",
			"decision": "revise",
			"message":  strings.TrimSpace(text),
		}, "已提交修改意见，等待 Claude 调整当前计划。")
	}
	if capture.Mode == requestCaptureModeSameRequestDecline {
		clearSurfaceRequestCapture(surface)
		return s.dispatchRequestResponse(surface, request, action, map[string]any{
			"type":     "approval",
			"decision": "decline",
			"message":  strings.TrimSpace(text),
		}, "已提交处理意见，等待 Claude 调整当前工具调用。")
	}
	if capture.Mode != requestCaptureModeDeclineWithFeedback {
		clearSurfaceRequestCapture(surface)
		return notice(surface, "request_capture_expired", "当前反馈模式已失效，请重新处理确认卡片。")
	}

	threadID := request.ThreadID
	cwd := inst.WorkspaceRoot
	routeMode := state.RouteModePinned
	if thread := inst.Threads[threadID]; threadVisible(thread) && thread.CWD != "" {
		cwd = thread.CWD
	}
	if threadID == "" {
		var createThread bool
		threadID, cwd, routeMode, createThread = freezeRoute(inst, surface)
		_ = createThread
	}

	clearSurfaceRequestCapture(surface)
	events := []eventcontract.Event{{
		Kind:             eventcontract.KindAgentCommand,
		SurfaceSessionID: surface.SurfaceSessionID,
		Command: &agentproto.Command{
			Kind: agentproto.CommandRequestRespond,
			Origin: agentproto.Origin{
				Surface:   surface.SurfaceSessionID,
				UserID:    surface.ActorUserID,
				ChatID:    surface.ChatID,
				MessageID: action.MessageID,
			},
			Target: agentproto.Target{
				ThreadID:               request.ThreadID,
				TurnID:                 request.TurnID,
				UseActiveTurnIfOmitted: request.TurnID == "",
			},
			Request: agentproto.Request{
				RequestID: request.RequestID,
				Response: map[string]any{
					"type":     "approval",
					"decision": "decline",
				},
			},
		},
	}}
	events = append(events, notice(surface, "request_feedback_queued", "已记录处理意见。当前确认会先被拒绝，随后继续处理你的下一步要求。")...)
	events = append(events, s.enqueueQueueItem(surface, action.MessageID, text, nil, []agentproto.Input{{Type: agentproto.InputText, Text: text}}, threadID, cwd, routeMode, surface.PromptOverride, true)...)
	return events
}

func (s *Service) maybeConsumePendingRequestFreeText(surface *state.SurfaceConsoleRecord, action control.Action, text string) ([]eventcontract.Event, bool) {
	if !surfaceSupportsPendingRequestFreeText(surface) {
		return nil, false
	}
	request := activePendingRequest(surface)
	if request == nil {
		return nil, false
	}
	if request.StructuredForm != nil {
		field, _, ok := requestPromptCurrentStructuredFormField(request)
		if !ok || field.Kind != state.RequestPromptFormFieldText {
			return nil, false
		}
		fieldName := strings.TrimSpace(field.Name)
		if fieldName == "" {
			return nil, false
		}
		requestAnswers := map[string][]string{
			fieldName: {strings.TrimSpace(text)},
		}
		return s.consumePendingStructuredFormFreeText(surface, request, action, requestAnswers), true
	}
	question, _, ok := requestPromptCurrentQuestionRecord(request)
	if !ok || !requestQuestionAllowsFreeText(question) {
		return nil, false
	}
	questionID := strings.TrimSpace(question.ID)
	if questionID == "" {
		return nil, false
	}
	requestAnswers := map[string][]string{
		questionID: {strings.TrimSpace(text)},
	}
	switch normalizeRequestType(request.RequestType) {
	case "request_user_input":
		return s.consumePendingRequestUserInputFreeText(surface, request, action, requestAnswers), true
	case "mcp_server_elicitation":
		return s.consumePendingMCPElicitationFreeText(surface, request, action, requestAnswers), true
	default:
		return nil, false
	}
}

func (s *Service) consumePendingStructuredFormFreeText(surface *state.SurfaceConsoleRecord, request *state.RequestPromptRecord, action control.Action, requestAnswers map[string][]string) []eventcontract.Event {
	updatedFields, errText := applyRequestStructuredFormAnswers(request, requestAnswers)
	if errText != "" {
		return notice(surface, "request_invalid", errText)
	}
	if len(updatedFields) == 0 {
		return notice(surface, "request_invalid", currentStructuredFormFieldPendingText(request))
	}
	if requestPromptStructuredFormComplete(request) {
		response, complete, errText := buildPlanConfirmationPermissionSelectionResponse(request, nil)
		if errText != "" {
			return notice(surface, "request_invalid", errText)
		}
		if complete {
			return s.dispatchRequestResponse(surface, request, action, response, "")
		}
	}
	bumpRequestCardRevision(request)
	setRequestPromptCurrentQuestionIndex(request, firstIncompleteStructuredFormFieldIndex(request))
	return []eventcontract.Event{s.requestPromptInlineEvent(surface, request, "")}
}

func (s *Service) consumePendingRequestUserInputFreeText(surface *state.SurfaceConsoleRecord, request *state.RequestPromptRecord, action control.Action, requestAnswers map[string][]string) []eventcontract.Event {
	response, complete, errText := buildRequestUserInputResponse(request, requestAnswers)
	if errText != "" {
		return notice(surface, "request_invalid", errText)
	}
	if !complete {
		bumpRequestCardRevision(request)
		setRequestPromptCurrentQuestionIndex(request, firstIncompleteRequestQuestionIndex(request))
		return []eventcontract.Event{s.requestPromptInlineEvent(surface, request, "")}
	}
	return s.dispatchRequestResponse(surface, request, action, response, "")
}

func (s *Service) consumePendingMCPElicitationFreeText(surface *state.SurfaceConsoleRecord, request *state.RequestPromptRecord, action control.Action, requestAnswers map[string][]string) []eventcontract.Event {
	content, complete, missingLabels, errText := buildMCPElicitationContent(request, requestAnswers)
	if errText != "" {
		return notice(surface, "request_invalid", errText)
	}
	if !complete {
		if len(missingLabels) == 1 {
			return s.refreshPendingRequestAfterFreeText(surface, request, fmt.Sprintf("字段“%s”还没有处理。你可以继续填写，或直接跳过。", missingLabels[0]))
		}
		return s.refreshPendingRequestAfterFreeText(surface, request, "")
	}
	return s.dispatchRequestResponse(surface, request, action, buildMCPElicitationPayload("accept", content, promptMCPElicitationMeta(request.Prompt, nil)), "")
}

func (s *Service) refreshPendingRequestAfterFreeText(surface *state.SurfaceConsoleRecord, request *state.RequestPromptRecord, message string) []eventcontract.Event {
	bumpRequestCardRevision(request)
	setRequestPromptCurrentQuestionIndex(request, firstIncompleteRequestQuestionIndex(request))
	events := []eventcontract.Event{s.requestPromptInlineEvent(surface, request, "")}
	if strings.TrimSpace(message) == "" {
		return events
	}
	events = append(events, eventcontract.Event{
		Kind:             eventcontract.KindNotice,
		SurfaceSessionID: surface.SurfaceSessionID,
		SourceMessageID:  strings.TrimSpace(request.SourceMessageID),
		Notice: &control.Notice{
			Code: "request_pending_input_saved",
			Text: strings.TrimSpace(message),
		},
	})
	return events
}

func surfaceSupportsPendingRequestFreeText(surface *state.SurfaceConsoleRecord) bool {
	if surface == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(surface.Platform), "wecom") {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(surface.GatewayID), "wecom:")
}

func requestQuestionAllowsFreeText(question state.RequestPromptQuestionRecord) bool {
	if len(question.Options) == 0 {
		return true
	}
	if question.AllowOther {
		return true
	}
	return !question.DirectResponse
}
