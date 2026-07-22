package daemon

import (
	"sort"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/core/surface"
)

func (a *App) runtimeStatusPayload() runtimeStatusPayload {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.syncManagedHeadlessLocked(time.Now().UTC())
	surfaces := a.service.Surfaces()
	instances := a.service.Instances()
	instanceStatuses := a.runtimeInstanceStatusesLocked(instances)
	gateways := gatewayStatuses(a.gateway)
	connectedGatewayCount, degradedGatewayCount, offlineGatewayCount := summarizeGatewayHealth(gateways)
	attachedSurfaceCount, queuedMessageCount, pendingRequestCount, redeliveryRequestCount := summarizeSurfaceHealth(surfaces)
	managedInstanceCount, onlineInstanceCount := summarizeInstanceHealth(instanceStatuses)
	deliverySuccessCount := a.opsRuntime.delivery.successCount
	deliveryFailureCount := a.opsRuntime.delivery.failureCount
	deliverySuccessRate := summarizeDeliverySuccessRate(deliverySuccessCount, deliveryFailureCount)
	return runtimeStatusPayload{
		Instances:              instances,
		Surfaces:               surfaces,
		InstanceStatuses:       instanceStatuses,
		SurfaceStatuses:        a.runtimeSurfaceStatusesLocked(surfaces),
		Gateways:               gateways,
		WeComBots:              a.runtimeWeComSummariesLocked(),
		RecentFailures:         runtimeFailureSummaries(a.opsRuntime.delivery.recent),
		PendingRemoteTurns:     a.service.PendingRemoteTurns(),
		ActiveRemoteTurns:      a.service.ActiveRemoteTurns(),
		ConnectedGatewayCount:  connectedGatewayCount,
		DegradedGatewayCount:   degradedGatewayCount,
		OfflineGatewayCount:    offlineGatewayCount,
		ManagedInstanceCount:   managedInstanceCount,
		OnlineInstanceCount:    onlineInstanceCount,
		AttachedSurfaceCount:   attachedSurfaceCount,
		QueuedMessageCount:     queuedMessageCount,
		PendingRequestCount:    pendingRequestCount,
		RedeliveryRequestCount: redeliveryRequestCount,
		DeliverySuccessCount:   deliverySuccessCount,
		DeliveryFailureCount:   deliveryFailureCount,
		DeliverySuccessRate:    deliverySuccessRate,
	}
}

func summarizeDeliverySuccessRate(successCount, failureCount int) float64 {
	total := successCount + failureCount
	if total <= 0 {
		return 0
	}
	return float64(successCount) / float64(total)
}

func runtimeFailureSummaries(records []deliveryFailureRecord) []adminRuntimeFailureSummary {
	if len(records) == 0 {
		return nil
	}
	summaries := make([]adminRuntimeFailureSummary, 0, len(records))
	for _, record := range records {
		summaries = append(summaries, adminRuntimeFailureSummary{
			OccurredAt:       record.OccurredAt,
			Channel:          record.Channel,
			GatewayID:        record.GatewayID,
			SurfaceSessionID: record.SurfaceSessionID,
			EventKind:        record.EventKind,
			Reason:           record.Reason,
		})
	}
	return summaries
}

