package daemon

import (
	"sort"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
)

type adminSurfaceStatusSummary struct {
	SurfaceSessionID      string                      `json:"surfaceSessionId"`
	Platform              string                      `json:"platform,omitempty"`
	GatewayID             string                      `json:"gatewayId,omitempty"`
	ProductMode           string                      `json:"productMode,omitempty"`
	DisplayTitle          string                      `json:"displayTitle"`
	ThreadTitle           string                      `json:"threadTitle,omitempty"`
	FirstUserMessage      string                      `json:"firstUserMessage,omitempty"`
	LastUserMessage       string                      `json:"lastUserMessage,omitempty"`
	WorkspacePath         string                      `json:"workspacePath,omitempty"`
	InstanceID            string                      `json:"instanceId,omitempty"`
	InstanceDisplayName   string                      `json:"instanceDisplayName,omitempty"`
	OwnerSurface          bool                        `json:"ownerSurface"`
	SharedAttach          bool                        `json:"sharedAttach"`
	RouteMode             string                      `json:"routeMode,omitempty"`
	DispatchMode          string                      `json:"dispatchMode,omitempty"`
	ActiveItemStatus      string                      `json:"activeItemStatus,omitempty"`
	QueuedCount           int                         `json:"queuedCount"`
	HasPendingRequest     bool                        `json:"hasPendingRequest"`
	PendingRequestCount   int                         `json:"pendingRequestCount"`
	PendingRequest        *adminPendingRequestSummary `json:"pendingRequest,omitempty"`
	PendingRemoteTurn     bool                        `json:"pendingRemoteTurn"`
	ActiveRemoteTurn      bool                        `json:"activeRemoteTurn"`
	ReplyTargetMessageID  string                      `json:"replyTargetMessageId,omitempty"`
	NextThreadID          string                      `json:"nextThreadId,omitempty"`
	NextThreadTitle       string                      `json:"nextThreadTitle,omitempty"`
	LastDeliveryError     string                      `json:"lastDeliveryError,omitempty"`
	LastDeliveryAttemptAt *time.Time                  `json:"lastDeliveryAttemptAt,omitempty"`
	NeedsRedelivery       bool                        `json:"needsRedelivery"`
	DeliveryAttemptCount  int                         `json:"deliveryAttemptCount"`
	LastActiveAt          *time.Time                  `json:"lastActiveAt,omitempty"`
	PeerSurfaces          []adminPeerSurfaceSummary   `json:"peerSurfaces,omitempty"`
}

type adminPeerSurfaceSummary struct {
	SurfaceSessionID     string     `json:"surfaceSessionId"`
	Platform             string     `json:"platform,omitempty"`
	GatewayID            string     `json:"gatewayId,omitempty"`
	SharedAttach         bool       `json:"sharedAttach"`
	SelectedThreadID     string     `json:"selectedThreadId,omitempty"`
	RouteMode            string     `json:"routeMode,omitempty"`
	QueuedCount          int        `json:"queuedCount"`
	ActiveItemStatus     string     `json:"activeItemStatus,omitempty"`
	HasPendingRequest    bool       `json:"hasPendingRequest"`
	PendingRequestCount  int        `json:"pendingRequestCount"`
	PendingRemoteTurn    bool       `json:"pendingRemoteTurn"`
	ActiveRemoteTurn     bool       `json:"activeRemoteTurn"`
	ReplyTargetMessageID string     `json:"replyTargetMessageId,omitempty"`
	LastInboundAt        *time.Time `json:"lastInboundAt,omitempty"`
}

