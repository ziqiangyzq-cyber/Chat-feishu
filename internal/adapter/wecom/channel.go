package wecom

import (
	"context"
	"errors"
	"log"
	"sort"
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
	config    Config

	mu                   sync.Mutex
	handler              surface.ActionHandler
	responseReqByChat    map[string]responseReqBinding
	recentNoticeByChat   map[string]time.Time
	recentCardEventByKey map[string]time.Time
	lastActivityByChat   map[string]time.Time
	now                  func() time.Time

	// inbound serialises handler execution on a dedicated worker goroutine so it
	// never runs on the read loop. A handler that delivers synchronously ends up
	// in writeEnvelopeAndWait, which blocks until the server's ack is read — and
	// that ack can only be read by the read loop. Running the handler on the read
	// goroutine therefore self-deadlocks the connection (observed: config cards
	// like /mode replied once, then all later inbound messages sat unread in the
	// socket). inboundEnd is closed when the current connection tears down.
	inbound    chan func()
	inboundEnd chan struct{}
}

type responseReqBinding struct {
	ReqID           string
	SourceMessageID string
	BoundAt         time.Time
}

// Compile-time assertion that *Channel satisfies surface.Channel.
var _ surface.Channel = (*Channel)(nil)

// Compile-time assertion that *Channel satisfies surface.StreamingRenderer.
var _ surface.StreamingRenderer = (*Channel)(nil)

const (
	defaultSessionIdle = 30 * time.Minute
	// Core slash commands expected to behave the same on Feishu and WeCom.
	// Full catalog still routes through control.ParseFeishuTextActionWithoutCatalog.
	wecomCoreCommandHelp = "" +
		"**常用命令**（飞书 / 企业微信通用）\n" +
		"- `/stop` — 中断当前执行\n" +
		"- `/status` — 查看当前工作区与会话\n" +
		"- `/new` — 新开会话\n" +
		"- `/compact` — 压缩上下文\n" +
		"- `/help` — 完整命令帮助\n" +
		"- `/use` — 选择工作区 / 会话\n" +
		"- `/sendfile` — 发送工作区文件（企微侧以路径提示为主）"
)

// NewChannel constructs a WeCom Channel from the given aibot config.
func NewChannel(config Config) *Channel {
	if config.SessionIdle <= 0 {
		config.SessionIdle = defaultSessionIdle
	}
	return &Channel{
		client:               NewClient(config),
		projector:            NewProjector(),
		config:               config,
		responseReqByChat:    make(map[string]responseReqBinding),
		recentNoticeByChat:   make(map[string]time.Time),
		recentCardEventByKey: make(map[string]time.Time),
		lastActivityByChat:   make(map[string]time.Time),
		now:                  time.Now,
	}
}

// Name returns the stable channel identifier.
func (c *Channel) Name() string { return "wecom" }

func (c *Channel) SetStateHook(hook func(string, error)) {
	if c == nil || c.client == nil {
		return
	}
	c.client.SetStateHook(hook)
}

// Capabilities reports the WeCom feature matrix implemented by this adapter.
// Streaming uses aibot_respond_msg + aibot_respond_update_msg. FileSend is
// backed by the media upload API (uploadMedia); image outputs upload for real
// and only degrade to a markdown path notice when the upload fails.
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
// retained so inbound frames can be dispatched to it.
func (c *Channel) Start(ctx context.Context, handler surface.ActionHandler) error {
	inbound := make(chan func(), 128)
	inboundEnd := make(chan struct{})
	c.mu.Lock()
	c.handler = handler
	c.inbound = inbound
	c.inboundEnd = inboundEnd
	c.mu.Unlock()

	// Install inbound sinks that translate decoded WeCom frames into
	// channel-neutral control.Action values and dispatch them to the handler.
	c.client.onMessage = c.dispatchMessage
	c.client.onCardEvent = c.dispatchCardEvent

	// Run handlers off the read loop (see the inbound field comment). The worker
	// processes actions in arrival order; the read loop stays free to read the
	// acks that unblock synchronous deliveries.
	var workerWG sync.WaitGroup
	workerWG.Add(1)
	go func() {
		defer workerWG.Done()
		for {
			select {
			case <-inboundEnd:
				return
			case job := <-inbound:
				func() {
					// A handler panic must not kill the worker (and with it every
					// later inbound message on this connection).
					defer func() {
						if recovered := recover(); recovered != nil {
							log.Printf("wecom: inbound handler panic: %v", recovered)
						}
					}()
					job()
				}()
			}
		}
	}()

	var runErr error
	if err := c.client.Dial(ctx); err != nil {
		runErr = err
	} else {
		// Idle-state reaping runs only while a connection is live. The derived
		// context ends with this Start call, so the reconnect loop in the daemon
		// cannot stack one maintenance goroutine per redial.
		maintenanceCtx, cancelMaintenance := context.WithCancel(ctx)
		go c.maintenanceLoop(maintenanceCtx)
		runErr = c.client.Run(ctx)
		cancelMaintenance()
	}

	// The read loop has stopped, so no more jobs will be enqueued; tear the
	// worker down. Draining is unnecessary — the connection is gone.
	c.mu.Lock()
	c.inbound = nil
	c.inboundEnd = nil
	c.mu.Unlock()
	close(inboundEnd)
	workerWG.Wait()
	return runErr
}

