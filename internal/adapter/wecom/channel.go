package wecom

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

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

	mu                  sync.Mutex
	handler             surface.ActionHandler
	responseReqIDByChat map[string]string
	recentNoticeByChat  map[string]time.Time
	now                 func() time.Time
}

// Compile-time assertion that *Channel satisfies surface.Channel.
var _ surface.Channel = (*Channel)(nil)

// NewChannel constructs a WeCom Channel from the given aibot config.
func NewChannel(config Config) *Channel {
	return &Channel{
		client:              NewClient(config),
		projector:           NewProjector(),
		responseReqIDByChat: make(map[string]string),
		recentNoticeByChat:  make(map[string]time.Time),
		now:                 time.Now,
	}
}

// Name returns the stable channel identifier.
func (c *Channel) Name() string { return "wecom" }

// Capabilities reports the WeCom feature matrix implemented by this adapter.
// Streaming/file support stay disabled until the transport emits the matching
// WeCom update/file frames, so upstream callers do not select unsupported paths.
func (c *Channel) Capabilities() surface.Capabilities {
	return surface.Capabilities{
		Streaming:            false,
		InteractiveSameFrame: false,
		FileSend:             false,
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
	chatID := wecomMessageChatID(frame)
	if chatID == "" {
		return
	}
	c.rememberResponseReqID(chatID, frame.Headers.ReqID)
	action := control.Action{
		Kind:        control.ActionTextMessage,
		ChatID:      chatID,
		ActorUserID: wecomMessageActorUserID(frame),
		MessageID:   strings.TrimSpace(frame.MsgID),
		Text:        text,
		Inputs:      []agentproto.Input{{Type: agentproto.InputText, Text: text}},
	}
	if command, ok := control.ParseFeishuTextActionWithoutCatalog(text); ok {
		command.ChatID = action.ChatID
		command.ActorUserID = action.ActorUserID
		command.MessageID = action.MessageID
		action = command
	}
	action.SteerInputs = append([]agentproto.Input(nil), action.Inputs...)
	handler(ctx, action)
}

func wecomMessageChatID(frame msgCallbackFrame) string {
	return firstNonEmpty(
		strings.TrimSpace(frame.ChatID),
		strings.TrimSpace(frame.From.UserID),
		strings.TrimSpace(frame.FromUserID),
		strings.TrimSpace(frame.UserID),
	)
}

func wecomMessageActorUserID(frame msgCallbackFrame) string {
	return firstNonEmpty(
		strings.TrimSpace(frame.From.UserID),
		strings.TrimSpace(frame.FromUserID),
		strings.TrimSpace(frame.UserID),
		strings.TrimSpace(frame.SenderUserID),
		strings.TrimSpace(frame.OperatorUserID),
	)
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
		log.Printf("wecom: ignored card event task=%q key=%q chat=%q operator=%q selections=%d", event.TaskID, event.EventKey, event.ChatID, event.OperatorUserID, len(event.Selections))
		return
	}
	log.Printf("wecom: mapped card event kind=%s picker=%q workspace=%q target=%q chat=%q", action.Kind, action.PickerID, action.WorkspaceKey, action.TargetPickerValue, action.ChatID)
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
func (c *Channel) Deliver(ctx context.Context, chatID string, event eventcontract.Event) error {
	if strings.TrimSpace(chatID) == "" {
		return errors.New("wecom: deliver requires a chatID")
	}
	if c.shouldSuppressNotice(chatID, event) {
		return nil
	}
	responseReqID := c.consumeResponseReqID(chatID)
	for _, frame := range c.projector.ProjectEvent(event) {
		if responseReqID != "" {
			if err := c.client.respondFrame(ctx, responseReqID, frame); err != nil {
				return err
			}
			continue
		}
		if err := c.client.sendFrame(ctx, chatID, frame); err != nil {
			return err
		}
	}
	return nil
}

func (c *Channel) rememberResponseReqID(chatID, reqID string) {
	reqID = strings.TrimSpace(reqID)
	if chatID == "" || reqID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.responseReqIDByChat[chatID] = reqID
}

func (c *Channel) responseReqID(chatID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.responseReqIDByChat[chatID]
}

func (c *Channel) consumeResponseReqID(chatID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	reqID := c.responseReqIDByChat[chatID]
	delete(c.responseReqIDByChat, chatID)
	return reqID
}

const noticeDedupeWindow = 30 * time.Second

func (c *Channel) shouldSuppressNotice(chatID string, event eventcontract.Event) bool {
	notice := event.Normalized().Notice
	if notice == nil {
		return false
	}
	key := wecomNoticeDedupeKey(chatID, *notice)
	if key == "" {
		return false
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for existing, seenAt := range c.recentNoticeByChat {
		if now.Sub(seenAt) > noticeDedupeWindow {
			delete(c.recentNoticeByChat, existing)
		}
	}
	if seenAt, ok := c.recentNoticeByChat[key]; ok && now.Sub(seenAt) <= noticeDedupeWindow {
		return true
	}
	c.recentNoticeByChat[key] = now
	return false
}

func wecomNoticeDedupeKey(chatID string, notice control.Notice) string {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return ""
	}
	parts := []string{
		chatID,
		strings.TrimSpace(notice.DeliveryDedupKey),
		strings.TrimSpace(notice.Code),
		strings.TrimSpace(notice.Title),
		strings.TrimSpace(notice.Text),
	}
	if parts[1] == "" && parts[2] == "" && parts[3] == "" && parts[4] == "" {
		return ""
	}
	return strings.Join(parts, "\x00")
}

// Stop tears down the long connection.
func (c *Channel) Stop(_ context.Context) error {
	return c.client.Close()
}
