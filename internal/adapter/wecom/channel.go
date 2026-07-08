package wecom

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/surface"
)

// Channel is the WeCom implementation of surface.Channel. It owns a Client
// (the aibot long connection) and bridges channel-neutral events to WeCom
// outbound frames.
type Channel struct {
	client *Client

	mu      sync.Mutex
	handler surface.ActionHandler
}

// Compile-time assertion that *Channel satisfies surface.Channel.
var _ surface.Channel = (*Channel)(nil)

// NewChannel constructs a WeCom Channel from the given aibot config.
func NewChannel(config Config) *Channel {
	return &Channel{client: NewClient(config)}
}

// Name returns the stable channel identifier.
func (c *Channel) Name() string { return "wecom" }

// Capabilities reports the WeCom feature matrix. WeCom supports streamed text
// and file sends, but cannot combine streaming text with interactive buttons in
// a single message (InteractiveSameFrame=false). MaxButtons reflects the
// template_card button limit.
func (c *Channel) Capabilities() surface.Capabilities {
	return surface.Capabilities{
		Streaming:            true,
		InteractiveSameFrame: false,
		FileSend:             true,
		MaxButtons:           6,
	}
}

// Start dials the long connection, subscribes, and runs the read/ping loops
// until the context is cancelled or the connection fails. The handler is
// retained so inbound frames can be dispatched to it in a later phase.
func (c *Channel) Start(ctx context.Context, handler surface.ActionHandler) error {
	c.mu.Lock()
	c.handler = handler
	c.mu.Unlock()

	if err := c.client.Dial(ctx); err != nil {
		return err
	}
	// TODO(wecom Phase 2): thread c.handler into the Client so
	// handleMsgCallback / handleEventCallback can translate inbound frames into
	// control.Action and invoke the handler. Currently Run only drives the
	// read/ping scaffolding.
	return c.client.Run(ctx)
}

// Deliver renders an outbound event to the given chat. This Phase-1
// implementation handles only plain timeline text; richer event kinds (cards,
// images, files, streaming, interactive template_cards) are deferred.
func (c *Channel) Deliver(_ context.Context, chatID string, event eventcontract.Event) error {
	if strings.TrimSpace(chatID) == "" {
		return errors.New("wecom: deliver requires a chatID")
	}

	text := plainTextFromEvent(event)
	if text == "" {
		// TODO(wecom Phase 2): render non-text event kinds (Block, Notice,
		// ImageOutput, FileChangeSummary, target/selection views, ...) into the
		// appropriate WeCom message types. Nothing to send for now.
		return nil
	}

	return c.client.writeJSON(respondMsgFrame{
		Type:    frameTypeRespondMsg,
		ChatID:  chatID,
		MsgType: "text",
		Text:    &textBody{Content: text},
	})
}

// Stop tears down the long connection.
func (c *Channel) Stop(_ context.Context) error {
	return c.client.Close()
}

// plainTextFromEvent extracts a plain-text representation from an event, if one
// is readily available. Returns "" when the event carries no simple text.
func plainTextFromEvent(event eventcontract.Event) string {
	if event.TimelineText != nil {
		return strings.TrimSpace(event.TimelineText.Text)
	}
	// TODO(wecom Phase 2): extend extraction to Notice / Block / FinalTurnSummary
	// and other text-bearing event kinds.
	return ""
}