// enqueueInbound hands a handler invocation to the worker goroutine. It must
// never run the job on the calling (read-loop) goroutine during a live
// connection, or a synchronous delivery would deadlock the reader on its own
// ack. The inline fallback only fires when no worker is active (outside a live
// connection), where the deadlock cannot occur.
func (c *Channel) enqueueInbound(job func()) {
	c.mu.Lock()
	inbound := c.inbound
	inboundEnd := c.inboundEnd
	c.mu.Unlock()
	if inbound == nil {
		job()
		return
	}
	select {
	case inbound <- job:
	case <-inboundEnd:
	}
}

// dispatchMessage maps an inbound user text message to a control.Action and
// forwards it to the retained handler. Handler work runs in a new goroutine so
// a slow orchestrator cannot stall the WebSocket read loop.
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
	c.touchActivity(chatID)
	msgID := strings.TrimSpace(frame.MsgID)
	reqID := frame.Headers.ReqID
	action := control.Action{
		Kind:        control.ActionTextMessage,
		ChatID:      chatID,
		ActorUserID: wecomMessageActorUserID(frame),
		MessageID:   msgID,
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
	// Remember the response req on the worker, immediately before the handler
	// runs, so the remember→handle→deliver sequence stays serialized per message.
	// Doing it here (on the read loop) would let a later message's req overwrite
	// an earlier one before the earlier one is delivered.
	c.enqueueInbound(func() {
		c.rememberResponseReq(chatID, msgID, reqID)
		// Lightweight local help so WeCom users always see the shared command list
		// even before the orchestrator catalog round-trip returns.
		if action.Kind == control.ActionShowCommandHelp {
			_ = c.client.sendFrame(ctx, chatID, markdownFrame(wecomCoreCommandHelp))
		}
		handler(ctx, action)
	})
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
	if c.shouldSuppressCardEvent(event) {
		return
	}
	if chatID := strings.TrimSpace(action.ChatID); chatID != "" {
		c.touchActivity(chatID)
	}
	log.Printf("wecom: mapped card event kind=%s picker=%q workspace=%q target=%q chat=%q", action.Kind, action.PickerID, action.WorkspaceKey, action.TargetPickerValue, action.ChatID)
	c.enqueueInbound(func() { handler(ctx, action) })
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
// Non-final assistant blocks are pushed through the streaming transport so the
// user sees progressive markdown updates; final blocks finish the stream.
func (c *Channel) Deliver(ctx context.Context, chatID string, event eventcontract.Event) error {
	if strings.TrimSpace(chatID) == "" {
		return errors.New("wecom: deliver requires a chatID")
	}
	c.touchActivity(chatID)
	if c.shouldSuppressNotice(chatID, event) {
		return nil
	}
	event = event.Normalized()

	// Progressive block streaming (assistant partial → final).
	if payload, ok := event.CanonicalPayload().(eventcontract.BlockCommittedPayload); ok {
		text := strings.TrimSpace(payload.Block.Text)
		if text != "" && !payload.Block.Final {
			return c.client.streamMarkdown(ctx, chatID, text, false)
		}
		if text != "" && payload.Block.Final {
			// Prefer stream finish when a stream is already open; otherwise fall
			// through to normal one-shot delivery (with optional respond req).
			if _, open := c.client.streamAge(chatID, c.now()); open {
				body := text
				// projectBlock applies fencing for final code blocks.
				if frames := c.projector.ProjectEvent(event); len(frames) == 1 {
					if md := frameMarkdownContent(frames[0]); md != "" {
						body = md
					}
				}
				return c.client.streamMarkdown(ctx, chatID, body, true)
			}
		}
	}

	frames := c.projector.ProjectEvent(event)
	if len(frames) == 0 {
		return nil
	}
	response := c.consumeResponseReq(chatID, event.SourceMessageID)
	if response.ReqID == "" && eventPrefersInboundReqReply(event.Kind) {
		// Command config cards (e.g. /mode, /model) are emitted without a
		// SourceMessageID, so the exact-match consume above misses. WeCom aibot
		// only reliably delivers replies tied to the inbound webhook req, not
		// standalone aibot_send_msg pushes — so fall back to the freshest
		// remembered req for this chat to answer the just-received command.
		response = c.forceConsumeResponseReq(chatID)
	}
	for _, frame := range frames {
		if strings.TrimSpace(frame.LocalPath) != "" && (frame.MsgType == "image" || frame.MsgType == "file") {
			mediaType := mediaTypeFile
			if frame.MsgType == "image" {
				mediaType = mediaTypeImage
			}
			mediaID, _, err := c.client.uploadMedia(ctx, frame.LocalPath, mediaType)
			if err != nil {
				return err
			}
			frame = mediaFrame(mediaType, mediaID)
		}
		if response.ReqID != "" {
			log.Printf("wecom: respond frame chat=%s req=%s source=%s kind=%s msgtype=%s", chatID, response.ReqID, response.SourceMessageID, event.Kind, frame.MsgType)
			if err := c.client.respondFrame(ctx, response.ReqID, frame); err != nil {
				return err
			}
			// One-shot req_id is consumed after the first frame.
			response = responseReqBinding{}
			continue
		}
		log.Printf("wecom: send frame chat=%s kind=%s msgtype=%s", chatID, event.Kind, frame.MsgType)
		if err := c.client.sendFrame(ctx, chatID, frame); err != nil {
			return err
		}
	}
	return nil
}

// RenderStream implements surface.StreamingRenderer.
func (c *Channel) RenderStream(ctx context.Context, chatID string, markdown string, finish bool) error {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return errors.New("wecom: render stream requires chatID")
	}
	c.touchActivity(chatID)
	return c.client.streamMarkdown(ctx, chatID, markdown, finish)
}

