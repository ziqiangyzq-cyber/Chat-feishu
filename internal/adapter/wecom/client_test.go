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

func TestDispatchMessageCallbackDecodesInboundMediaAndVoiceFields(t *testing.T) {
	client := NewClient(Config{})
	var frames []msgCallbackFrame
	client.onMessage = func(_ context.Context, frame msgCallbackFrame) {
		frames = append(frames, frame)
	}

	callbacks := []string{
		`{"cmd":"aibot_msg_callback","body":{"msgtype":"image","image":{"url":"https://example.test/image","aeskey":"image-key"}}}`,
		`{"cmd":"aibot_msg_callback","body":{"msgtype":"file","file":{"url":"https://example.test/file","aeskey":"file-key"}}}`,
		`{"cmd":"aibot_msg_callback","body":{"msgtype":"voice","voice":{"content":"voice transcript"}}}`,
	}
	for _, raw := range callbacks {
		if err := client.dispatch(context.Background(), []byte(raw)); err != nil {
			t.Fatalf("dispatch %s: %v", raw, err)
		}
	}

	if len(frames) != 3 {
		t.Fatalf("decoded frames = %d, want 3", len(frames))
	}
	if frames[0].Image.URL != "https://example.test/image" || frames[0].Image.AESKey != "image-key" {
		t.Fatalf("unexpected image callback: %+v", frames[0].Image)
	}
	if frames[1].File.URL != "https://example.test/file" || frames[1].File.AESKey != "file-key" {
		t.Fatalf("unexpected file callback: %+v", frames[1].File)
	}
	if frames[2].Voice.Content != "voice transcript" {
		t.Fatalf("unexpected voice callback: %+v", frames[2].Voice)
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

func TestNewRespondMsgFrameUsesCallbackReqID(t *testing.T) {
	frame := newRespondMsgFrame("req-1", Frame{
		MsgType: "text",
		Text:    &textBody{Content: "hello"},
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
	if body["msgtype"] != "text" {
		t.Fatalf("msgtype = %v, want text", body["msgtype"])
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
	upd := newStreamUpdateFrame(open.Headers.ReqID, "chat-1", "stream-1", "hello world", true)
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
	if reqID := frameReqID(t, decoded); reqID != open.Headers.ReqID {
		t.Fatalf("update req_id = %q, want opening req_id %q", reqID, open.Headers.ReqID)
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
		if id == "" {
			t.Fatal("generated empty request ID")
		}
		if len(id) > maxReqIDLength {
			t.Fatalf("request ID length = %d, want <= %d: %q", len(id), maxReqIDLength, id)
		}
		for _, ch := range []byte(id) {
			if (ch >= 'a' && ch <= 'z') ||
				(ch >= 'A' && ch <= 'Z') ||
				(ch >= '0' && ch <= '9') ||
				ch == '_' || ch == '-' || ch == '@' {
				continue
			}
			t.Fatalf("request ID contains unsupported character %q: %q", ch, id)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate request ID: %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestCallbackStreamReusesReqIDAcrossOpenAndUpdates(t *testing.T) {
	srv, frames, _ := wsTestServer(t)
	defer srv.Close()
	client := dialTestServer(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Dial(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	recvFrame(t, frames) // subscribe

	boundAt := time.Now()
	if err := client.streamMarkdownReply(ctx, "chat-1", "callback-req-1", boundAt, "part 1", false); err != nil {
		t.Fatal(err)
	}
	open := recvFrame(t, frames)
	if open["cmd"] != frameCmdRespondMsg {
		t.Fatalf("opening cmd = %v, want %q", open["cmd"], frameCmdRespondMsg)
	}
	if got := frameReqID(t, open); got != "callback-req-1" {
		t.Fatalf("opening req_id = %q, want callback-req-1", got)
	}
	openStreamID := frameStreamMeta(t, open)["id"]

	if err := client.streamMarkdown(ctx, "chat-1", "part 2", false); err != nil {
		t.Fatal(err)
	}
	update1 := recvFrame(t, frames)
	if err := client.streamMarkdown(ctx, "chat-1", "final", true); err != nil {
		t.Fatal(err)
	}
	update2 := recvFrame(t, frames)

	for i, update := range []map[string]any{update1, update2} {
		if update["cmd"] != frameCmdRespondUpdateMsg {
			t.Fatalf("update %d cmd = %v, want %q", i+1, update["cmd"], frameCmdRespondUpdateMsg)
		}
		if got := frameReqID(t, update); got != "callback-req-1" {
			t.Fatalf("update %d req_id = %q, want callback-req-1", i+1, got)
		}
		if got := frameStreamMeta(t, update)["id"]; got != openStreamID {
			t.Fatalf("update %d stream.id = %v, want %v", i+1, got, openStreamID)
		}
	}
}

func TestExpiredStreamFallsBackToStandaloneMessage(t *testing.T) {
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
	client.streams["chat-1"] = &chatStream{
		ID:        "stream-1",
		ReqID:     "expired-callback-req",
		Reply:     true,
		Started:   true,
		StartedAt: time.Now().Add(-streamReqIDSafetyTTL),
		LastText:  "progress",
	}

	done := make(chan error, 1)
	go func() {
		done <- client.streamMarkdown(ctx, "chat-1", "final answer", true)
	}()
	fallback := recvFrame(t, frames)
	if fallback["cmd"] != frameCmdSendMsg {
		t.Fatalf("fallback cmd = %v, want %q", fallback["cmd"], frameCmdSendMsg)
	}
	body, _ := fallback["body"].(map[string]any)
	if _, hasStream := body["stream"]; hasStream {
		t.Fatalf("expired stream fallback must be standalone: %#v", body)
	}
	markdown, _ := body["markdown"].(map[string]any)
	if markdown["content"] != "final answer" {
		t.Fatalf("expired stream fallback lost final content: %#v", body)
	}
	if err := serverConn.WriteJSON(map[string]any{
		"headers": map[string]any{"req_id": frameReqID(t, fallback)},
		"errcode": 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("fallback send: %v", err)
	}
}

// A rejected stream frame (notably errcode 846605) must stop using the invalid
// stream req_id and deliver its content through standalone aibot_send_msg.
func TestStreamFrame846605FallsBackToStandaloneMessage(t *testing.T) {
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
	if recovery["cmd"] != frameCmdSendMsg {
		t.Fatalf("fallback cmd = %v, want %q", recovery["cmd"], frameCmdSendMsg)
	}
	body, _ := recovery["body"].(map[string]any)
	if _, hasStream := body["stream"]; hasStream {
		t.Fatalf("fallback must be standalone, got stream body: %#v", body)
	}
	markdown, _ := body["markdown"].(map[string]any)
	if markdown["content"] != "part 1" {
		t.Fatalf("fallback lost content: %#v", body)
	}
	if got := frameReqID(t, recovery); got == frameReqID(t, open) {
		t.Fatalf("fallback reused rejected req_id %q", got)
	}
	if err := serverConn.WriteJSON(map[string]any{
		"headers": map[string]any{"req_id": frameReqID(t, recovery)},
		"errcode": 0,
	}); err != nil {
		t.Fatal(err)
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
	open := recvFrame(t, frames)
	if err := serverConn.WriteJSON(map[string]any{
		"headers": map[string]any{"req_id": frameReqID(t, open)},
		"errcode": 0,
	}); err != nil {
		t.Fatal(err)
	}

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
	if recovery["cmd"] != frameCmdSendMsg {
		t.Fatalf("fallback cmd = %v, want %q", recovery["cmd"], frameCmdSendMsg)
	}
	body, _ := recovery["body"].(map[string]any)
	markdown, _ := body["markdown"].(map[string]any)
	if markdown["content"] != "final answer" {
		t.Fatalf("final content not recovered: %#v", body)
	}
	if err := serverConn.WriteJSON(map[string]any{
		"headers": map[string]any{"req_id": frameReqID(t, recovery)},
		"errcode": 0,
	}); err != nil {
		t.Fatal(err)
	}
}

// A success response for a tracked stream frame clears the record without
// touching the live stream.
func TestStreamFrameSuccessResponseKeepsStream(t *testing.T) {
	client := NewClient(Config{BotID: "bot-1", Secret: "secret-1"})
	client.streams["chat-1"] = &chatStream{ID: "stream-1", ReqID: "upd-1", Started: true, StartedAt: time.Now()}
	client.streamFrames["upd-1"] = []streamFrameInfo{{ChatID: "chat-1", StreamID: "stream-1", Content: "x", SentAt: time.Now()}}

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

func TestStreamFrameResponsesWithSharedReqIDAreConsumedInOrder(t *testing.T) {
	client := NewClient(Config{})
	client.streams["chat-1"] = &chatStream{
		ID:        "stream-1",
		ReqID:     "callback-req-1",
		Started:   true,
		StartedAt: time.Now(),
	}
	client.streamFrames["callback-req-1"] = []streamFrameInfo{
		{ChatID: "chat-1", StreamID: "stream-1", Content: "part 1", SentAt: time.Now()},
		{ChatID: "chat-1", StreamID: "stream-1", Content: "part 2", SentAt: time.Now()},
	}

	if !client.handleStreamFrameResponse(frameEnvelope{Headers: frameHeaders{ReqID: "callback-req-1"}}) {
		t.Fatal("first response was not handled")
	}
	queue := client.streamFrames["callback-req-1"]
	if len(queue) != 1 || queue[0].Content != "part 2" {
		t.Fatalf("remaining response queue = %#v, want only part 2", queue)
	}
	if !client.handleStreamFrameResponse(frameEnvelope{Headers: frameHeaders{ReqID: "callback-req-1"}}) {
		t.Fatal("second response was not handled")
	}
	if _, exists := client.streamFrames["callback-req-1"]; exists {
		t.Fatalf("response queue not cleared: %#v", client.streamFrames)
	}
}

// An error for a frame of an already-replaced stream must not clobber the
// newer stream or trigger another resend.
func TestStreamFrameErrorForSupersededStreamIsIgnored(t *testing.T) {
	client := NewClient(Config{BotID: "bot-1", Secret: "secret-1"})
	client.streams["chat-1"] = &chatStream{ID: "stream-2", ReqID: "upd-new", Started: true, StartedAt: time.Now()}
	client.streamFrames["upd-old"] = []streamFrameInfo{{ChatID: "chat-1", StreamID: "stream-1", Content: "old", SentAt: time.Now()}}

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
