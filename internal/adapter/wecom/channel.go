package wecom

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/surface"
)

// Channel is the WeCom implementation of surface.Channel. It owns a Client
// (the aibot long connection) and bridges channel-neutral events to WeCom
// outbound frames.
type Channel struct {
	client    *Client
	projector *Projector

	mu      sync.Mutex
	handler surface.ActionHandler
}

// Compile-time assertion that *Channel satisfies surface.Channel.
var _ surface.Channel = (*Channel)(nil)

// NewChannel constructs a WeCom Channel from the given aibot config.
func NewChannel(config Config) *Channel {
	return &Channel{client: NewClient(config), projector: NewProjector()}
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

	// Install inbound sinks that translate decoded WeCom frames into
	// channel-neutral control.Action values and dispatch them to the handler.
	c.client.onMessage = c.dispatchMessage
	c.client.onCardEvent = c.dispatchCardEvent

	if err := c.client.Dial(ctx); err != nil {
		return err
	}
	return c.client.Run(ctx)
}

// dispatchMessage maps an inbound user text message to a control.Action and
// forwards it to the retained handler.
func (c *Channel) dispatchMessage(ctx context.Context, frame msgCallbackFrame) {
	handler := c.currentHandler()
	if handler == nil {
		return
	}
	text := strings.TrimSpace(frame.Text.Content)
	if text == "" {
		return
	}
	action := control.Action{
		Kind:      control.ActionTextMessage,
		ChatID:    strings.TrimSpace(frame.ChatID),
		MessageID: strings.TrimSpace(frame.MsgID),
		Text:      text,
		Inputs:    []agentproto.Input{{Type: agentproto.InputText, Text: text}},
	}
	action.SteerInputs = append([]agentproto.Input(nil), action.Inputs...)
	handler(ctx, action)
}

// dispatchCardEvent maps an inbound template_card interaction to a
// control.Action and forwards it to the retained handler.
func (c *Channel) dispatchCardEvent(ctx context.Context, event InboundCardEvent) {
	handler := c.currentHandler()
	if handler == nil {
		return
	}
	action, ok := MapCardEventToAction(event)
	if !ok {
		return
	}
	handler(ctx, action)
}

// currentHandler returns the retained action handler under the lock.
func (c *Channel) currentHandler() surface.ActionHandler {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.handler
}

// Deliver renders an outbound event via the Projector and sends the resulting
// frames in order. An event that needs both a text body and interactive
// controls yields two frames (stream body first, then the interactive card);
// they are sent as separate WeCom messages because streaming text and
// interactive buttons cannot coexist in one message.
//
// Event kinds the Projector does not yet render (images, files, ...) produce no
// frames and are safely skipped. TODO(wecom Phase 3): render those kinds.
func (c *Channel) Deliver(_ context.Context, chatID string, event eventcontract.Event) error {
	if strings.TrimSpace(chatID) == "" {
		return errors.New("wecom: deliver requires a chatID")
	}
	for _, frame := range c.projector.ProjectEvent(event) {
		if err := c.client.sendFrame(chatID, frame); err != nil {
			return err
		}
	}
	return nil
}

// Stop tears down the long connection.
func (c *Channel) Stop(_ context.Context) error {
	return c.client.Close()
}