func frameMarkdownContent(frame Frame) string {
	if frame.Markdown != nil {
		return strings.TrimSpace(frame.Markdown.Content)
	}
	if frame.Text != nil {
		return strings.TrimSpace(frame.Text.Content)
	}
	return ""
}

func (c *Channel) touchActivity(chatID string) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return
	}
	c.mu.Lock()
	c.lastActivityByChat[chatID] = c.now()
	c.mu.Unlock()
}

func (c *Channel) maintenanceLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.reapIdleState(ctx)
		}
	}
}

func (c *Channel) reapIdleState(ctx context.Context) {
	now := c.now()
	idle := c.config.SessionIdle
	maxTurn := c.config.MaxTurn

	// Finalize streams that exceeded MaxTurn.
	if maxTurn > 0 {
		for _, chatID := range c.client.activeStreamChats(now, maxTurn) {
			log.Printf("wecom: max turn exceeded chat=%s; finishing stream", chatID)
			_ = c.client.streamMarkdown(ctx, chatID, "⚠️ 本轮执行超过时间上限，已停止流式更新。可用 `/status` 查看状态，或重新发送指令继续。", true)
		}
	}

	if idle <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for chatID, seen := range c.lastActivityByChat {
		if now.Sub(seen) <= idle {
			continue
		}
		delete(c.lastActivityByChat, chatID)
		delete(c.responseReqByChat, chatID)
		c.client.dropStream(chatID)
	}
	for chatID, binding := range c.responseReqByChat {
		if !binding.BoundAt.IsZero() && now.Sub(binding.BoundAt) > idle {
			delete(c.responseReqByChat, chatID)
		}
	}
}

func (c *Channel) rememberResponseReq(chatID, sourceMessageID, reqID string) {
	reqID = strings.TrimSpace(reqID)
	sourceMessageID = strings.TrimSpace(sourceMessageID)
	if chatID == "" || reqID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.responseReqByChat[chatID] = responseReqBinding{
		ReqID:           reqID,
		SourceMessageID: sourceMessageID,
		BoundAt:         c.now(),
	}
}

