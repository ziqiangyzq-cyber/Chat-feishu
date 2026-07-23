package wecom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestNewSubscribeFrameUsesOfficialEnvelope(t *testing.T) {
	frame := newSubscribeFrame(Config{BotID: "bot-1", Secret: "secret-1"})
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal subscribe frame: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal subscribe frame: %v", err)
	}
	if got["cmd"] != frameCmdSubscribe {
		t.Fatalf("cmd = %v, want %q", got["cmd"], frameCmdSubscribe)
	}
	if _, ok := got["type"]; ok {
		t.Fatalf("subscribe frame must not use legacy top-level type: %s", raw)
	}
	body, ok := got["body"].(map[string]any)
	if !ok {
		t.Fatalf("body missing or wrong shape: %s", raw)
	}
	if body["bot_id"] != "bot-1" || body["secret"] != "secret-1" {
		t.Fatalf("unexpected body: %#v", body)
	}
	if _, ok := got["botid"]; ok {
		t.Fatalf("subscribe frame must not use legacy top-level botid: %s", raw)
	}
}

func TestDispatchMessageCallbackUsesOfficialEnvelope(t *testing.T) {
	client := NewClient(Config{})
	var got msgCallbackFrame
	client.onMessage = func(_ context.Context, frame msgCallbackFrame) {
		got = frame
	}

	raw := []byte(`{
		"cmd": "aibot_msg_callback",
		"headers": {"req_id": "req-1"},
		"body": {
			"msgid": "msg-1",
			"aibotid": "bot-1",
			"chatid": "chat-1",
			"chattype": "group",
			"from": {"userid": "user-1"},
			"msgtype": "text",
			"text": {"content": " hello "}
		}
	}`)
	if err := client.dispatch(context.Background(), raw); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if got.Cmd != frameCmdMsgCallback || got.Headers.ReqID != "req-1" {
		t.Fatalf("unexpected envelope data: %+v", got)
	}
	if got.BotID != "bot-1" || got.ChatID != "chat-1" || got.ChatType != "group" || got.MsgID != "msg-1" {
		t.Fatalf("unexpected callback context: %+v", got)
	}
	if got.From.UserID != "user-1" || got.Text.Content != " hello " {
		t.Fatalf("unexpected callback payload: %+v", got)
	}
}

func TestNewSendMsgFrameUsesOfficialEnvelope(t *testing.T) {
	frame := newSendMsgFrame("chat-1", Frame{
		MsgType: "text",
		Text:    &textBody{Content: "hello"},
	})
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal send frame: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal send frame: %v", err)
	}
	if got["cmd"] != frameCmdSendMsg {
		t.Fatalf("cmd = %v, want %q", got["cmd"], frameCmdSendMsg)
	}
	if _, ok := got["chatid"]; ok {
		t.Fatalf("send frame must not use legacy top-level chatid: %s", raw)
	}
	body, ok := got["body"].(map[string]any)
	if !ok {
		t.Fatalf("body missing or wrong shape: %s", raw)
	}
	if body["chatid"] != "chat-1" || body["msgtype"] != "text" {
		t.Fatalf("unexpected body metadata: %#v", body)
	}
	text, ok := body["text"].(map[string]any)
	if !ok || text["content"] != "hello" {
		t.Fatalf("unexpected text body: %#v", body["text"])
	}
}

func TestNewRespondMsgFrameUsesCallbackReqIDAndFinishedStream(t *testing.T) {
	frame := newRespondMsgFrame("req-1", Frame{
		MsgType:  "markdown",
		Markdown: &markdownBody{Content: "**hello**"},
	})
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal respond frame: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal respond frame: %v", err)
	}
	if got["cmd"] != frameCmdRespondMsg {
		t.Fatalf("cmd = %v, want %q", got["cmd"], frameCmdRespondMsg)
	}
	headers, ok := got["headers"].(map[string]any)
	if !ok || headers["req_id"] != "req-1" {
		t.Fatalf("unexpected headers: %#v", got["headers"])
	}
	body, ok := got["body"].(map[string]any)
	if !ok {
		t.Fatalf("body missing or wrong shape: %s", raw)
	}
	if _, ok := body["chatid"]; ok {
		t.Fatalf("callback response frame must not include chatid: %s", raw)
	}
	if body["msgtype"] != "stream" {
		t.Fatalf("msgtype = %v, want stream", body["msgtype"])
	}
	if _, ok := body["markdown"]; ok {
		t.Fatalf("callback response must not use proactive markdown body: %s", raw)
	}
	stream, ok := body["stream"].(map[string]any)
	if !ok {
		t.Fatalf("stream body missing or wrong shape: %#v", body["stream"])
	}
	if stream["finish"] != true || stream["content"] != "**hello**" {
		t.Fatalf("unexpected finished stream: %#v", stream)
	}
	if stream["id"] == "" {
		t.Fatalf("stream id missing: %#v", stream)
	}
}

