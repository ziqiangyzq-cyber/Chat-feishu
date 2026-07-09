package daemon

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/adapter/feishu"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/orchestrator"
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/core/surface"
)

type wecomStateHookSetter interface {
	SetStateHook(func(string, error))
}

type wecomRequestDeliveryReporter interface {
	LastDeliveryMessageID() string
}

// WeCom channel namespace.
//
// Feishu surfaces are keyed by a gateway id equal to the Feishu app id (an
// opaque token such as "cli_xxx") that the daemon itself assigns from Feishu
// config and NEVER prefixes with "wecom:". The WeCom bot is deliberately parked
// under a reserved gateway id built from wecomNamespacePrefix so a Feishu app id
// and the WeCom bot id can never collide. Channel routing (both outbound and
// inbound tagging) keys off this namespace, not off any per-channel struct.
const (
	// wecomNamespacePrefix marks a gateway/surface id as belonging to WeCom. The
	// colon guarantees separation from Feishu app ids.
	wecomNamespacePrefix = "wecom:"
	// wecomGatewayID is the single reserved gateway id under which every WeCom
	// surface lives. The daemon runs at most one WeCom bot, so one namespace id
	// is sufficient.
	wecomGatewayID = wecomNamespacePrefix + "bot"
)

func wecomGatewayIDForBot(botID string) string {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return wecomGatewayID
	}
	return wecomNamespacePrefix + botID
}

// isWeComGateway reports whether a resolved gateway id belongs to the WeCom
// namespace. It is the single routing predicate: false for every Feishu app id
// (which is never "wecom:"-prefixed), true only for surfaces the daemon itself
// tagged as WeCom. This is what keeps the Feishu delivery path byte-identical —
// every Feishu surface takes the false branch.
func isWeComGateway(gatewayID string) bool {
	return strings.HasPrefix(strings.TrimSpace(gatewayID), wecomNamespacePrefix)
}

// wecomSurfaceID derives the WeCom surface session id for a WeCom chat. The
// format mirrors the Feishu "<platform>:<gateway>:<scope>:<id>" shape but with
// a "wecom" platform token, so feishu.ParseSurfaceRef rejects it (parts[0] !=
// "feishu") and all Feishu-specific surface consumers skip it gracefully.
func wecomSurfaceID(chatID string) string {
	return wecomSurfaceIDForGateway(wecomGatewayID, chatID)
}

func wecomSurfaceIDForGateway(gatewayID, chatID string) string {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return ""
	}
	gatewayID = strings.TrimSpace(gatewayID)
	if gatewayID == "" {
		gatewayID = wecomGatewayID
	}
	return gatewayID + ":chat:" + chatID
}

// SetWeComChannel installs the OPTIONAL, opt-in WeCom (企业微信 aibot) second
// channel. It is called exactly once during startup (from entry.go) BEFORE
// Run, and only when WeCom credentials are configured. When it is never called,
// a.wecomChannel stays nil and every WeCom code path is a no-op branch, leaving
// the Feishu-only delivery path byte-identical.
//
// Passing a nil channel is treated as "not configured" and clears any prior
// value, so callers can guard purely on credential presence.
func (a *App) SetWeComChannel(channel surface.Channel) {
	a.SetWeComChannelWithGateway(wecomGatewayID, channel)
}

func (a *App) SetWeComChannelWithGateway(gatewayID string, channel surface.Channel) {
	gatewayID = strings.TrimSpace(gatewayID)
	if gatewayID == "" {
		gatewayID = wecomGatewayID
	}
	var cancel context.CancelFunc
	var done chan struct{}
	a.mu.Lock()
	if gatewayID == wecomGatewayID {
		a.wecomChannel = channel
	}
	if a.wecomChannels == nil {
		a.wecomChannels = map[string]surface.Channel{}
	}
	if a.wecomRunCancel == nil {
		a.wecomRunCancel = map[string]context.CancelFunc{}
	}
	if a.wecomRunDone == nil {
		a.wecomRunDone = map[string]chan struct{}{}
	}
	if channel == nil {
		cancel, done = a.detachWeComGatewayRuntimeLocked(gatewayID)
		delete(a.wecomChannels, gatewayID)
		a.setWeComDisabledLocked(gatewayID)
		a.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		a.waitWeComGatewayRuntimeStop(gatewayID, done)
		return
	}
	a.wecomChannels[gatewayID] = channel
	if hookable, ok := channel.(wecomStateHookSetter); ok {
		hookable.SetStateHook(func(state string, err error) {
			a.mu.Lock()
			defer a.mu.Unlock()
			switch strings.TrimSpace(state) {
			case "connecting":
				a.markWeComConnectingLocked(gatewayID)
			case "connected":
				a.markWeComConnectedLocked(gatewayID, time.Now().UTC())
				a.replayGatewayPendingRequestVisibilityLocked(context.Background(), gatewayID)
			case "degraded":
				a.markWeComDegradedLocked(gatewayID, daemonErrString(err))
			case "stopped":
				a.markWeComStoppedLocked(gatewayID, daemonErrString(err))
			}
		})
	}
	a.setWeComStateLocked(gatewayID, "stopped", time.Now().UTC())
	a.mu.Unlock()
}

