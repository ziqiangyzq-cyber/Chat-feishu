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
	"github.com/kxn/codex-remote-feishu/internal/core/state"
	"github.com/kxn/codex-remote-feishu/internal/core/surface"
)

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
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return ""
	}
	return wecomGatewayID + ":chat:" + chatID
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
	a.wecomChannel = channel
}

// wecomInboundHandler returns the surface.ActionHandler wired to the WeCom
// channel's Start. It tags each inbound WeCom action with the WeCom gateway /
// surface namespace BEFORE the shared HandleGatewayAction runs, so the surface
// the orchestrator creates (and every event it later emits for that surface) is
// WeCom-namespaced and therefore routes back to the WeCom channel — never to
// Feishu. The action-result conversion reuses the existing Feishu wrapper; the
// only WeCom-specific step is the namespace tagging.
func (a *App) wecomInboundHandler() surface.ActionHandler {
	return feishu.WrapActionHandler(func(ctx context.Context, action control.Action) *feishu.ActionResult {
		return a.HandleGatewayAction(ctx, tagWeComInboundAction(action))
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
	runWeComChannelWithReconnect(ctx, channel, a.wecomInboundHandler(), time.Second, 30*time.Second)
}

func runWeComChannelWithReconnect(ctx context.Context, channel surface.Channel, handler surface.ActionHandler, baseDelay, maxDelay time.Duration) {
	if channel == nil {
		return
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
			return
		}
		err := channel.Start(ctx, handler)
		_ = channel.Stop(context.Background())
		if err == nil || errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return
		}
		log.Printf("wecom channel stopped: %v; reconnecting in %s", err, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
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
	if strings.TrimSpace(action.GatewayID) == "" {
		action.GatewayID = wecomGatewayID
	}
	if strings.TrimSpace(action.SurfaceSessionID) == "" {
		if surfaceID := wecomSurfaceID(action.ChatID); surfaceID != "" {
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
	channel := a.wecomChannel
	if channel == nil {
		return nil
	}
	if strings.TrimSpace(chatID) == "" {
		return nil
	}
	deliver := func() {
		if err := channel.Deliver(ctx, chatID, event); err != nil {
			log.Printf("wecom delivery failed (ignored): chat=%s kind=%s err=%v", chatID, event.Kind, err)
		}
	}
	if appLocked {
		a.mu.Unlock()
		deliver()
		a.mu.Lock()
	} else {
		deliver()
	}
	return nil
}