func (a *App) runtimeSurfaceStatusesLocked(surfaces []*state.SurfaceConsoleRecord) []adminSurfaceStatusSummary {
	summaries := make([]adminSurfaceStatusSummary, 0, len(surfaces))
	for _, surface := range surfaces {
		if surface == nil {
			continue
		}
		snapshot := a.service.SurfaceSnapshot(surface.SurfaceSessionID)
		summary := adminSurfaceStatusSummary{
			SurfaceSessionID: surface.SurfaceSessionID,
			Platform:         strings.TrimSpace(surface.Platform),
			GatewayID:        strings.TrimSpace(surface.GatewayID),
			ProductMode:      string(state.NormalizeProductMode(surface.ProductMode)),
			OwnerSurface:     !surface.SharedAttach,
			SharedAttach:     surface.SharedAttach,
		}
		if snapshot != nil {
			fillAdminSurfaceSummaryFromSnapshot(&summary, snapshot)
		}
		if summary.WorkspacePath == "" {
			summary.WorkspacePath = normalizeAdminSurfaceText(surface.ClaimedWorkspaceKey)
		}
		if summary.InstanceID == "" {
			summary.InstanceID = strings.TrimSpace(surface.AttachedInstanceID)
		}
		if !surface.LastInboundAt.IsZero() {
			lastActive := surface.LastInboundAt
			summary.LastActiveAt = &lastActive
		}
		fillAdminSurfaceDeliveryState(&summary, surface)
		if summary.PendingRequest == nil {
			summary.PendingRequest = summarizePendingRequest(activeAdminPendingRequest(surface))
		}
		summary.DisplayTitle = adminSurfaceDisplayTitle(summary)
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		leftAt := adminSurfaceLastActiveUnix(summaries[i])
		rightAt := adminSurfaceLastActiveUnix(summaries[j])
		if leftAt != rightAt {
			return leftAt > rightAt
		}
		if summaries[i].DisplayTitle != summaries[j].DisplayTitle {
			return summaries[i].DisplayTitle < summaries[j].DisplayTitle
		}
		return summaries[i].SurfaceSessionID < summaries[j].SurfaceSessionID
	})
	return summaries
}

func fillAdminSurfaceSummaryFromSnapshot(summary *adminSurfaceStatusSummary, snapshot *control.Snapshot) {
	if summary == nil || snapshot == nil {
		return
	}
	summary.ThreadTitle = normalizeAdminSurfaceText(snapshot.Attachment.SelectedThreadTitle)
	summary.FirstUserMessage = normalizeAdminSurfaceText(snapshot.Attachment.SelectedThreadFirstUserMessage)
	summary.LastUserMessage = normalizeAdminSurfaceText(snapshot.Attachment.SelectedThreadLastUserMessage)
	summary.WorkspacePath = normalizeAdminSurfaceText(snapshot.WorkspaceKey)
	summary.InstanceID = strings.TrimSpace(snapshot.Attachment.InstanceID)
	summary.InstanceDisplayName = normalizeAdminSurfaceText(snapshot.Attachment.DisplayName)
	summary.RouteMode = strings.TrimSpace(snapshot.Attachment.RouteMode)
	summary.DispatchMode = strings.TrimSpace(snapshot.Dispatch.DispatchMode)
	summary.ActiveItemStatus = strings.TrimSpace(snapshot.Dispatch.ActiveItemStatus)
	summary.QueuedCount = snapshot.Dispatch.QueuedCount
	summary.PendingRemoteTurn = snapshot.Dispatch.PendingRemoteTurn
	summary.ActiveRemoteTurn = snapshot.Dispatch.ActiveRemoteTurn
	summary.ReplyTargetMessageID = strings.TrimSpace(snapshot.Dispatch.ReplyTargetMessageID)
	summary.NextThreadID = strings.TrimSpace(snapshot.NextPrompt.ThreadID)
	summary.NextThreadTitle = normalizeAdminSurfaceText(snapshot.NextPrompt.ThreadTitle)
	summary.HasPendingRequest = snapshot.Gate.Kind == "pending_request"
	summary.PendingRequestCount = snapshot.Gate.PendingRequestCount
	summary.PeerSurfaces = buildAdminPeerSurfaceSummaries(snapshot.PeerSurfaces)
}