func (a *App) attachWeComGatewayRuntimeLocked(gatewayID string, channel surface.Channel) {
	if a == nil || channel == nil || a.shuttingDown {
		return
	}
	gatewayCtx := a.gatewayRuntimeContext()
	if gatewayCtx == nil {
		return
	}
	if a.wecomRunCancel == nil {
		a.wecomRunCancel = map[string]context.CancelFunc{}
	}
	if a.wecomRunDone == nil {
		a.wecomRunDone = map[string]chan struct{}{}
	}
	if _, running := a.wecomRunCancel[gatewayID]; running {
		return
	}
	childCtx, childCancel := context.WithCancel(gatewayCtx)
	done := make(chan struct{})
	a.wecomRunCancel[gatewayID] = childCancel
	a.wecomRunDone[gatewayID] = done
	go func() {
		a.runWeComChannelWithGateway(childCtx, gatewayID, channel)
		close(done)
		a.mu.Lock()
		defer a.mu.Unlock()
		if currentDone, ok := a.wecomRunDone[gatewayID]; ok && currentDone == done {
			delete(a.wecomRunDone, gatewayID)
		}
		if _, ok := a.wecomRunCancel[gatewayID]; ok {
			delete(a.wecomRunCancel, gatewayID)
		}
	}()
}

func (a *App) detachWeComGatewayRuntimeLocked(gatewayID string) (context.CancelFunc, chan struct{}) {
	if a == nil {
		return nil, nil
	}
	cancel := a.wecomRunCancel[gatewayID]
	done := a.wecomRunDone[gatewayID]
	delete(a.wecomRunCancel, gatewayID)
	delete(a.wecomRunDone, gatewayID)
	return cancel, done
}

func (a *App) waitWeComGatewayRuntimeStop(gatewayID string, done chan struct{}) {
	if done == nil {
		return
	}
	timer := time.NewTimer(a.gatewayStopTimeoutValue())
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		log.Printf("wecom runtime stop exceeded timeout: gateway=%s timeout=%s", gatewayID, a.gatewayStopTimeoutValue())
	}
}

func (a *App) restartWeComGatewayRuntime(gatewayID string) {
	gatewayID = strings.TrimSpace(gatewayID)
	if gatewayID == "" {
		gatewayID = wecomGatewayID
	}
	a.mu.Lock()
	channel := a.wecomChannelForGatewayLocked(gatewayID)
	cancel, done := a.detachWeComGatewayRuntimeLocked(gatewayID)
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.waitWeComGatewayRuntimeStop(gatewayID, done)
	a.mu.Lock()
	defer a.mu.Unlock()
	channel = a.wecomChannelForGatewayLocked(gatewayID)
	if channel == nil {
		a.setWeComDisabledLocked(gatewayID)
		return
	}
	a.attachWeComGatewayRuntimeLocked(gatewayID, channel)
}

// wecomInboundHandler returns the surface.ActionHandler wired to the WeCom
// channel's Start. It tags each inbound WeCom action with the WeCom gateway /
// surface namespace BEFORE the shared HandleGatewayAction runs, so the surface
// the orchestrator creates (and every event it later emits for that surface) is
// WeCom-namespaced and therefore routes back to the WeCom channel — never to
// Feishu. The action-result conversion reuses the existing Feishu wrapper; the
// only WeCom-specific step is the namespace tagging.
func (a *App) wecomInboundHandler() surface.ActionHandler {
	return a.wecomInboundHandlerForGateway(wecomGatewayID)
}

func (a *App) wecomInboundHandlerForGateway(gatewayID string) surface.ActionHandler {
	return feishu.WrapActionHandler(func(ctx context.Context, action control.Action) *feishu.ActionResult {
		return a.HandleGatewayAction(ctx, tagWeComInboundActionForGateway(gatewayID, action))
	})
}

