package wecom

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// wsEndpoint is the WeCom aibot long-connection WebSocket endpoint.
const wsEndpoint = "wss://openws.work.weixin.qq.com"

// pingInterval is the keepalive cadence for the long connection.
const pingInterval = 30 * time.Second

// tcpKeepAlivePeriod sets the OS-level TCP keepalive idle/interval on the long
// connection. Unlike an application read deadline, kernel keepalive probes are
// answered by any live peer, so this detects a genuinely dead peer without
// false-positiving on a healthy-but-idle connection (WeCom sends nothing while
// idle and does not answer WS pings with pongs, so app-level read timing cannot
// distinguish idle from dead).
const tcpKeepAlivePeriod = 30 * time.Second

// writeTimeout bounds a single frame write.
const writeTimeout = 10 * time.Second

// ackWait bounds how long writeEnvelopeAndWait blocks for the server's reply to
// a request frame before giving up, so a missing ack cannot wedge the caller.
const ackWait = 30 * time.Second

var reqIDSequence atomic.Uint64

// Frame type discriminators exchanged over the aibot long connection.
const (
	frameCmdSubscribe         = "aibot_subscribe"
	frameCmdMsgCallback       = "aibot_msg_callback"
	frameCmdEventCallback     = "aibot_event_callback"
	frameCmdRespondMsg        = "aibot_respond_msg"
	frameCmdSendMsg           = "aibot_send_msg"
	frameCmdRespondUpdateMsg  = "aibot_respond_update_msg"
	frameCmdUploadMediaInit   = "aibot_upload_media_init"
	frameCmdUploadMediaChunk  = "aibot_upload_media_chunk"
	frameCmdUploadMediaFinish = "aibot_upload_media_finish"
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

type replyEnvelope struct {
	Cmd     string          `json:"cmd,omitempty"`
	Headers frameHeaders    `json:"headers,omitempty"`
	Body    json.RawMessage `json:"body,omitempty"`
	ErrCode int             `json:"errcode,omitempty"`
	ErrMsg  string          `json:"errmsg,omitempty"`
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
// respond/update frames. finish marks the terminal update; content carries the
// cumulative markdown for update frames.
type streamMeta struct {
	ID      string `json:"id"`
	Finish  bool   `json:"finish"`
	Content string `json:"content,omitempty"`
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
		File         *mediaBody    `json:"file,omitempty"`
		Image        *mediaBody    `json:"image,omitempty"`
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

	onState func(string, error)

	// onMessage receives decoded inbound user messages.
	onMessage func(ctx context.Context, frame msgCallbackFrame)
	// onCardEvent receives decoded inbound template_card interactions.
	onCardEvent func(ctx context.Context, event InboundCardEvent)

	mu           sync.Mutex
	conn         *websocket.Conn
	writeMu      sync.Mutex
	pendingReply map[string]chan replyEnvelope
	lastAck      replyEnvelope

	// streamMu guards per-chat streaming state used by streamMarkdown.
	streamMu sync.Mutex
	streams  map[string]*chatStream
	// streamFrames remembers in-flight fire-and-forget stream frames by req_id
	// so a gateway error response (e.g. errcode 846605 after the server-side
	// stream expired mid-turn) can be correlated back to its chat and the
	// content recovered instead of silently dropped.
	streamFrames map[string]streamFrameInfo
}

// chatStream tracks an open aibot stream.id for one chat.
type chatStream struct {
	ID        string
	Started   bool
	StartedAt time.Time
	LastText  string
}

// streamFrameInfo captures what a stream frame carried so it can be resent
// through a fresh stream when the gateway rejects it.
type streamFrameInfo struct {
	ChatID   string
	StreamID string
	Content  string
	Finish   bool
	Retry    bool
	SentAt   time.Time
}

const maxTrackedStreamFrames = 512

// NewClient constructs a Client for the given aibot credentials.
func NewClient(config Config) *Client {
	c := &Client{
		config:       config,
		pendingReply: map[string]chan replyEnvelope{},
		streams:      make(map[string]*chatStream),
		streamFrames: make(map[string]streamFrameInfo),
	}
	c.dialFn = c.dialDefault
	return c
}

func (c *Client) SetStateHook(hook func(string, error)) {
	c.mu.Lock()
	c.onState = hook
	c.mu.Unlock()
}

// enableTCPKeepAlive turns on OS-level TCP keepalive for the WebSocket's
// underlying socket so a dead peer is detected without an application read
// deadline. Best-effort: a non-TCP transport (e.g. a test fake) is left as-is.
func enableTCPKeepAlive(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	tcp, ok := conn.UnderlyingConn().(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcp.SetKeepAlive(true)
	_ = tcp.SetKeepAlivePeriod(tcpKeepAlivePeriod)
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
	c.emitState("connecting", nil)
	conn, err := c.dialFn(ctx)
	if err != nil {
		c.emitState("degraded", err)
		return err
	}
	enableTCPKeepAlive(conn)
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	if err := c.subscribe(ctx); err != nil {
		_ = c.Close()
		c.emitState("degraded", err)
		return err
	}
	c.emitState("connected", nil)
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
	if err == nil || errors.Is(err, context.Canceled) || ctx.Err() != nil {
		c.emitState("stopped", nil)
	} else {
		c.emitState("degraded", err)
	}
	return err
}

// readLoop reads frames and dispatches them by type until an error occurs or
// the context is cancelled.
func (c *Client) readLoop(ctx context.Context) error {
	conn, err := c.currentConn()
	if err != nil {
		return err
	}
	// No application-level read deadline here: WeCom does not answer our WS pings
	// with pongs and sends nothing on an idle connection, so a read deadline
	// cannot tell "healthy but idle" from "dead" and just tears down good
	// connections every pongWait (observed as ~90s i/o-timeout churn). Dead
	// connections are instead caught by TCP keepalive (set in Dial) and by a
	// failed ping write (pingLoop), both of which only fire on real death.
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
		if c.handleStreamFrameResponse(env) {
			return nil
		}
		if env.ErrCode != 0 {
			if c.deliverReply(replyEnvelope{
				Cmd:     env.Cmd,
				Headers: env.Headers,
				ErrCode: env.ErrCode,
				ErrMsg:  env.ErrMsg,
			}) {
				return nil
			}
			log.Printf("wecom: received response errcode=%d errmsg=%q req=%s", env.ErrCode, env.ErrMsg, env.Headers.ReqID)
			return nil
		}
		if c.deliverReply(replyEnvelope{
			Cmd:     env.Cmd,
			Headers: env.Headers,
			Body:    rawBodyFromEnvelope(raw),
		}) {
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

// sendFrame serialises an outbound projector Frame into the aibot send wire
// frame and writes it. Incremental streaming uses streamMarkdown instead;
// Frame.Stream only marks "text half of a split text+card event" and is still
// sent as a standalone message so the following template_card is not coupled.
func (c *Client) sendFrame(ctx context.Context, chatID string, frame Frame) error {
	wire := newSendMsgFrame(chatID, frame)
	_, err := c.writeEnvelopeAndWait(ctx, wire.Headers.ReqID, wire.Cmd, wire)
	return err
}

func (c *Client) respondFrame(ctx context.Context, reqID string, frame Frame) error {
	wire := newRespondMsgFrame(reqID, frame)
	reply, err := c.writeEnvelopeAndWait(ctx, wire.Headers.ReqID, wire.Cmd, wire)
	if err == nil {
		log.Printf("wecom: respond ack req=%s errcode=%d", wire.Headers.ReqID, reply.ErrCode)
	}
	return err
}

// streamMarkdown writes (or updates) a streaming markdown message for chatID.
// Successive calls with finish=false share one stream id; finish=true ends it.
func (c *Client) streamMarkdown(ctx context.Context, chatID, content string, finish bool) error {
	return c.streamMarkdownFrame(ctx, chatID, content, finish, false)
}

func (c *Client) streamMarkdownFrame(ctx context.Context, chatID, content string, finish, retry bool) error {
	chatID = strings.TrimSpace(chatID)
	content = strings.TrimSpace(content)
	if chatID == "" {
		return errors.New("wecom: stream requires chatID")
	}
	if content == "" && !finish {
		return nil
	}

	c.streamMu.Lock()
	st := c.streams[chatID]
	if st == nil {
		st = &chatStream{ID: newReqID("stream"), StartedAt: time.Now()}
		c.streams[chatID] = st
	}
	if content != "" {
		st.LastText = content
	}
	streamID := st.ID
	started := st.Started
	if !started {
		st.Started = true
	}
	if finish {
		delete(c.streams, chatID)
	}
	c.streamMu.Unlock()

	var reqID string
	var err error
	if !started {
		wire := newSendMsgFrame(chatID, markdownFrame(content))
		wire.Body.Stream = &streamMeta{ID: streamID, Finish: finish, Content: content}
		reqID = wire.Headers.ReqID
		err = c.writeJSON(ctx, wire)
	} else {
		wire := newStreamUpdateFrame(chatID, streamID, content, finish)
		reqID = wire.Headers.ReqID
		err = c.writeJSON(ctx, wire)
	}
	if err == nil {
		c.trackStreamFrame(reqID, streamFrameInfo{
			ChatID:   chatID,
			StreamID: streamID,
			Content:  content,
			Finish:   finish,
			Retry:    retry,
			SentAt:   time.Now(),
		})
	}
	return err
}

// trackStreamFrame records an in-flight stream frame keyed by req_id, pruning
// stale entries so an upstream that stops answering cannot grow the map.
func (c *Client) trackStreamFrame(reqID string, info streamFrameInfo) {
	if reqID == "" {
		return
	}
	c.streamMu.Lock()
	if len(c.streamFrames) >= maxTrackedStreamFrames {
		cutoff := time.Now().Add(-2 * time.Minute)
		for id, fi := range c.streamFrames {
			if fi.SentAt.Before(cutoff) {
				delete(c.streamFrames, id)
			}
		}
	}
	c.streamFrames[reqID] = info
	c.streamMu.Unlock()
}

// handleStreamFrameResponse consumes gateway responses to tracked stream
// frames. A success just clears the record; a failure means the server-side
// stream is dead (observed as errcode 846605 "invalid req_id" once a long
// turn outlives the stream), so the carried content is resent through a fresh
// stream instead of being silently dropped. Returns true when the envelope
// belonged to a stream frame.
func (c *Client) handleStreamFrameResponse(env frameEnvelope) bool {
	reqID := env.Headers.ReqID
	if reqID == "" {
		return false
	}
	c.streamMu.Lock()
	info, tracked := c.streamFrames[reqID]
	if tracked {
		delete(c.streamFrames, reqID)
	}
	resend := false
	finishCurrent := false
	if tracked && env.ErrCode != 0 {
		st := c.streams[info.ChatID]
		switch {
		case st != nil && st.ID == info.StreamID:
			// Current stream is the dead one: forget it and resend via a new one.
			delete(c.streams, info.ChatID)
			resend = true
		case st == nil && info.Finish:
			// The finish frame failed after local state was already cleared;
			// the final answer must still reach the user.
			resend = true
		case st != nil && st.ID != info.StreamID && info.Finish:
			// A newer (recovered) stream is open; land the final content there.
			finishCurrent = true
		}
	}
	c.streamMu.Unlock()
	if !tracked {
		return false
	}
	if env.ErrCode == 0 {
		return true
	}
	if !resend && !finishCurrent {
		log.Printf("wecom: stream frame rejected errcode=%d chat=%s req=%s (superseded, no resend)", env.ErrCode, info.ChatID, reqID)
		return true
	}
	if info.Retry {
		// The recovery frame itself was rejected: last resort is a plain
		// standalone message so the content still lands.
		log.Printf("wecom: stream recovery rejected errcode=%d chat=%s; falling back to standalone message", env.ErrCode, info.ChatID)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := c.sendFrame(ctx, info.ChatID, markdownFrame(info.Content)); err != nil {
				log.Printf("wecom: standalone fallback failed chat=%s: %v", info.ChatID, err)
			}
		}()
		return true
	}
	log.Printf("wecom: stream frame rejected errcode=%d chat=%s finish=%v; resending content", env.ErrCode, info.ChatID, info.Finish)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.streamMarkdownFrame(ctx, info.ChatID, info.Content, info.Finish, true); err != nil {
			log.Printf("wecom: stream recovery send failed chat=%s: %v", info.ChatID, err)
		}
	}()
	return true
}

func newStreamUpdateFrame(chatID, streamID, content string, finish bool) respondUpdateMsgFrame {
	wire := respondUpdateMsgFrame{
		Cmd:     frameCmdRespondUpdateMsg,
		Headers: frameHeaders{ReqID: newReqID("upd")},
	}
	wire.Body.ChatID = chatID
	wire.Body.MsgType = "markdown"
	if content != "" {
		wire.Body.Text = &textBody{Content: content}
	}
	wire.Body.Stream = &streamMeta{ID: streamID, Finish: finish, Content: content}
	return wire
}

// dropStream forgets any open stream for chatID without sending a finish frame.
func (c *Client) dropStream(chatID string) {
	c.streamMu.Lock()
	delete(c.streams, chatID)
	c.streamMu.Unlock()
}

// streamAge returns how long the open stream for chatID has been active.
func (c *Client) streamAge(chatID string, now time.Time) (time.Duration, bool) {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	st := c.streams[chatID]
	if st == nil || !st.Started {
		return 0, false
	}
	return now.Sub(st.StartedAt), true
}

// activeStreamChats returns chat ids with an open stream older than maxAge.
func (c *Client) activeStreamChats(now time.Time, maxAge time.Duration) []string {
	if maxAge <= 0 {
		return nil
	}
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	out := make([]string, 0)
	for chatID, st := range c.streams {
		if st != nil && st.Started && now.Sub(st.StartedAt) > maxAge {
			out = append(out, chatID)
		}
	}
	return out
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
	// The callback reply API does not accept the proactive-send "markdown"
	// body shape. WeCom's official long-connection SDK replies to ordinary
	// text/markdown with msgtype=stream, including when the whole answer is
	// delivered in a single finished frame. A markdown-shaped reply can receive
	// an errcode=0 acknowledgement yet never render in the client.
	if content, ok := callbackStreamContent(frame); ok {
		wire.Body.MsgType = "stream"
		wire.Body.Stream = &streamMeta{
			ID:      newReqID("stream"),
			Finish:  true,
			Content: content,
		}
		return wire
	}
	fillRespondMsgBody(&wire, frame)
	return wire
}

func callbackStreamContent(frame Frame) (string, bool) {
	switch {
	case frame.MsgType == "markdown" && frame.Markdown != nil:
		return frame.Markdown.Content, true
	case frame.MsgType == "text" && frame.Text != nil:
		return frame.Text.Content, true
	default:
		return "", false
	}
}

func fillRespondMsgBody(wire *respondMsgFrame, frame Frame) {
	wire.Body.MsgType = frame.MsgType
	wire.Body.Text = frame.Text
	wire.Body.Markdown = frame.Markdown
	wire.Body.File = frame.File
	wire.Body.Image = frame.Image
	wire.Body.TemplateCard = frame.TemplateCard
}

func newReqID(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), reqIDSequence.Add(1))
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
				// A failed ping means the connection is dead. Close it so the
				// blocked ReadMessage returns immediately, letting Run return and
				// the supervisor re-dial rather than waiting out pongWait.
				_ = c.Close()
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

func (c *Client) emitState(state string, err error) {
	c.mu.Lock()
	hook := c.onState
	c.mu.Unlock()
	if hook != nil {
		hook(state, err)
	}
}

type uploadMediaInitBody struct {
	Type        mediaType `json:"type"`
	Filename    string    `json:"filename"`
	TotalSize   int       `json:"total_size"`
	TotalChunks int       `json:"total_chunks"`
	MD5         string    `json:"md5,omitempty"`
}

type uploadMediaInitResult struct {
	UploadID string `json:"upload_id"`
}

type uploadMediaChunkBody struct {
	UploadID   string `json:"upload_id"`
	ChunkIndex int    `json:"chunk_index"`
	Base64Data string `json:"base64_data"`
}

type uploadMediaFinishBody struct {
	UploadID string `json:"upload_id"`
}

type uploadMediaFinishResult struct {
	Type    mediaType `json:"type"`
	MediaID string    `json:"media_id"`
}

func (c *Client) uploadMedia(ctx context.Context, path string, mediaType mediaType) (string, string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", errors.New("wecom: upload media path is required")
	}
	buffer, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("wecom: read media file: %w", err)
	}
	fileName := strings.TrimSpace(filepath.Base(path))
	if fileName == "" {
		fileName = "artifact"
	}
	const chunkSize = 512 * 1024
	totalChunks := (len(buffer) + chunkSize - 1) / chunkSize
	if totalChunks <= 0 {
		totalChunks = 1
	}
	if totalChunks > 100 {
		return "", fileName, fmt.Errorf("wecom: file too large: %d chunks", totalChunks)
	}
	initReqID := newReqID("upload-init")
	initReply, err := c.sendReplyJSON(ctx, initReqID, frameCmdUploadMediaInit, uploadMediaInitBody{
		Type:        mediaType,
		Filename:    fileName,
		TotalSize:   len(buffer),
		TotalChunks: totalChunks,
		MD5:         fmt.Sprintf("%x", md5.Sum(buffer)),
	})
	if err != nil {
		return "", fileName, err
	}
	var initResult uploadMediaInitResult
	if err := json.Unmarshal(initReply.Body, &initResult); err != nil {
		return "", fileName, fmt.Errorf("wecom: decode upload init: %w", err)
	}
	if strings.TrimSpace(initResult.UploadID) == "" {
		return "", fileName, errors.New("wecom: upload init returned empty upload_id")
	}
	for i := 0; i < totalChunks; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(buffer) {
			end = len(buffer)
		}
		_, err := c.sendReplyJSON(ctx, newReqID("upload-chunk"), frameCmdUploadMediaChunk, uploadMediaChunkBody{
			UploadID:   initResult.UploadID,
			ChunkIndex: i,
			Base64Data: base64.StdEncoding.EncodeToString(buffer[start:end]),
		})
		if err != nil {
			return "", fileName, err
		}
	}
	finishReply, err := c.sendReplyJSON(ctx, newReqID("upload-finish"), frameCmdUploadMediaFinish, uploadMediaFinishBody{
		UploadID: initResult.UploadID,
	})
	if err != nil {
		return "", fileName, err
	}
	var finishResult uploadMediaFinishResult
	if err := json.Unmarshal(finishReply.Body, &finishResult); err != nil {
		return "", fileName, fmt.Errorf("wecom: decode upload finish: %w", err)
	}
	if strings.TrimSpace(finishResult.MediaID) == "" {
		return "", fileName, errors.New("wecom: upload finish returned empty media_id")
	}
	return strings.TrimSpace(finishResult.MediaID), fileName, nil
}