func buildAdminPeerSurfaceSummaries(peers []control.PeerSurfaceSummary) []adminPeerSurfaceSummary {
	if len(peers) == 0 {
		return nil
	}
	summaries := make([]adminPeerSurfaceSummary, 0, len(peers))
	for _, peer := range peers {
		summary := adminPeerSurfaceSummary{
			SurfaceSessionID:     strings.TrimSpace(peer.SurfaceSessionID),
			Platform:             strings.TrimSpace(peer.Platform),
			GatewayID:            strings.TrimSpace(peer.GatewayID),
			SharedAttach:         peer.SharedAttach,
			SelectedThreadID:     strings.TrimSpace(peer.SelectedThreadID),
			RouteMode:            strings.TrimSpace(peer.RouteMode),
			QueuedCount:          peer.QueuedCount,
			ActiveItemStatus:     strings.TrimSpace(peer.ActiveItemStatus),
			HasPendingRequest:    peer.HasPendingRequest,
			PendingRequestCount:  peer.PendingRequestCount,
			PendingRemoteTurn:    peer.PendingRemoteTurn,
			ActiveRemoteTurn:     peer.ActiveRemoteTurn,
			ReplyTargetMessageID: strings.TrimSpace(peer.ReplyTargetMessageID),
		}
		if !peer.LastInboundAt.IsZero() {
			lastInbound := peer.LastInboundAt
			summary.LastInboundAt = &lastInbound
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func fillAdminSurfaceDeliveryState(summary *adminSurfaceStatusSummary, surface *state.SurfaceConsoleRecord) {
	if summary == nil || surface == nil {
		return
	}
	if active := activeAdminPendingRequest(surface); active != nil {
		summary.PendingRequest = summarizePendingRequest(active)
	}
	for _, requestID := range surface.PendingRequestOrder {
		request := normalizeAdminPendingRequestOnSurface(surface, surface.PendingRequests[requestID])
		if request == nil {
			continue
		}
		if request.NeedsRedelivery || strings.TrimSpace(request.LastDeliveryError) != "" {
			summary.LastDeliveryError = normalizeAdminSurfaceText(request.LastDeliveryError)
			summary.NeedsRedelivery = request.NeedsRedelivery
			summary.DeliveryAttemptCount = request.DeliveryAttemptCount
			if !request.LastDeliveryAttemptAt.IsZero() {
				attemptedAt := request.LastDeliveryAttemptAt
				summary.LastDeliveryAttemptAt = &attemptedAt
			}
			return
		}
	}
	for _, request := range surface.PendingRequests {
		request = normalizeAdminPendingRequestOnSurface(surface, request)
		if request == nil {
			continue
		}
		if request.NeedsRedelivery || strings.TrimSpace(request.LastDeliveryError) != "" {
			summary.LastDeliveryError = normalizeAdminSurfaceText(request.LastDeliveryError)
			summary.NeedsRedelivery = request.NeedsRedelivery
			summary.DeliveryAttemptCount = request.DeliveryAttemptCount
			if !request.LastDeliveryAttemptAt.IsZero() {
				attemptedAt := request.LastDeliveryAttemptAt
				summary.LastDeliveryAttemptAt = &attemptedAt
			}
			return
		}
	}
}

func activeAdminPendingRequest(surface *state.SurfaceConsoleRecord) *state.RequestPromptRecord {
	if surface == nil || len(surface.PendingRequests) == 0 {
		return nil
	}
	for _, requestID := range surface.PendingRequestOrder {
		request := normalizeAdminPendingRequestOnSurface(surface, surface.PendingRequests[requestID])
		if request != nil {
			return request
		}
	}
	for _, request := range surface.PendingRequests {
		request = normalizeAdminPendingRequestOnSurface(surface, request)
		if request != nil {
			return request
		}
	}
	return nil
}

func normalizeAdminPendingRequestOnSurface(surface *state.SurfaceConsoleRecord, record *state.RequestPromptRecord) *state.RequestPromptRecord {
	if record == nil {
		return nil
	}
	if surface != nil {
		if strings.TrimSpace(record.OwnerSurfaceSessionID) == "" {
			record.OwnerSurfaceSessionID = strings.TrimSpace(surface.SurfaceSessionID)
		}
		if strings.TrimSpace(record.OwnerGatewayID) == "" {
			record.OwnerGatewayID = strings.TrimSpace(surface.GatewayID)
		}
		if strings.TrimSpace(record.OwnerChatID) == "" {
			record.OwnerChatID = strings.TrimSpace(surface.ChatID)
		}
	}
	if record.DeliveryAttemptCount < 0 {
		record.DeliveryAttemptCount = 0
	}
	return record
}

func normalizeAdminSurfaceText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func adminSurfaceDisplayTitle(summary adminSurfaceStatusSummary) string {
	for _, candidate := range []string{
		summary.ThreadTitle,
		summary.InstanceDisplayName,
		summary.WorkspacePath,
	} {
		if normalized := normalizeAdminSurfaceText(candidate); normalized != "" {
			return normalized
		}
	}
	return "未命名会话"
}

func adminSurfaceLastActiveUnix(summary adminSurfaceStatusSummary) int64 {
	if summary.LastActiveAt == nil {
		return 0
	}
	return summary.LastActiveAt.Unix()
}