func (a *App) maybeAttachDefaultWeComWorkspaceLocked(ctx context.Context, action control.Action) {
	if a == nil || a.service == nil || !isWeComGateway(action.GatewayID) || action.Kind != control.ActionTextMessage {
		return
	}
	surfaceID := strings.TrimSpace(action.SurfaceSessionID)
	if surfaceID == "" {
		return
	}
	if surface := a.service.Surface(surfaceID); surface != nil {
		if strings.TrimSpace(surface.AttachedInstanceID) != "" ||
			strings.TrimSpace(surface.ClaimedWorkspaceKey) != "" ||
			surface.PendingHeadless != nil {
			return
		}
	}
	workspaceKey := a.defaultWeComWorkspaceKeyLocked()
	if workspaceKey == "" {
		return
	}
	if shared := a.sharedWeComAttachSurfaceLocked(workspaceKey); shared != nil {
		a.attachSharedWeComSurfaceLocked(ctx, action, shared)
		return
	}
	events := a.service.ApplySurfaceAction(control.Action{
		Kind:             control.ActionAttachWorkspace,
		GatewayID:        action.GatewayID,
		SurfaceSessionID: surfaceID,
		ChatID:           action.ChatID,
		ActorUserID:      action.ActorUserID,
		WorkspaceKey:     workspaceKey,
	})
	a.handleUIEventsLocked(ctx, events)
}

func (a *App) attachSharedWeComSurfaceLocked(ctx context.Context, action control.Action, owner *state.SurfaceConsoleRecord) {
	if a == nil || a.service == nil || owner == nil {
		return
	}
	surfaceID := strings.TrimSpace(action.SurfaceSessionID)
	workspaceKey := state.NormalizeWorkspaceKey(owner.ClaimedWorkspaceKey)
	instanceID := strings.TrimSpace(owner.AttachedInstanceID)
	if surfaceID == "" || workspaceKey == "" || instanceID == "" {
		return
	}
	inst := a.service.Instance(instanceID)
	if inst == nil || !inst.Online {
		return
	}
	a.service.MaterializeSurface(surfaceID, action.GatewayID, action.ChatID, action.ActorUserID)
	surface := a.service.Surface(surfaceID)
	if surface == nil {
		return
	}
	surface.SharedAttach = true
	surface.ClaimedWorkspaceKey = workspaceKey
	if !a.service.TransitionSurfaceToSharedHeadless(surfaceID, instanceID, workspaceKey) {
		surface.SharedAttach = false
		surface.ClaimedWorkspaceKey = ""
		events := a.service.ApplySurfaceAction(control.Action{
			Kind:             control.ActionAttachWorkspace,
			GatewayID:        action.GatewayID,
			SurfaceSessionID: surfaceID,
			ChatID:           action.ChatID,
			ActorUserID:      action.ActorUserID,
			WorkspaceKey:     workspaceKey,
		})
		a.handleUIEventsLocked(ctx, events)
		return
	}
	a.handleUIEventsLocked(ctx, []eventcontract.Event{{
		Kind:             eventcontract.KindNotice,
		GatewayID:        action.GatewayID,
		SurfaceSessionID: surfaceID,
		SourceMessageID:  strings.TrimSpace(action.MessageID),
		Notice: &control.Notice{
			Code: "workspace_attached_shared",
			Text: "已接入当前在线工作区，会继续复用现有会话上下文。",
		},
	}})
}

func (a *App) sharedWeComAttachSurfaceLocked(workspaceKey string) *state.SurfaceConsoleRecord {
	if a == nil || a.service == nil {
		return nil
	}
	workspaceKey = state.NormalizeWorkspaceKey(workspaceKey)
	if workspaceKey == "" {
		return nil
	}
	for _, surface := range a.service.Surfaces() {
		if surface == nil || isWeComGateway(surface.GatewayID) {
			continue
		}
		if strings.TrimSpace(surface.AttachedInstanceID) == "" {
			continue
		}
		if state.NormalizeWorkspaceKey(surface.ClaimedWorkspaceKey) != workspaceKey {
			continue
		}
		inst := a.service.Instance(surface.AttachedInstanceID)
		if inst == nil || !inst.Online {
			continue
		}
		return surface
	}
	return nil
}