func TestNewRespondMsgFrameConvertsTextToFinishedStream(t *testing.T) {
	frame := newRespondMsgFrame("req-1", Frame{
		MsgType: "text",
		Text:    &textBody{Content: "hello"},
	})
	if frame.Body.MsgType != "stream" || frame.Body.Stream == nil {
		t.Fatalf("text callback reply was not converted to a stream: %#v", frame.Body)
	}
	if !frame.Body.Stream.Finish || frame.Body.Stream.Content != "hello" {
		t.Fatalf("unexpected finished stream: %#v", frame.Body.Stream)
	}
}

func TestStreamMarkdownOpensThenUpdates(t *testing.T) {
	// No live dial — marshal path only for open + update envelopes.
	open := newSendMsgFrame("chat-1", markdownFrame("hello"))
	open.Body.Stream = &streamMeta{ID: "stream-1", Finish: false, Content: "hello"}
	raw, err := json.Marshal(open)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["cmd"] != frameCmdSendMsg {
		t.Fatalf("cmd = %v", decoded["cmd"])
	}
	upd := newStreamUpdateFrame("chat-1", "stream-1", "hello world", true)
	raw, err = json.Marshal(upd)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["cmd"] != frameCmdRespondUpdateMsg {
		t.Fatalf("update cmd = %v", decoded["cmd"])
	}
	body, _ := decoded["body"].(map[string]any)
	stream, _ := body["stream"].(map[string]any)
	if stream["id"] != "stream-1" || stream["finish"] != true {
		t.Fatalf("unexpected stream meta: %#v", stream)
	}
}

// wsTestServer is a minimal aibot gateway fake: it captures every inbound
// frame and exposes the server-side conn so tests can inject responses.
func wsTestServer(t *testing.T) (*httptest.Server, chan map[string]any, chan *websocket.Conn) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	frames := make(chan map[string]any, 32)
	conns := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conns <- conn
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				frames <- m
			}
		}
	}))
	return srv, frames, conns
}

func dialTestServer(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	client := NewClient(Config{BotID: "bot-1", Secret: "secret-1"})
	client.dialFn = func(ctx context.Context) (*websocket.Conn, error) {
		url := "ws" + strings.TrimPrefix(srv.URL, "http")
		conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
		return conn, err
	}
	return client
}

func recvFrame(t *testing.T, frames chan map[string]any) map[string]any {
	t.Helper()
	select {
	case m := <-frames:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for frame")
		return nil
	}
}

func frameReqID(t *testing.T, m map[string]any) string {
	t.Helper()
	headers, _ := m["headers"].(map[string]any)
	req, _ := headers["req_id"].(string)
	if req == "" {
		t.Fatalf("frame missing req_id: %#v", m)
	}
	return req
}

func frameStreamMeta(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	body, _ := m["body"].(map[string]any)
	stream, _ := body["stream"].(map[string]any)
	if stream == nil {
		t.Fatalf("frame missing stream meta: %#v", m)
	}
	return stream
}

func TestNewReqIDIsUniqueUnderConcurrency(t *testing.T) {
	const (
		workers      = 32
		idsPerWorker = 1000
	)

	ids := make(chan string, workers*idsPerWorker)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range idsPerWorker {
				ids <- newReqID("test")
			}
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[string]struct{}, workers*idsPerWorker)
	for id := range ids {
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate request ID: %q", id)
		}
		seen[id] = struct{}{}
	}
}

