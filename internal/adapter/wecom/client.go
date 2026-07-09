package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsEndpoint is the WeCom aibot long-connection WebSocket endpoint.
const wsEndpoint = "wss://openws.work.weixin.qq.com"

// pingInterval is the keepalive cadence for the long connection.
const pingInterval = 30 * time.Second

// writeTimeout bounds a single frame write.
const writeTimeout = 10 * time.Second

// Frame type discriminators exchanged over the aibot long connection.
const (
	frameCmdSubscribe        = "aibot_subscribe"
	frameCmdMsgCallback      = "aibot_msg_callback"
	frameCmdEventCallback    = "aibot_event_callback"
	frameCmdRespondMsg       = "aibot_respond_msg"
	frameCmdSendMsg          = "aibot_send_msg"
	frameCmdRespondUpdateMsg = "aibot_respond_update_msg"
)

// frameEnvelope is the common shape used to peek at an inbound frame's command
// before decoding it into a more specific struct.
type frameEnvelope struct {
	Cmd     string         `json:"cmd"`
	Headers frameHeaders   `json:"headers,omitempty"`
	ErrCode int            `json:"errcode,omitempty"`
	ErrMsg  string         `json:"errmsg,omitempty"`
	Body    map[string]any `json:"body,omitempty"`
}

type frameHeaders struct {
	ReqID string `json:"req_id,omitempty"`
}

// subscribeFrame registers this aibot for its event stream. It is the first
// frame sent after a successful dial.
type subscribeFrame struct {
	Cmd     string       `json:"cmd"`
	Headers frameHeaders `json:"headers,omitempty"`
	Body    struct {
		BotID  string `json:"bot_id"`
		Secret string `json:"secret"`
	} `json:"body"`
}

// msgCallbackFrame is an inbound user message pushed by the server.
type msgCallbackFrame struct {
	Cmd      string       `json:"cmd,omitempty"`
	Headers  frameHeaders `json:"headers,omitempty"`
	BotID    string       `json:"aibotid"`
	ChatID   string       `json:"chatid"`
	ChatType string       `json:"chattype"`
	MsgID    string       `json:"msgid"`
	MsgType  string       `json:"msgtype"`
	From     struct {
		UserID string `json:"userid"`
	} `json:"from"`
	FromUserID     string `json:"from_userid"`
	UserID         string `json:"userid"`
	SenderUserID   string `json:"sender_userid"`
	OperatorUserID string `json:"operator_userid"`
	Text           struct {
		Content string `json:"content"`
	} `json:"text"`
}

// streamMeta carries the streaming identity shared across a sequence of
// respond/update frames. finish marks the terminal update.
type streamMeta struct {
	ID     string `json:"id"`
	Finish bool   `json:"finish"`
}

// respondMsgFrame is a new outbound message. Exactly one of Text / Markdown /
// TemplateCard is populated, selected by MsgType.
type respondMsgFrame struct {
	Cmd     string       `json:"cmd"`
	Headers frameHeaders `json:"headers,omitempty"`
	Body    struct {
		ChatID       string        `json:"chatid,omitempty"`
		ChatType     int           `json:"chat_type,omitempty"`
		MsgType      string        `json:"msgtype"`
		Text         *textBody     `json:"text,omitempty"`
		Markdown     *markdownBody `json:"markdown,omitempty"`
		TemplateCard *templateCard `json:"template_card,omitempty"`
		Stream       *streamMeta   `json:"stream,omitempty"`
	} `json:"body"`
}

// respondUpdateMsgFrame updates a previously sent message, used for streaming
// (successive frames share Stream.ID; the terminal frame sets Stream.Finish).
type respondUpdateMsgFrame struct {
	Cmd     string       `json:"cmd"`
	Headers frameHeaders `json:"headers,omitempty"`
	Body    struct {
		ChatID  string      `json:"chatid"`
		MsgID   string      `json:"msgid"`
		MsgType string      `json:"msgtype"`
		Text    *textBody   `json:"text,omitempty"`
		Stream  *streamMeta `json:"stream,omitempty"`
	} `json:"body"`
}

