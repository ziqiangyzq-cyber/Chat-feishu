package wecom

import (
	"context"
	"encoding/json"
	"testing"
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
	if body["chatid"] != "chat-1" || body["msgtype"] != "markdown" {
		t.Fatalf("unexpected body metadata: %#v", body)
	}
	if _, ok := body["text"]; ok {
		t.Fatalf("plain text frame should be converted to markdown for long connection: %s", raw)
	}
	markdown, ok := body["markdown"].(map[string]any)
	if !ok || markdown["content"] != "hello" {
		t.Fatalf("unexpected markdown body: %#v", body["markdown"])
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
	if body["msgtype"] != "markdown" {
		t.Fatalf("msgtype = %v, want markdown", body["msgtype"])
	}
}
