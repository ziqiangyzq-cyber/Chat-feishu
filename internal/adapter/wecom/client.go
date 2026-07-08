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
	frameTypeSubscribe        = "aibot_subscribe"
	frameTypeMsgCallback      = "aibot_msg_callback"
	frameTypeEventCallback    = "aibot_event_callback"
	frameTypeRespondMsg       = "aibot_respond_msg"
	frameTypeRespondUpdateMsg = "aibot_respond_update_msg"
)

// frameEnvelope is the common shape used to peek at an inbound frame's type
// before decoding it into a more specific struct.
type frameEnvelope struct {
	Type string `json:"type"`
}

// subscribeFrame registers this aibot for its event stream. It is the first
// frame sent after a successful dial.
type subscribeFrame struct {
	Type   string `json:"type"`
	BotID  string `json:"botid"`
	Secret string `json:"secret"`
}

// msgCallbackFrame is an inbound user message pushed by the server.
type msgCallbackFrame struct {
	Type    string `json:"type"`
	BotID   string `json:"botid"`
	ChatID  string `json:"chatid"`
	MsgID   string `json:"msgid"`
	MsgType string `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
}

// eventCallbackFrame is an inbound non-message event (menu click, enter chat,
// template_card interaction, ...).
type eventCallbackFrame struct {
	Type      string `json:"type"`
	BotID     string `json:"botid"`
	ChatID    string `json:"chatid"`
	EventType string `json:"event_type"`
	EventKey  string `json:"event_key"`
}

// streamMeta carries the streaming identity shared across a sequence of
// respond/update frames. finish marks the terminal update.
type streamMeta struct {
	ID     string `json:"id"`
	Finish bool   `json:"finish"`
}

// respondMsgFrame is a new outbound message.
type respondMsgFrame struct {
	Type    string      `json:"type"`
	ChatID  string      `json:"chatid"`
	MsgType string      `json:"msgtype"`
	Text    *textBody   `json:"text,omitempty"`
	Stream  *streamMeta `json:"stream,omitempty"`
}

// respondUpdateMsgFrame updates a previously sent message, used for streaming
// (successive frames share Stream.ID; the terminal frame sets Stream.Finish).
type respondUpdateMsgFrame struct {
	Type    string      `json:"type"`
	ChatID  string      `json:"chatid"`
	MsgID   string      `json:"msgid"`
	MsgType string      `json:"msgtype"`
	Text    *textBody   `json:"text,omitempty"`
	Stream  *streamMeta `json:"stream,omitempty"`
}

// textBody is the payload for a plain-text message.
type textBody struct {
	Content string `json:"content"`
}

// Client wraps a gorilla/websocket connection to the WeCom aibot long
// connection. It owns the dial, subscribe, read-loop and ping scaffolding.
type Client struct {
	config Config

	// dialFn is injectable for testing; defaults to dialDefault.
	dialFn func(ctx context.Context) (*websocket.Conn, error)

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
	return c.writeJSON(subscribeFrame{
		Type:   frameTypeSubscribe,
		BotID:  c.config.BotID,
		Secret: c.config.Secret,
	})
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
func (c *Client) writeJSON(v any) error {
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
	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
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

// dispatch decodes a raw inbound frame and routes it to the appropriate
// handler based on its type discriminator.
func (c *Client) dispatch(ctx context.Context, raw []byte) error {
	var env frameEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("wecom: decode frame envelope: %w", err)
	}
	switch env.Type {
	case frameTypeMsgCallback:
		var frame msgCallbackFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			return fmt.Errorf("wecom: decode msg_callback: %w", err)
		}
		return c.handleMsgCallback(ctx, frame)
	case frameTypeEventCallback:
		var frame eventCallbackFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			return fmt.Errorf("wecom: decode event_callback: %w", err)
		}
		return c.handleEventCallback(ctx, frame)
	default:
		// TODO(wecom Phase 2): handle additional inbound frame types (acks,
		// server keepalive frames) as the protocol is fleshed out.
		log.Printf("wecom: ignoring unhandled frame type %q", env.Type)
		return nil
	}
}

// handleMsgCallback processes an inbound user message.
func (c *Client) handleMsgCallback(_ context.Context, frame msgCallbackFrame) error {
	// TODO(wecom Phase 2): translate msgCallbackFrame into a channel-neutral
	// control.Action and forward it to the surface.ActionHandler wired via
	// Channel.Start. For now this returns without side effects.
	_ = frame
	return nil
}

// handleEventCallback processes an inbound non-message event (menu click,
// template_card interaction, ...).
func (c *Client) handleEventCallback(_ context.Context, frame eventCallbackFrame) error {
	// TODO(wecom Phase 2): map template_card / menu events to control.Action
	// and dispatch to the handler. For now this returns without side effects.
	_ = frame
	return nil
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