// textBody is the payload for a plain-text message.
type textBody struct {
	Content string `json:"content"`
}

// Client wraps a gorilla/websocket connection to the WeCom aibot long
// connection. It owns the dial, subscribe, read-loop and ping scaffolding.
//
// Inbound frames are decoded here (transport concern) and handed to the
// injected sinks; the Channel installs sinks that translate them into
// channel-neutral control.Action values. This keeps the wire/transport layer
// free of control-plane knowledge.
type Client struct {
	config Config

	// dialFn is injectable for testing; defaults to dialDefault.
	dialFn func(ctx context.Context) (*websocket.Conn, error)

	// onMessage receives decoded inbound user messages.
	onMessage func(ctx context.Context, frame msgCallbackFrame)
	// onCardEvent receives decoded inbound template_card interactions.
	onCardEvent func(ctx context.Context, event InboundCardEvent)

	mu      sync.Mutex
	conn    *websocket.Conn
	writeMu sync.Mutex
}

// NewClient constructs a Client for the given aibot credentials.
func NewClient(config Config) *Client {
	c := &Client{config: config}
	c.dialFn = c.dialDefault
	return c
}

// dialDefault establishes the raw WebSocket connection to the aibot endpoint.
func (c *Client) dialDefault(ctx context.Context) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("wecom: dial %s: %w", wsEndpoint, err)
	}
	return conn, nil
}

// Dial opens the long connection and sends the initial subscribe frame.
func (c *Client) Dial(ctx context.Context) error {
	conn, err := c.dialFn(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	if err := c.subscribe(ctx); err != nil {
		_ = c.Close()
		return err
	}
	return nil
}

// subscribe sends the aibot_subscribe frame registering this bot's stream.
func (c *Client) subscribe(ctx context.Context) error {
	if c.config.BotID == "" || c.config.Secret == "" {
		return errors.New("wecom: subscribe requires BotID and Secret")
	}
	return c.writeJSON(ctx, newSubscribeFrame(c.config))
}

// currentConn returns the live connection or an error if not dialed.
func (c *Client) currentConn() (*websocket.Conn, error) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil, errors.New("wecom: not connected")
	}
	return conn, nil
}

// writeJSON serializes v and writes it as a text frame under the write lock.
func (c *Client) writeJSON(ctx context.Context, v any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	conn, err := c.currentConn()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("wecom: marshal frame: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	deadline := time.Now().Add(writeTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}

// Run drives the read loop and ping loop until the context is cancelled or the
// connection fails. It assumes Dial has already succeeded.
func (c *Client) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.pingLoop(ctx)
	}()

	err := c.readLoop(ctx)
	cancel()
	wg.Wait()
	return err
}

// readLoop reads frames and dispatches them by type until an error occurs or
// the context is cancelled.
func (c *Client) readLoop(ctx context.Context) error {
	conn, err := c.currentConn()
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("wecom: read: %w", err)
		}
		if err := c.dispatch(ctx, raw); err != nil {
			log.Printf("wecom: dispatch error: %v", err)
		}
	}
}