func (c *Channel) responseReqID(chatID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.responseReqByChat[chatID].ReqID
}

func (c *Channel) consumeResponseReq(chatID, sourceMessageID string) responseReqBinding {
	c.mu.Lock()
	defer c.mu.Unlock()
	binding := c.responseReqByChat[chatID]
	if binding.ReqID == "" {
		return responseReqBinding{}
	}
	sourceMessageID = strings.TrimSpace(sourceMessageID)
	if binding.SourceMessageID != "" {
		if sourceMessageID == "" || binding.SourceMessageID != sourceMessageID {
			return responseReqBinding{}
		}
	}
	delete(c.responseReqByChat, chatID)
	return binding
}

func (c *Channel) consumeResponseReqID(chatID string) string {
	return c.consumeResponseReq(chatID, "").ReqID
}

// forceConsumeResponseReq returns and clears the freshest remembered req for a
// chat, ignoring source-message matching. Used only for command config cards
// that carry no SourceMessageID but are the synchronous reply to the inbound
// command, which WeCom aibot can only deliver via the inbound req.
func (c *Channel) forceConsumeResponseReq(chatID string) responseReqBinding {
	c.mu.Lock()
	defer c.mu.Unlock()
	binding := c.responseReqByChat[chatID]
	if binding.ReqID == "" {
		return responseReqBinding{}
	}
	delete(c.responseReqByChat, chatID)
	return binding
}

// eventPrefersInboundReqReply reports whether an event must be delivered through
// the inbound webhook req rather than a standalone aibot_send_msg push. Command
// config/menu pages (/mode, /model, ...) are emitted without a SourceMessageID
// yet are a direct reply to the command that triggered them.
func eventPrefersInboundReqReply(kind eventcontract.Kind) bool {
	return kind == eventcontract.KindPage
}

func (c *Channel) LastDeliveryMessageID() string {
	if c == nil || c.client == nil {
		return ""
	}
	messageID, err := c.client.lastAckMessageID()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(messageID)
}

const noticeDedupeWindow = 30 * time.Second
const cardEventDedupeWindow = 15 * time.Second

func (c *Channel) shouldSuppressCardEvent(event InboundCardEvent) bool {
	key := wecomCardEventDedupeKey(event)
	if key == "" {
		return false
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for existing, seenAt := range c.recentCardEventByKey {
		if now.Sub(seenAt) > cardEventDedupeWindow {
			delete(c.recentCardEventByKey, existing)
		}
	}
	if seenAt, ok := c.recentCardEventByKey[key]; ok && now.Sub(seenAt) <= cardEventDedupeWindow {
		log.Printf("wecom: suppressed duplicate card event task=%q key=%q chat=%q", strings.TrimSpace(event.TaskID), strings.TrimSpace(event.EventKey), strings.TrimSpace(event.ChatID))
		return true
	}
	c.recentCardEventByKey[key] = now
	return false
}

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

func wecomCardEventDedupeKey(event InboundCardEvent) string {
	chatID := strings.TrimSpace(event.ChatID)
	taskID := strings.TrimSpace(event.TaskID)
	eventKey := strings.TrimSpace(event.EventKey)
	if chatID == "" || taskID == "" || eventKey == "" {
		return ""
	}
	parts := []string{
		chatID,
		strings.TrimSpace(event.OperatorUserID),
		strings.TrimSpace(event.MessageID),
		taskID,
		eventKey,
	}
	selections := make([]string, 0, len(event.Selections))
	for _, selection := range event.Selections {
		questionKey := strings.TrimSpace(selection.QuestionKey)
		optionIDs := make([]string, 0, len(selection.OptionIDs))
		for _, optionID := range selection.OptionIDs {
			if optionID = strings.TrimSpace(optionID); optionID != "" {
				optionIDs = append(optionIDs, optionID)
			}
		}
		sort.Strings(optionIDs)
		selections = append(selections, questionKey+"="+strings.Join(optionIDs, ","))
	}
	sort.Strings(selections)
	parts = append(parts, selections...)
	return strings.Join(parts, "\x00")
}

// Stop tears down the long connection.
func (c *Channel) Stop(_ context.Context) error {
	return c.client.Close()
}
