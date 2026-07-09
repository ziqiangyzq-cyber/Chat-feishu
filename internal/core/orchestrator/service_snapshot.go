package orchestrator

import (
	"sort"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

func (s *Service) buildSnapshot(surface *state.SurfaceConsoleRecord) *control.Snapshot {
	snapshot := &control.Snapshot{
		SurfaceSessionID: surface.SurfaceSessionID,
		ActorUserID:      surface.ActorUserID,
		ProductMode:      string(s.normalizeSurfaceProductMode(surface)),
		Backend:          s.surfaceBackend(surface),
		WorkspaceKey:     s.surfaceCurrentWorkspaceKey(surface),
		CodexProviderID:  s.surfaceCodexProviderID(surface),
		Surface: control.SurfaceSummary{
			Platform:      strings.TrimSpace(surface.Platform),
			GatewayID:     strings.TrimSpace(surface.GatewayID),
			ChatID:        strings.TrimSpace(surface.ChatID),
			SharedAttach:  surface.SharedAttach,
			LastInboundAt: surface.LastInboundAt,
		},
		AutoWhip:         snapshotAutoWhipSummary(surface),
		AutoContinue:     snapshotAutoContinueSummary(surface),
	}
	if snapshot.Backend == agentproto.BackendClaude && state.IsHeadlessProductMode(s.normalizeSurfaceProductMode(surface)) {
		snapshot.ClaudeProfileID = s.surfaceClaudeProfileID(surface)
		snapshot.ClaudeProfileName = s.claudeProfileDisplayName(snapshot.ClaudeProfileID)
	}
	snapshot.Gate = s.snapshotGateSummary(surface)
	if pending := surface.PendingHeadless; pending != nil {
		snapshot.PendingHeadless = control.PendingHeadlessSummary{
			InstanceID:            pending.InstanceID,
			ThreadID:              pending.ThreadID,
			ThreadTitle:           pending.ThreadTitle,
			WorkspaceKey:          pending.WorkspaceKey,
			ThreadCWD:             pending.ThreadCWD,
			Backend:               pending.Backend,
			CodexProviderID:       pending.CodexProviderID,
			ClaudeProfileID:       pending.ClaudeProfileID,
			ClaudeReasoningEffort: pending.ClaudeReasoningEffort,
			Status:                string(pending.Status),
			PID:                   pending.PID,
			ExpiresAt:             pending.ExpiresAt,
			RequestedAt:           pending.RequestedAt,
		}
	}
	if inst := s.root.Instances[surface.AttachedInstanceID]; inst != nil {
		selected := inst.Threads[surface.SelectedThreadID]
		if !threadVisible(selected) {
			selected = nil
		}
		selectedTitle := ""
		selectedFirstUserMessage := ""
		selectedLastUserMessage := ""
		selectedModelReroute := (*agentproto.TurnModelReroute)(nil)
		selectedAgeText := ""
		if selected != nil {
			selectedTitle = displayThreadTitle(inst, selected)
			selectedFirstUserMessage = threadFirstUserSnippet(selected, 64)
			selectedLastUserMessage = threadLastUserSnippet(selected, 64)
			selectedModelReroute = agentproto.CloneTurnModelReroute(selected.LastModelReroute)
			selectedAgeText = humanizeRelativeTime(s.now(), selected.LastUsedAt)
		}
		snapshot.Attachment = control.AttachmentSummary{
			InstanceID:                     inst.InstanceID,
			ObjectType:                     snapshotAttachmentObjectType(s.normalizeSurfaceProductMode(surface), inst),
			DisplayName:                    inst.DisplayName,
			Source:                         inst.Source,
			Managed:                        inst.Managed,
			PID:                            inst.PID,
			SelectedThreadID:               surface.SelectedThreadID,
			SelectedThreadTitle:            selectedTitle,
			SelectedThreadFirstUserMessage: selectedFirstUserMessage,
			SelectedThreadLastUserMessage:  selectedLastUserMessage,
			SelectedThreadModelReroute:     selectedModelReroute,
			SelectedThreadAgeText:          selectedAgeText,
			RouteMode:                      string(surface.RouteMode),
			Abandoning:                     surface.Abandoning,
		}
		snapshot.Dispatch = s.snapshotDispatchSummary(surface, inst)
		snapshot.PeerSurfaces = s.snapshotPeerSurfaces(surface, inst.InstanceID)
		snapshot.NextPrompt = s.resolveNextPromptSummary(inst, surface, "", "", state.ModelConfigRecord{})
	}

	for _, inst := range s.root.Instances {
		snapshot.Instances = append(snapshot.Instances, control.InstanceSummary{
			InstanceID:              inst.InstanceID,
			DisplayName:             inst.DisplayName,
			WorkspaceRoot:           inst.WorkspaceRoot,
			WorkspaceKey:            inst.WorkspaceKey,
			Source:                  inst.Source,
			Managed:                 inst.Managed,
			PID:                     inst.PID,
			Online:                  inst.Online,
			State:                   threadStateForInstance(inst),
			ObservedFocusedThreadID: inst.ObservedFocusedThreadID,
		})
		if inst.InstanceID != surface.AttachedInstanceID {
			continue
		}
		for _, thread := range visibleThreads(inst) {
			snapshot.Threads = append(snapshot.Threads, control.ThreadSummary{
				ThreadID:           thread.ThreadID,
				Name:               thread.Name,
				DisplayTitle:       displayThreadTitle(inst, thread),
				Preview:            thread.Preview,
				CWD:                thread.CWD,
				State:              threadProjectedState(thread),
				RuntimeStatus:      threadRuntimeStatusType(thread),
				Model:              thread.ExplicitModel,
				ReasoningEffort:    thread.ExplicitReasoningEffort,
				LastModelReroute:   agentproto.CloneTurnModelReroute(thread.LastModelReroute),
				Loaded:             thread.Loaded,
				WaitingOnApproval:  threadWaitingOnApproval(thread),
				WaitingOnUserInput: threadWaitingOnUserInput(thread),
				IsObservedFocused:  inst.ObservedFocusedThreadID == thread.ThreadID,
				IsSelected:         surface.SelectedThreadID == thread.ThreadID,
			})
		}
	}
	sort.Slice(snapshot.Instances, func(i, j int) bool {
		return snapshot.Instances[i].WorkspaceKey < snapshot.Instances[j].WorkspaceKey
	})
	return snapshot
}

func snapshotAttachmentObjectType(mode state.ProductMode, inst *state.InstanceRecord) string {
	switch {
	case inst == nil:
		return ""
	case state.IsHeadlessProductMode(mode):
		return "workspace"
	case isVSCodeInstance(inst):
		return "vscode_instance"
	case isHeadlessInstance(inst):
		return "headless_instance"
	default:
		return "instance"
	}
}

func snapshotAutoWhipSummary(surface *state.SurfaceConsoleRecord) control.AutoWhipSummary {
	if surface == nil {
		return control.AutoWhipSummary{}
	}
	return control.AutoWhipSummary{
		Enabled:             surface.AutoWhip.Enabled,
		PendingReason:       string(surface.AutoWhip.PendingReason),
		PendingDueAt:        surface.AutoWhip.PendingDueAt,
		ConsecutiveCount:    surface.AutoWhip.ConsecutiveCount,
		LastTriggeredTurnID: surface.AutoWhip.LastTriggeredTurnID,
	}
}

func snapshotAutoContinueSummary(surface *state.SurfaceConsoleRecord) control.AutoContinueSummary {
	if surface == nil {
		return control.AutoContinueSummary{}
	}
	summary := control.AutoContinueSummary{
		Enabled: surface.AutoContinue.Enabled,
	}
	if episode := activeAutoContinueEpisode(surface); episode != nil {
		summary.State = string(episode.State)
		summary.PendingDueAt = episode.PendingDueAt
		summary.AttemptCount = episode.AttemptCount
		summary.ConsecutiveDryFailureCount = episode.ConsecutiveDryFailureCount
		summary.TriggerKind = string(episode.TriggerKind)
	}
	return summary
}

func (s *Service) snapshotGateSummary(surface *state.SurfaceConsoleRecord) control.GateSummary {
	if surface == nil {
		return control.GateSummary{}
	}
	if surface.ActiveRequestCapture != nil {
		return control.GateSummary{Kind: "request_capture"}
	}
	if s.targetPickerHasBlockingProcessing(surface) {
		return control.GateSummary{Kind: "target_picker"}
	}
	if s.activePathPicker(surface) != nil {
		return control.GateSummary{Kind: "path_picker"}
	}
	count := snapshotPendingRequestCount(surface)
	if count != 0 {
		summary := control.GateSummary{Kind: "pending_request", PendingRequestCount: count}
		if active := activePendingRequest(surface); active != nil {
			summary.PendingRequestLifecycle = normalizeRequestLifecycleState(active.LifecycleState)
			summary.PendingRequestVisibility = normalizeRequestVisibilityState(active.VisibilityState)
		}
		return summary
	}
	return control.GateSummary{}
}

func (s *Service) snapshotDispatchSummary(surface *state.SurfaceConsoleRecord, inst *state.InstanceRecord) control.DispatchSummary {
	if surface == nil {
		return control.DispatchSummary{}
	}
	summary := control.DispatchSummary{
		DispatchMode:      string(surface.DispatchMode),
		QueuedCount:       len(surface.QueuedQueueItemIDs),
		ActiveQueueItemID: strings.TrimSpace(surface.ActiveQueueItemID),
	}
	if inst != nil {
		summary.InstanceOnline = inst.Online
		if binding := s.pendingRemoteTurnBindingForSurface(inst.InstanceID, surface); binding != nil {
			summary.PendingRemoteTurn = true
			summary.PendingRemoteTurnID = strings.TrimSpace(binding.TurnID)
			summary.ActiveSourceMessageID = strings.TrimSpace(binding.SourceMessageID)
			summary.ReplySourceMessageID = strings.TrimSpace(binding.SourceMessageID)
			summary.ReplyTargetMessageID = strings.TrimSpace(firstNonEmpty(binding.ReplyToMessageID, binding.SourceMessageID))
		}
		if binding := s.activeRemoteTurnBindingForSurface(inst.InstanceID, surface); binding != nil {
			summary.ActiveRemoteTurn = true
			summary.ActiveRemoteTurnID = strings.TrimSpace(binding.TurnID)
			if summary.ActiveSourceMessageID == "" {
				summary.ActiveSourceMessageID = strings.TrimSpace(binding.SourceMessageID)
			}
			if summary.ReplySourceMessageID == "" {
				summary.ReplySourceMessageID = strings.TrimSpace(binding.SourceMessageID)
			}
			if summary.ReplyTargetMessageID == "" {
				summary.ReplyTargetMessageID = strings.TrimSpace(firstNonEmpty(binding.ReplyToMessageID, binding.SourceMessageID))
			}
		}
	}
	if surface.ActiveQueueItemID == "" {
		return summary
	}
	item := surface.QueueItems[surface.ActiveQueueItemID]
	if item == nil {
		return summary
	}
	summary.ActiveItemStatus = string(item.Status)
	if summary.ActiveSourceMessageID == "" {
		summary.ActiveSourceMessageID = strings.TrimSpace(item.SourceMessageID)
	}
	if summary.ReplySourceMessageID == "" {
		summary.ReplySourceMessageID = strings.TrimSpace(item.SourceMessageID)
	}
	if summary.ReplyTargetMessageID == "" {
		summary.ReplyTargetMessageID = strings.TrimSpace(firstNonEmpty(item.ReplyToMessageID, item.SourceMessageID))
	}
	return summary
}

func (s *Service) snapshotPeerSurfaces(current *state.SurfaceConsoleRecord, instanceID string) []control.PeerSurfaceSummary {
	if s == nil || current == nil || strings.TrimSpace(instanceID) == "" {
		return nil
	}
	surfaces := s.findAttachedSurfaces(instanceID)
	if len(surfaces) <= 1 {
		return nil
	}
	summaries := make([]control.PeerSurfaceSummary, 0, len(surfaces)-1)
	for _, peer := range surfaces {
		if peer == nil || peer.SurfaceSessionID == current.SurfaceSessionID {
			continue
		}
		summary := control.PeerSurfaceSummary{
			SurfaceSessionID: strings.TrimSpace(peer.SurfaceSessionID),
			Platform:         strings.TrimSpace(peer.Platform),
			GatewayID:        strings.TrimSpace(peer.GatewayID),
			ChatID:           strings.TrimSpace(peer.ChatID),
			ActorUserID:      strings.TrimSpace(peer.ActorUserID),
			SharedAttach:     peer.SharedAttach,
			IsCurrent:        peer.SurfaceSessionID == current.SurfaceSessionID,
			SelectedThreadID: strings.TrimSpace(peer.SelectedThreadID),
			RouteMode:        strings.TrimSpace(string(peer.RouteMode)),
			QueuedCount:      len(peer.QueuedQueueItemIDs),
			LastInboundAt:    peer.LastInboundAt,
		}
		if peer.ActiveQueueItemID != "" {
			if item := peer.QueueItems[peer.ActiveQueueItemID]; item != nil {
				summary.ActiveItemStatus = string(item.Status)
				summary.SourceMessageID = strings.TrimSpace(item.SourceMessageID)
				summary.ReplyTargetMessageID = strings.TrimSpace(firstNonEmpty(item.ReplyToMessageID, item.SourceMessageID))
			}
		}
		if active := activePendingRequest(peer); active != nil {
			summary.HasPendingRequest = true
			summary.PendingRequestCount = snapshotPendingRequestCount(peer)
			summary.PendingRequestLifecycle = normalizeRequestLifecycleState(active.LifecycleState)
			summary.PendingRequestVisibility = normalizeRequestVisibilityState(active.VisibilityState)
		}
		if binding := s.pendingRemoteTurnBindingForSurface(instanceID, peer); binding != nil {
			summary.PendingRemoteTurn = true
			if summary.SourceMessageID == "" {
				summary.SourceMessageID = strings.TrimSpace(binding.SourceMessageID)
			}
			if summary.ReplyTargetMessageID == "" {
				summary.ReplyTargetMessageID = strings.TrimSpace(firstNonEmpty(binding.ReplyToMessageID, binding.SourceMessageID))
			}
		}
		if binding := s.activeRemoteTurnBindingForSurface(instanceID, peer); binding != nil {
			summary.ActiveRemoteTurn = true
			if summary.SourceMessageID == "" {
				summary.SourceMessageID = strings.TrimSpace(binding.SourceMessageID)
			}
			if summary.ReplyTargetMessageID == "" {
				summary.ReplyTargetMessageID = strings.TrimSpace(firstNonEmpty(binding.ReplyToMessageID, binding.SourceMessageID))
			}
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].SharedAttach != summaries[j].SharedAttach {
			return !summaries[i].SharedAttach
		}
		if summaries[i].Platform != summaries[j].Platform {
			return summaries[i].Platform < summaries[j].Platform
		}
		return summaries[i].SurfaceSessionID < summaries[j].SurfaceSessionID
	})
	return summaries
}