// dispatch decodes a raw inbound frame and routes it to the appropriate handler
// based on its cmd discriminator.
func (c *Client) dispatch(ctx context.Context, raw []byte) error {
	var env frameEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("wecom: decode frame envelope: %w", err)
	}
	switch env.Cmd {
	case frameCmdMsgCallback:
		var wire struct {
			Cmd     string           `json:"cmd"`
			Headers frameHeaders     `json:"headers,omitempty"`
			Body    msgCallbackFrame `json:"body"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return fmt.Errorf("wecom: decode msg_callback: %w", err)
		}
		frame := wire.Body
		frame.Cmd = wire.Cmd
		frame.Headers = wire.Headers
		return c.handleMsgCallback(ctx, frame)
	case frameCmdEventCallback:
		event, err := decodeInboundCardEvent(raw)
		if err != nil {
			return fmt.Errorf("wecom: decode event_callback: %w", err)
		}
		return c.handleEventCallback(ctx, event)
	default:
		if env.ErrCode != 0 {
			log.Printf("wecom: received response errcode=%d errmsg=%q req=%s", env.ErrCode, env.ErrMsg, env.Headers.ReqID)
			return nil
		}
		if env.Cmd != "" {
			log.Printf("wecom: ignoring unhandled frame cmd %q", env.Cmd)
		}
		return nil
	}
}

// handleMsgCallback processes an inbound user message, handing it to the
// installed message sink (if any).
func (c *Client) handleMsgCallback(ctx context.Context, frame msgCallbackFrame) error {
	if c.onMessage != nil {
		c.onMessage(ctx, frame)
	}
	return nil
}

// handleEventCallback processes an inbound template_card interaction, handing it
// to the installed card-event sink (if any).
func (c *Client) handleEventCallback(ctx context.Context, event InboundCardEvent) error {
	if c.onCardEvent != nil {
		c.onCardEvent(ctx, event)
	}
	return nil
}

// sendFrame serialises an outbound projector Frame into the aibot respond wire
// frame and writes it. Streaming semantics (aibot_respond_update_msg with a
// shared stream id) are not implemented yet; a Frame flagged Stream is still
// sent as a standalone message here.
//
// Future protocol support: honour Frame.Stream by emitting aibot_respond_msg +
// aibot_respond_update_msg sequences sharing a stream id. Until that exists,
// Channel.Capabilities reports Streaming=false.
func (c *Client) sendFrame(ctx context.Context, chatID string, frame Frame) error {
	wire := newSendMsgFrame(chatID, frame)
	return c.writeJSON(ctx, wire)
}

func (c *Client) respondFrame(ctx context.Context, reqID string, frame Frame) error {
	wire := newRespondMsgFrame(reqID, frame)
	return c.writeJSON(ctx, wire)
}

func newSubscribeFrame(config Config) subscribeFrame {
	frame := subscribeFrame{
		Cmd:     frameCmdSubscribe,
		Headers: frameHeaders{ReqID: newReqID("sub")},
	}
	frame.Body.BotID = config.BotID
	frame.Body.Secret = config.Secret
	return frame
}

func newSendMsgFrame(chatID string, frame Frame) respondMsgFrame {
	wire := respondMsgFrame{
		Cmd:     frameCmdSendMsg,
		Headers: frameHeaders{ReqID: newReqID("send")},
	}
	wire.Body.ChatID = chatID
	fillRespondMsgBody(&wire, frame)
	return wire
}

func newRespondMsgFrame(reqID string, frame Frame) respondMsgFrame {
	wire := respondMsgFrame{
		Cmd:     frameCmdRespondMsg,
		Headers: frameHeaders{ReqID: reqID},
	}
	fillRespondMsgBody(&wire, frame)
	return wire
}

func fillRespondMsgBody(wire *respondMsgFrame, frame Frame) {
	wire.Body.MsgType = frame.MsgType
	wire.Body.Text = frame.Text
	wire.Body.Markdown = frame.Markdown
	wire.Body.TemplateCard = frame.TemplateCard
}

func newReqID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// pingLoop sends a WebSocket ping every pingInterval to keep the long
// connection alive. It exits when the context is cancelled or a write fails.
func (c *Client) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.ping(); err != nil {
				log.Printf("wecom: ping failed: %v", err)
				return
			}
		}
	}
}

// ping writes a single WebSocket control ping frame.
func (c *Client) ping() error {
	conn, err := c.currentConn()
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteControl(
		websocket.PingMessage,
		nil,
		time.Now().Add(writeTimeout),
	)
}

// Close tears down the long connection.
func (c *Client) Close() error {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Close()
}