// A rejected stream update (e.g. errcode 846605 once the server-side stream
// expired during a long turn) must not silently drop content: the client has
// to resend it through a fresh stream.
func TestStreamFrameErrorRecoversViaNewStream(t *testing.T) {
	srv, frames, conns := wsTestServer(t)
	defer srv.Close()
	client := dialTestServer(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Dial(ctx); err != nil {
		t.Fatal(err)
	}
	go func() { _ = client.Run(ctx) }()
	defer func() { _ = client.Close() }()

	serverConn := <-conns
	recvFrame(t, frames) // subscribe

	if err := client.streamMarkdown(ctx, "chat-1", "part 1", false); err != nil {
		t.Fatal(err)
	}
	open := recvFrame(t, frames)
	openStream := frameStreamMeta(t, open)

	// Gateway rejects the opening frame: stream is dead server-side.
	reject := map[string]any{
		"cmd":     open["cmd"],
		"headers": map[string]any{"req_id": frameReqID(t, open)},
		"errcode": 846605,
		"errmsg":  "invalid req_id",
	}
	if err := serverConn.WriteJSON(reject); err != nil {
		t.Fatal(err)
	}

	recovery := recvFrame(t, frames)
	recoveryStream := frameStreamMeta(t, recovery)
	if recoveryStream["id"] == openStream["id"] {
		t.Fatalf("recovery reused dead stream id %v", openStream["id"])
	}
	if recoveryStream["content"] != "part 1" {
		t.Fatalf("recovery lost content: %#v", recoveryStream)
	}
}

// The finish frame carries the final answer; when it is rejected after local
// stream state was already cleared, the content must still be delivered.
func TestStreamFinishErrorStillDeliversFinalContent(t *testing.T) {
	srv, frames, conns := wsTestServer(t)
	defer srv.Close()
	client := dialTestServer(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Dial(ctx); err != nil {
		t.Fatal(err)
	}
	go func() { _ = client.Run(ctx) }()
	defer func() { _ = client.Close() }()

	serverConn := <-conns
	recvFrame(t, frames) // subscribe

	if err := client.streamMarkdown(ctx, "chat-1", "progress", false); err != nil {
		t.Fatal(err)
	}
	recvFrame(t, frames) // opening frame, accepted (no response needed)

	if err := client.streamMarkdown(ctx, "chat-1", "final answer", true); err != nil {
		t.Fatal(err)
	}
	finish := recvFrame(t, frames)
	if meta := frameStreamMeta(t, finish); meta["finish"] != true {
		t.Fatalf("expected finish frame, got %#v", meta)
	}

	reject := map[string]any{
		"cmd":     finish["cmd"],
		"headers": map[string]any{"req_id": frameReqID(t, finish)},
		"errcode": 846605,
		"errmsg":  "invalid req_id",
	}
	if err := serverConn.WriteJSON(reject); err != nil {
		t.Fatal(err)
	}

	recovery := recvFrame(t, frames)
	recoveryStream := frameStreamMeta(t, recovery)
	if recoveryStream["content"] != "final answer" || recoveryStream["finish"] != true {
		t.Fatalf("final content not recovered: %#v", recoveryStream)
	}
}

// A success response for a tracked stream frame clears the record without
// touching the live stream.
func TestStreamFrameSuccessResponseKeepsStream(t *testing.T) {
	client := NewClient(Config{BotID: "bot-1", Secret: "secret-1"})
	client.streams["chat-1"] = &chatStream{ID: "stream-1", Started: true, StartedAt: time.Now()}
	client.streamFrames["upd-1"] = streamFrameInfo{ChatID: "chat-1", StreamID: "stream-1", Content: "x", SentAt: time.Now()}

	handled := client.handleStreamFrameResponse(frameEnvelope{Headers: frameHeaders{ReqID: "upd-1"}})
	if !handled {
		t.Fatal("expected success response to be handled")
	}
	if len(client.streamFrames) != 0 {
		t.Fatalf("frame record not cleared: %#v", client.streamFrames)
	}
	if client.streams["chat-1"] == nil {
		t.Fatal("live stream must survive a success response")
	}
}

// An error for a frame of an already-replaced stream must not clobber the
// newer stream or trigger another resend.
func TestStreamFrameErrorForSupersededStreamIsIgnored(t *testing.T) {
	client := NewClient(Config{BotID: "bot-1", Secret: "secret-1"})
	client.streams["chat-1"] = &chatStream{ID: "stream-2", Started: true, StartedAt: time.Now()}
	client.streamFrames["upd-old"] = streamFrameInfo{ChatID: "chat-1", StreamID: "stream-1", Content: "old", SentAt: time.Now()}

	handled := client.handleStreamFrameResponse(frameEnvelope{
		Headers: frameHeaders{ReqID: "upd-old"},
		ErrCode: 846605,
		ErrMsg:  "invalid req_id",
	})
	if !handled {
		t.Fatal("expected error response to be handled")
	}
	if st := client.streams["chat-1"]; st == nil || st.ID != "stream-2" {
		t.Fatalf("newer stream must survive: %#v", st)
	}
}