func (a *App) runtimeWeComSummariesLocked() []adminWeComRuntimeSummary {
	if a == nil {
		return nil
	}
	if len(a.wecomChannels) == 0 && a.wecomChannel == nil {
		return []adminWeComRuntimeSummary{{
			GatewayID: wecomGatewayID,
			Name:      "WeCom Bot",
			Enabled:   false,
			State:     "disabled",
		}}
	}
	summaries := make([]adminWeComRuntimeSummary, 0, len(a.wecomChannels)+1)
	seen := map[string]bool{}
	appendSummary := func(gatewayID string, channel surface.Channel) {
		if seen[gatewayID] {
			return
		}
		seen[gatewayID] = true
		capabilities := surface.Capabilities{}
		if channel != nil {
			capabilities = channel.Capabilities()
		}
		runtimeState := a.wecomRuntimeStateLocked(gatewayID)
		summary := adminWeComRuntimeSummary{
			GatewayID:      gatewayID,
			Name:           adminWeComDisplayName(gatewayID),
			Enabled:        channel != nil,
			Connected:      runtimeState.connected,
			State:          strings.TrimSpace(runtimeState.state),
			LastError:      strings.TrimSpace(runtimeState.lastError),
			ReconnectTries: runtimeState.reconnectAttempts,
			Capabilities: surfaceCapabilitiesView{
				Streaming:            capabilities.Streaming,
				InteractiveSameFrame: capabilities.InteractiveSameFrame,
				FileSend:             capabilities.FileSend,
				MaxButtons:           capabilities.MaxButtons,
			},
		}
		if summary.State == "" {
			summary.State = "unknown"
		}
		if !runtimeState.lastConnectedAt.IsZero() {
			value := runtimeState.lastConnectedAt
			summary.LastConnectedAt = &value
		}
		if !runtimeState.lastStateChangeAt.IsZero() {
			value := runtimeState.lastStateChangeAt
			summary.LastStateChange = &value
		}
		if !runtimeState.nextRetryAt.IsZero() {
			value := runtimeState.nextRetryAt
			summary.NextRetryAt = &value
		}
		if runtimeState.lastRetryDelay > 0 {
			summary.LastRetryDelay = runtimeState.lastRetryDelay.String()
		}
		summaries = append(summaries, summary)
	}
	for gatewayID, channel := range a.wecomChannels {
		appendSummary(gatewayID, channel)
	}
	if a.wecomChannel != nil {
		appendSummary(wecomGatewayID, a.wecomChannel)
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Name != summaries[j].Name {
			return summaries[i].Name < summaries[j].Name
		}
		return summaries[i].GatewayID < summaries[j].GatewayID
	})
	return summaries
}

func adminWeComDisplayName(gatewayID string) string {
	value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(gatewayID), wecomNamespacePrefix))
	if value == "" {
		return "WeCom Bot"
	}
	return value
}

func (a *App) runtimeInstanceStatusesLocked(instances []*state.InstanceRecord) []adminInstanceSummary {
	summaries := make([]adminInstanceSummary, 0, len(instances)+len(a.managedHeadlessRuntime.Processes))
	seen := make(map[string]bool, len(instances)+len(a.managedHeadlessRuntime.Processes))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		summary, ok := a.adminManagedInstanceSummaryLocked(inst.InstanceID)
		if !ok {
			continue
		}
		summaries = append(summaries, summary)
		seen[inst.InstanceID] = true
	}
	for instanceID := range a.managedHeadlessRuntime.Processes {
		if seen[instanceID] {
			continue
		}
		summary, ok := a.adminManagedInstanceSummaryLocked(instanceID)
		if !ok {
			continue
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Online != summaries[j].Online {
			return summaries[i].Online
		}
		if summaries[i].Managed != summaries[j].Managed {
			return summaries[i].Managed
		}
		if summaries[i].DisplayName != summaries[j].DisplayName {
			return summaries[i].DisplayName < summaries[j].DisplayName
		}
		return summaries[i].InstanceID < summaries[j].InstanceID
	})
	return summaries
}

func summarizeGatewayHealth(gateways []feishu.GatewayStatus) (connected int, degraded int, offline int) {
	for _, gateway := range gateways {
		switch {
		case gateway.Disabled || gateway.State == feishu.GatewayStateDisabled:
			offline++
		case gateway.State == feishu.GatewayStateConnected:
			connected++
		default:
			degraded++
		}
	}
	return connected, degraded, offline
}

func summarizeSurfaceHealth(surfaces []*state.SurfaceConsoleRecord) (attached int, queued int, pendingRequests int, redelivery int) {
	for _, surface := range surfaces {
		if surface == nil {
			continue
		}
		if strings.TrimSpace(surface.AttachedInstanceID) != "" {
			attached++
		}
		queued += len(surface.QueuedQueueItemIDs)
		for requestID, request := range surface.PendingRequests {
			request = normalizeAdminPendingRequestOnSurface(surface, request)
			if request == nil {
				delete(surface.PendingRequests, requestID)
				continue
			}
			pendingRequests++
			if request.NeedsRedelivery || strings.TrimSpace(request.LastDeliveryError) != "" {
				redelivery++
			}
		}
	}
	return attached, queued, pendingRequests, redelivery
}

func summarizeInstanceHealth(instances []adminInstanceSummary) (managed int, online int) {
	for _, inst := range instances {
		if inst.Managed {
			managed++
		}
		if inst.Online {
			online++
		}
	}
	return managed, online
}
