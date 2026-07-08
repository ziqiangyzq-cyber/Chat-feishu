package feishu

import (
	"context"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/surface"
)

// SurfaceChannel adapts the existing Feishu Gateway + Projector pair to the
// channel-neutral surface.Channel contract. It introduces no new rendering or
// delivery behavior: Start delegates to Gateway.Start and Deliver is exactly
// Gateway.Apply(Projector.ProjectEvent(...)) — the same two-step render/apply
// the daemon already performs. The only translation is between the Feishu and
// surface ActionResult representations, which is lossless for Feishu-produced
// results (see surfaceActionResultToFeishu / feishuActionResultToSurface).
type SurfaceChannel struct {
	gw   Gateway
	proj *Projector
}

// Compile-time assertion that *SurfaceChannel satisfies surface.Channel.
var _ surface.Channel = (*SurfaceChannel)(nil)

// NewSurfaceChannel wraps a Gateway and Projector as a surface.Channel.
func NewSurfaceChannel(gw Gateway, proj *Projector) *SurfaceChannel {
	return &SurfaceChannel{gw: gw, proj: proj}
}

// Name returns the stable channel identifier.
func (c *SurfaceChannel) Name() string { return "feishu" }

// Capabilities reports the Feishu feature matrix. A Feishu card can stream
// markdown and host interactive action buttons in the same message
// (InteractiveSameFrame=true) and the gateway can send file attachments.
// MaxButtons is left unspecified (0) because Feishu cards impose no small,
// fixed per-card button cap comparable to WeCom's template_card limit.
func (c *SurfaceChannel) Capabilities() surface.Capabilities {
	return surface.Capabilities{
		Streaming:            true,
		InteractiveSameFrame: true,
		FileSend:             true,
		MaxButtons:           0,
	}
}

// Start begins receiving inbound actions via the underlying Gateway, converting
// the channel-neutral surface.ActionHandler into the Feishu-typed handler the
// Gateway expects. It blocks until the context is cancelled or Start returns.
func (c *SurfaceChannel) Start(ctx context.Context, handler surface.ActionHandler) error {
	return c.gw.Start(ctx, func(ctx context.Context, action control.Action) *ActionResult {
		if handler == nil {
			return nil
		}
		return surfaceActionResultToFeishu(handler(ctx, action))
	})
}

// Deliver renders and sends an outbound event to chatID. This is the exact
// render-then-apply the daemon's plain delivery path performs:
// Gateway.Apply(Projector.ProjectEvent(chatID, event)).
func (c *SurfaceChannel) Deliver(ctx context.Context, chatID string, event eventcontract.Event) error {
	return c.gw.Apply(ctx, c.proj.ProjectEvent(chatID, event))
}

// Stop releases channel resources. The Feishu Gateway is stopped by cancelling
// the context passed to Start (the daemon owns that lifecycle), so there is
// nothing additional to release here.
func (c *SurfaceChannel) Stop(context.Context) error { return nil }

// WrapActionHandler adapts a Feishu-typed ActionHandler into a channel-neutral
// surface.ActionHandler, converting the returned ActionResult. It lets the
// daemon keep its existing feishu.ActionHandler while driving the channel
// through the surface.Channel contract.
func WrapActionHandler(handler ActionHandler) surface.ActionHandler {
	if handler == nil {
		return nil
	}
	return func(ctx context.Context, action control.Action) *surface.ActionResult {
		return feishuActionResultToSurface(handler(ctx, action))
	}
}

// feishuActionResultToSurface converts a Feishu ActionResult into its surface
// equivalent. The nil-ness of the result and of ReplaceCurrentCard is preserved
// so the round-trip through the surface contract is lossless.
func feishuActionResultToSurface(result *ActionResult) *surface.ActionResult {
	if result == nil {
		return nil
	}
	out := &surface.ActionResult{}
	if result.ReplaceCurrentCard != nil {
		out.ReplaceCurrentCard = result.ReplaceCurrentCard
	}
	return out
}

// surfaceActionResultToFeishu converts a surface ActionResult back into a Feishu
// ActionResult. It reverses feishuActionResultToSurface: a *Operation carried
// opaquely as a surface.Operation is unwrapped back to *Operation.
func surfaceActionResultToFeishu(result *surface.ActionResult) *ActionResult {
	if result == nil {
		return nil
	}
	out := &ActionResult{}
	switch op := result.ReplaceCurrentCard.(type) {
	case *Operation:
		out.ReplaceCurrentCard = op
	case Operation:
		copied := op
		out.ReplaceCurrentCard = &copied
	}
	return out
}