func (c *Client) sendReplyJSON(ctx context.Context, reqID, cmd string, body any) (replyEnvelope, error) {
	payload := struct {
		Cmd     string       `json:"cmd"`
		Headers frameHeaders `json:"headers,omitempty"`
		Body    any          `json:"body,omitempty"`
	}{
		Cmd:     cmd,
		Headers: frameHeaders{ReqID: reqID},
		Body:    body,
	}
	return c.writeEnvelopeAndWait(ctx, reqID, cmd, payload)
}

func (c *Client) writeEnvelopeAndWait(ctx context.Context, reqID, label string, payload any) (replyEnvelope, error) {
	replyCh := make(chan replyEnvelope, 1)
	c.mu.Lock()
	c.pendingReply[reqID] = replyCh
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pendingReply, reqID)
		c.mu.Unlock()
	}()
	if err := c.writeJSON(ctx, payload); err != nil {
		return replyEnvelope{}, err
	}
	// Bound the wait so a missing/lost server ack can never wedge the caller
	// indefinitely (the worker goroutine that runs deliveries, or the read loop
	// for pre-connection setup frames). The ack normally arrives within one RTT.
	timer := time.NewTimer(ackWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return replyEnvelope{}, ctx.Err()
	case <-timer.C:
		return replyEnvelope{}, fmt.Errorf("wecom: %s timed out waiting for ack", label)
	case reply := <-replyCh:
		if reply.ErrCode != 0 {
			return reply, fmt.Errorf("wecom: %s failed: %s", label, strings.TrimSpace(reply.ErrMsg))
		}
		return reply, nil
	}
}

func (c *Client) deliverReply(reply replyEnvelope) bool {
	reqID := strings.TrimSpace(reply.Headers.ReqID)
	if reqID == "" {
		return false
	}
	c.mu.Lock()
	replyCh := c.pendingReply[reqID]
	if replyCh != nil {
		select {
		case replyCh <- reply:
		default:
		}
		c.lastAck = reply
	}
	c.mu.Unlock()
	return replyCh != nil
}

func (c *Client) lastAckMessageID() (string, error) {
	c.mu.Lock()
	reply := c.lastAck
	c.mu.Unlock()
	if len(reply.Body) == 0 {
		return "", errors.New("wecom: no reply ack available")
	}
	var body map[string]any
	if err := json.Unmarshal(reply.Body, &body); err != nil {
		return "", err
	}
	if value, ok := body["msgid"].(string); ok {
		return strings.TrimSpace(value), nil
	}
	return "", errors.New("wecom: ack missing msgid")
}

func rawBodyFromEnvelope(raw []byte) json.RawMessage {
	var wire struct {
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil
	}
	return wire.Body
}