func (a *App) defaultWeComWorkspaceKeyLocked() string {
	if a == nil || a.service == nil {
		return ""
	}
	home, _ := os.UserHomeDir()
	internalPrefix := state.NormalizeWorkspaceKey(home + "/.local/state/codex-remote")
	fallback := ""
	for _, inst := range a.service.Instances() {
		if inst == nil || !inst.Online {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(inst.Source), "vscode") {
			continue
		}
		workspaceKey := state.ResolveWorkspaceKey(inst.WorkspaceKey, inst.WorkspaceRoot)
		if workspaceKey == "" {
			continue
		}
		if internalPrefix != "" && strings.HasPrefix(workspaceKey, internalPrefix) {
			if fallback == "" {
				fallback = workspaceKey
			}
			continue
		}
		return workspaceKey
	}
	return fallback
}

func (a *App) runWeComChannel(ctx context.Context, channel surface.Channel) {
	a.runWeComChannelWithGateway(ctx, wecomGatewayID, channel)
}

func (a *App) runWeComChannelWithGateway(ctx context.Context, gatewayID string, channel surface.Channel) {
	runWeComChannelWithReconnect(ctx, gatewayID, channel, a.wecomInboundHandlerForGateway(gatewayID), time.Second, 30*time.Second, a)
}

