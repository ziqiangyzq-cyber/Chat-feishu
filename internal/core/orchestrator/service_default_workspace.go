package orchestrator

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type defaultWorkspaceBootstrapResult struct {
	Events       []eventcontract.Event
	WorkspaceKey string
	Attempted    bool
	Pending      bool
}

func (s *Service) maybeBootstrapDefaultWorkspaceForInput(surface *state.SurfaceConsoleRecord) defaultWorkspaceBootstrapResult {
	if surface == nil || !s.surfaceIsHeadless(surface) || !s.surfaceUsesWorkspaceClaims(surface) || strings.TrimSpace(surface.AttachedInstanceID) != "" {
		return defaultWorkspaceBootstrapResult{}
	}
	workspaceKey := s.surfaceDefaultWorkspaceRoot(surface)
	if workspaceKey == "" {
		return defaultWorkspaceBootstrapResult{}
	}
	result := defaultWorkspaceBootstrapResult{WorkspaceKey: workspaceKey}
	if pending := surface.PendingHeadless; pending != nil {
		if pending.PreserveQueuedInputs && normalizeWorkspaceClaimKey(firstNonEmpty(pending.WorkspaceKey, pending.ThreadCWD)) == workspaceKey {
			result.Attempted = true
			result.Pending = true
		}
		return result
	}

	result.Attempted = true
	backend := s.surfaceBackend(surface)
	continuation := s.buildHeadlessWorkspaceContinuation(surface, workspaceKey, backend, true)
	resolution := s.resolveWorkspaceContract(surface, workspaceKey, backend)
	result.Events = s.executeResolvedWorkspaceContinuation(surface, continuation, resolution, attachWorkspaceOptions{PrepareNewThread: true})
	if strings.TrimSpace(surface.AttachedInstanceID) != "" {
		return result
	}
	if pending := surface.PendingHeadless; pending != nil && normalizeWorkspaceClaimKey(firstNonEmpty(pending.WorkspaceKey, pending.ThreadCWD)) == workspaceKey {
		pending.PreserveQueuedInputs = true
		result.Pending = true
	}
	return result
}

func (s *Service) prepareNewThreadAfterDefaultWorkspaceBootstrap(surface *state.SurfaceConsoleRecord, inst *state.InstanceRecord, workspaceKey string) []eventcontract.Event {
	if surface == nil || inst == nil {
		return nil
	}
	workspaceKey = normalizeWorkspaceClaimKey(workspaceKey)
	if workspaceKey == "" {
		return notice(surface, "new_thread_cwd_missing", "当前默认工作区缺少可用目录，暂时无法开启新会话。")
	}
	if !s.transitionSurfaceRouteCore(surface, inst, surfaceRouteCoreState{
		AttachedInstanceID: inst.InstanceID,
		WorkspaceKey:       workspaceKey,
		RouteMode:          state.RouteModeNewThreadReady,
		PreparedThreadCWD:  workspaceKey,
	}) {
		return notice(surface, "workspace_instance_busy", "默认工作区实例暂时不可接管，请稍后重试。")
	}
	surface.PreparedAt = s.now()
	surface.LastSelection = &state.SelectionAnnouncementRecord{
		RouteMode: string(state.RouteModeNewThreadReady),
		Title:     preparedNewThreadSelectionTitle(),
	}
	return notice(surface, "default_workspace_ready", "已进入默认工作区，正在处理你的首条消息。")
}

func (s *Service) defaultWorkspaceBootstrapHasQueuedCreate(surface *state.SurfaceConsoleRecord) bool {
	if surface == nil {
		return false
	}
	for _, queueID := range surface.QueuedQueueItemIDs {
		item := surface.QueueItems[queueID]
		if item != nil && item.Status == state.QueueItemQueued && item.RouteModeAtEnqueue == state.RouteModeNewThreadReady {
			return true
		}
	}
	return false
}

func (s *Service) terminateDefaultWorkspaceBootstrap(surface *state.SurfaceConsoleRecord, pending *state.HeadlessLaunchRecord) []eventcontract.Event {
	if surface == nil || pending == nil || !pending.PreserveQueuedInputs {
		return nil
	}
	events := s.discardDrafts(surface)
	s.transitionSurfaceRouteCore(surface, nil, surfaceRouteCoreState{})
	return events
}

func (s *Service) enqueuePendingDefaultWorkspaceText(surface *state.SurfaceConsoleRecord, action control.Action, workspaceKey string) []eventcontract.Event {
	if surface == nil {
		return nil
	}
	if s.defaultWorkspaceBootstrapHasQueuedCreate(surface) {
		return notice(surface, "new_thread_first_input_pending", "当前新会话的首条消息已经在等待工作区启动；请等它开始处理后再继续发送。")
	}
	inputs, stagedMessageIDs, filePrompt := s.consumeStagedInputs(surface, action.ActorUserID)
	if filePrompt != "" {
		inputs = append(inputs, agentproto.Input{Type: agentproto.InputText, Text: filePrompt})
	}
	messageInputs := append([]agentproto.Input{}, action.Inputs...)
	if len(messageInputs) == 0 && strings.TrimSpace(action.Text) != "" {
		messageInputs = []agentproto.Input{{Type: agentproto.InputText, Text: strings.TrimSpace(action.Text)}}
	}
	inputs = append(inputs, messageInputs...)
	if len(inputs) == 0 {
		s.restoreStagedInputs(surface, stagedMessageIDs)
		return nil
	}
	return s.enqueueQueueItem(
		surface,
		action.MessageID,
		action.Text,
		stagedMessageIDs,
		inputs,
		"",
		workspaceKey,
		state.RouteModeNewThreadReady,
		surface.PromptOverride,
		false,
	)
}