func snapshotPendingRequestCount(surface *state.SurfaceConsoleRecord) int {
	if surface == nil {
		return 0
	}
	count := 0
	for requestID, request := range surface.PendingRequests {
		request = normalizePendingRequestOnSurface(surface, request)
		if request == nil {
			removePendingRequest(surface, requestID)
			continue
		}
		count++
	}
	return count
}

func (s *Service) pendingRemoteTurnBindingForSurface(instanceID string, surface *state.SurfaceConsoleRecord) *remoteTurnBinding {
	if s == nil || surface == nil {
		return nil
	}
	return s.matchingRemoteTurnBindingForSurface(instanceID, surface, s.turns.pendingRemote[instanceID], true)
}

func (s *Service) activeRemoteTurnBindingForSurface(instanceID string, surface *state.SurfaceConsoleRecord) *remoteTurnBinding {
	if s == nil || surface == nil {
		return nil
	}
	return s.matchingRemoteTurnBindingForSurface(instanceID, surface, s.turns.activeRemote[instanceID], false)
}

func (s *Service) matchingRemoteTurnBindingForSurface(instanceID string, surface *state.SurfaceConsoleRecord, binding *remoteTurnBinding, pending bool) *remoteTurnBinding {
	if s == nil || surface == nil {
		return nil
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return nil
	}
	if binding == nil || strings.TrimSpace(binding.InstanceID) != instanceID {
		return nil
	}
	if strings.TrimSpace(binding.SurfaceSessionID) != strings.TrimSpace(surface.SurfaceSessionID) {
		return nil
	}
	queueItemID := strings.TrimSpace(binding.QueueItemID)
	if queueItemID == "" {
		return binding
	}
	item := surface.QueueItems[queueItemID]
	if item == nil {
		return nil
	}
	switch item.Status {
	case state.QueueItemDispatching:
		if pending {
			return binding
		}
	case state.QueueItemRunning:
		if !pending {
			return binding
		}
	}
	if strings.TrimSpace(surface.ActiveQueueItemID) == queueItemID {
		return binding
	}
	for _, queuedID := range surface.QueuedQueueItemIDs {
		if strings.TrimSpace(queuedID) == queueItemID {
			return binding
		}
	}
	if pending {
		if current, _, currentItem := s.pendingRemoteBindingRecord(instanceID); current == binding && currentItem != nil {
			return binding
		}
		return nil
	}
	if current := s.activeRemoteBinding(instanceID, strings.TrimSpace(binding.TurnID)); current == binding {
		return binding
	}
	return nil
}