func runWeComChannelWithReconnect(ctx context.Context, gatewayID string, channel surface.Channel, handler surface.ActionHandler, baseDelay, maxDelay time.Duration, app *App) {
	if channel == nil {
		return
	}
	gatewayID = strings.TrimSpace(gatewayID)
	if gatewayID == "" {
		gatewayID = wecomGatewayID
	}
	if baseDelay <= 0 {
		baseDelay = time.Second
	}
	if maxDelay < baseDelay {
		maxDelay = baseDelay
	}
	delay := baseDelay
	for {
		if ctx.Err() != nil {
			if app != nil {
				app.mu.Lock()
				app.markWeComStoppedLocked(gatewayID, "")
				app.mu.Unlock()
			}
			return
		}
		if app != nil {
			app.mu.Lock()
			app.markWeComConnectingLocked(gatewayID)
			app.mu.Unlock()
		}
		err := channel.Start(ctx, handler)
		_ = channel.Stop(context.Background())
		if err == nil || errors.Is(err, context.Canceled) || ctx.Err() != nil {
			if app != nil {
				app.mu.Lock()
				app.markWeComStoppedLocked(gatewayID, daemonErrString(err))
				app.mu.Unlock()
			}
			return
		}
		log.Printf("wecom channel stopped: %v; reconnecting in %s", err, delay)
		if app != nil {
			nextRetryAt := time.Now().UTC().Add(delay)
			app.mu.Lock()
			app.markWeComReconnectWaitingLocked(gatewayID, daemonErrString(err), delay, nextRetryAt)
			app.mu.Unlock()
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			if app != nil {
				app.mu.Lock()
				app.markWeComStoppedLocked(gatewayID, "")
				app.mu.Unlock()
			}
			return
		case <-timer.C:
		}
		if delay < maxDelay {
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}
}

// tagWeComInboundAction stamps a raw inbound WeCom action with the WeCom gateway
// id and a WeCom-namespaced surface session id derived from its chat id, unless
// they are already set. An action with no chat id is left untouched (it cannot
// address a surface); ensureSurface downstream ignores an empty surface id.
func tagWeComInboundAction(action control.Action) control.Action {
	return tagWeComInboundActionForGateway(wecomGatewayID, action)
}

func tagWeComInboundActionForGateway(gatewayID string, action control.Action) control.Action {
	gatewayID = strings.TrimSpace(gatewayID)
	if gatewayID == "" {
		gatewayID = wecomGatewayID
	}
	if strings.TrimSpace(action.GatewayID) == "" {
		action.GatewayID = gatewayID
	}
	if strings.TrimSpace(action.SurfaceSessionID) == "" {
		if surfaceID := wecomSurfaceIDForGateway(gatewayID, action.ChatID); surfaceID != "" {
			action.SurfaceSessionID = surfaceID
		}
	}
	return action
}

// deliverWeComEventLocked delivers a channel-neutral event to the WeCom channel
// for a WeCom-namespaced surface. It is reached ONLY from the WeCom routing
// branch in deliverUIEventWithContextMode, so it never touches the Feishu path.
//
// Delivery I/O runs off the app lock (mirroring the Feishu gateway.Apply
// unlock/relock) so it cannot block other handlers. A WeCom delivery error is
// logged and swallowed rather than returned: the caller's failure path
// (queueGatewayFailureNotice) is Feishu-specific and would mis-render a WeCom
// surface, so a soft failure is the correct, safe outcome for an independent
// WeCom session. When no WeCom channel is configured the event is dropped
// safely (a WeCom-namespaced surface can only exist if a WeCom channel was
// running, but this stays defensive).
func (a *App) deliverWeComEventLocked(ctx context.Context, chatID string, event eventcontract.Event, appLocked bool) error {
	gatewayID := strings.TrimSpace(event.GatewayID)
	if gatewayID == "" {
		gatewayID = strings.TrimSpace(a.service.SurfaceGatewayID(event.SurfaceSessionID))
	}
	if gatewayID == "" {
		gatewayID = wecomGatewayID
	}
	channel := a.wecomChannelForGatewayLocked(gatewayID)
	if channel == nil {
		return nil
	}
	if strings.TrimSpace(chatID) == "" {
		return nil
	}
	reporter, _ := channel.(wecomRequestDeliveryReporter)
	var deliverErr error
	deliver := func() {
		deliverErr = channel.Deliver(ctx, chatID, event)
	}
	if appLocked {
		a.mu.Unlock()
		deliver()
		a.mu.Lock()
	} else {
		deliver()
	}
	if deliverErr != nil {
		if appLocked {
			a.recordDeliveryFailureLocked("wecom", gatewayID, event.SurfaceSessionID, string(event.Kind), deliverErr)
		} else {
			a.mu.Lock()
			a.recordDeliveryFailureLocked("wecom", gatewayID, event.SurfaceSessionID, string(event.Kind), deliverErr)
			a.mu.Unlock()
		}
		log.Printf("wecom delivery failed (ignored): chat=%s kind=%s err=%v", chatID, event.Kind, deliverErr)
		return nil
	}
	if appLocked {
		a.recordDeliverySuccessLocked("wecom", gatewayID)
	} else {
		a.mu.Lock()
		a.recordDeliverySuccessLocked("wecom", gatewayID)
		a.mu.Unlock()
	}
	a.recordWeComEventDelivery(event, reporter)
	return nil
}

func (a *App) replayGatewayPendingRequestVisibilityLocked(ctx context.Context, gatewayID string) {
	if a == nil || a.service == nil {
		return
	}
	gatewayID = strings.TrimSpace(gatewayID)
	if gatewayID == "" {
		return
	}
	for _, surface := range a.service.Surfaces() {
		if surface == nil || strings.TrimSpace(surface.GatewayID) != gatewayID {
			continue
		}
		surfaceID := strings.TrimSpace(surface.SurfaceSessionID)
		if surfaceID == "" {
			continue
		}
		events := a.service.ReplayActivePendingRequestVisibility(surfaceID)
		if len(events) == 0 {
			continue
		}
		a.handleUIEventsLocked(ctx, events)
	}
}

func (a *App) recordWeComEventDelivery(event eventcontract.Event, reporter wecomRequestDeliveryReporter) {
	if a == nil || a.service == nil {
		return
	}
	messageID := ""
	if reporter != nil {
		messageID = strings.TrimSpace(reporter.LastDeliveryMessageID())
	}
	if payload, ok := requestPayloadFromEvent(event); ok {
		if messageID != "" {
			a.service.RecordRequestPromptDelivery(orchestrator.RequestDeliveryReport{
				SurfaceSessionID: event.SurfaceSessionID,
				RequestID:        strings.TrimSpace(payload.View.RequestID),
				MessageID:        messageID,
				DeliveredAt:      time.Now(),
			})
		}
	}
	if messageID != "" {
		a.recordSurfaceOutboundMessageFallback(event, messageID)
	}
}

func (a *App) recordSurfaceOutboundMessageFallback(event eventcontract.Event, messageID string) {
	if a == nil || a.service == nil {
		return
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return
	}
	switch {
	case event.Block != nil:
		a.service.RecordSurfaceOutboundMessage(event.SurfaceSessionID, messageID, state.SurfaceMessageKindText, "")
	case event.RequestView != nil, event.Notice != nil, event.PageView != nil, event.PlanUpdate != nil, event.ExecCommandProgress != nil:
		a.service.RecordSurfaceOutboundMessage(event.SurfaceSessionID, messageID, state.SurfaceMessageKindCard, "")
	}
}

func (a *App) wecomChannelForGatewayLocked(gatewayID string) surface.Channel {
	if a == nil {
		return nil
	}
	gatewayID = strings.TrimSpace(gatewayID)
	if gatewayID == "" || gatewayID == wecomGatewayID {
		if channel, ok := a.wecomChannels[wecomGatewayID]; ok && channel != nil {
			return channel
		}
		return a.wecomChannel
	}
	if channel, ok := a.wecomChannels[gatewayID]; ok {
		return channel
	}
	return nil
}
