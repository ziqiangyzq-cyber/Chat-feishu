package wecom

import (
	"context"
	"testing"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/surface"
)

func TestChannelCapabilitiesMatchImplementedTransport(t *testing.T) {
	caps := NewChannel(Config{}).Capabilities()
	if caps.Streaming {
		t.Fatal("streaming must remain false until aibot update frames are implemented")
	}
	if caps.FileSend {
		t.Fatal("file send must remain false until file projection/upload is implemented")
	}
	if caps.InteractiveSameFrame {
		t.Fatal("wecom cannot combine text streaming and interactive template cards in one frame")
	}
	if caps.MaxButtons != defaultMaxButtons {
		t.Fatalf("MaxButtons = %d, want %d", caps.MaxButtons, defaultMaxButtons)
	}
}

func TestDispatchMessageCarriesActorUserID(t *testing.T) {
	ch := NewChannel(Config{})
	var got control.Action
	ch.handler = func(_ context.Context, action control.Action) *surface.ActionResult {
		got = action
		return nil
	}

	frame := msgCallbackFrame{
		ChatID:  " chat-1 ",
		MsgID:   " msg-1 ",
		Headers: frameHeaders{ReqID: " req-1 "},
	}
	frame.From.UserID = " user-from "
	frame.Text.Content = " hello "

	ch.dispatchMessage(context.Background(), frame)

	if got.Kind != control.ActionTextMessage {
		t.Fatalf("Kind = %q, want %q", got.Kind, control.ActionTextMessage)
	}
	if got.ActorUserID != "user-from" {
		t.Fatalf("ActorUserID = %q, want user-from", got.ActorUserID)
	}
	if got.ChatID != "chat-1" || got.MessageID != "msg-1" || got.Text != "hello" {
		t.Fatalf("unexpected action context: %+v", got)
	}
	if reqID := ch.responseReqID("chat-1"); reqID != "req-1" {
		t.Fatalf("response req id = %q, want req-1", reqID)
	}
}

func TestDispatchMessageFallsBackToSenderAsSingleChatID(t *testing.T) {
	ch := NewChannel(Config{})
	var got control.Action
	ch.handler = func(_ context.Context, action control.Action) *surface.ActionResult {
		got = action
		return nil
	}

	frame := msgCallbackFrame{
		MsgID:    " msg-1 ",
		ChatType: "single",
		Headers:  frameHeaders{ReqID: "req-single"},
	}
	frame.From.UserID = " user-single "
	frame.Text.Content = " hello "

	ch.dispatchMessage(context.Background(), frame)

	if got.ChatID != "user-single" || got.ActorUserID != "user-single" {
		t.Fatalf("unexpected single-chat action: %+v", got)
	}
	if reqID := ch.responseReqID("user-single"); reqID != "req-single" {
		t.Fatalf("response req id = %q, want req-single", reqID)
	}
}

func TestResponseReqIDConsumedOnce(t *testing.T) {
	ch := NewChannel(Config{})
	ch.rememberResponseReqID("chat-1", "req-1")
	if got := ch.consumeResponseReqID("chat-1"); got != "req-1" {
		t.Fatalf("first consume = %q, want req-1", got)
	}
	if got := ch.consumeResponseReqID("chat-1"); got != "" {
		t.Fatalf("second consume = %q, want empty", got)
	}
}

func TestDispatchCardEventDoesNotRememberCallbackReqID(t *testing.T) {
	ch := NewChannel(Config{})
	var got control.Action
	ch.handler = func(_ context.Context, action control.Action) *surface.ActionResult {
		got = action
		return nil
	}

	ch.dispatchCardEvent(context.Background(), InboundCardEvent{
		ChatID:         " chat-1 ",
		OperatorUserID: " user-1 ",
		MessageID:      " msg-1 ",
		TaskID:         " picker-1 ",
		EventKey:       keyPrefixTargetSession + keyValueSep + "thread-1",
		Headers:        frameHeaders{ReqID: " req-card-1 "},
	})

	if got.Kind != control.ActionTargetPickerSelectSession {
		t.Fatalf("Kind = %q, want %q", got.Kind, control.ActionTargetPickerSelectSession)
	}
	if got.ChatID != "chat-1" || got.ActorUserID != "user-1" || got.MessageID != "msg-1" {
		t.Fatalf("card action lost inbound context: %+v", got)
	}
	if reqID := ch.responseReqID("chat-1"); reqID != "" {
		t.Fatalf("card callback req id must not be reused for outbound sends, got %q", reqID)
	}
}

func TestDispatchCardEventSuppressesDuplicateCallback(t *testing.T) {
	ch := NewChannel(Config{})
	var count int
	ch.handler = func(_ context.Context, action control.Action) *surface.ActionResult {
		count++
		return nil
	}
	event := InboundCardEvent{
		ChatID:         "chat-1",
		OperatorUserID: "user-1",
		MessageID:      "msg-1",
		TaskID:         "picker-1",
		EventKey:       keyTargetConfirm + keyValueSep + "picker-1",
		Selections: []InboundSelection{{
			QuestionKey: questionKeySession,
			OptionIDs:   []string{"new_thread"},
		}},
	}

	ch.dispatchCardEvent(context.Background(), event)
	ch.dispatchCardEvent(context.Background(), event)

	if count != 1 {
		t.Fatalf("handler call count = %d, want 1", count)
	}
}

func TestShouldSuppressDuplicateNotice(t *testing.T) {
	ch := NewChannel(Config{})
	now := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	ch.now = func() time.Time { return now }
	event := eventcontract.Event{Notice: &control.Notice{
		Code: "workspace_instance_busy",
		Text: "目标工作区当前暂时不可接管，请稍后重试。",
	}}
	if ch.shouldSuppressNotice("chat-1", event) {
		t.Fatal("first notice must be delivered")
	}
	if !ch.shouldSuppressNotice("chat-1", event) {
		t.Fatal("duplicate notice inside dedupe window must be suppressed")
	}
	now = now.Add(noticeDedupeWindow + time.Second)
	if ch.shouldSuppressNotice("chat-1", event) {
		t.Fatal("notice after dedupe window must be delivered")
	}
}

func TestDispatchMessageParsesSlashCommand(t *testing.T) {
	ch := NewChannel(Config{})
	var got control.Action
	ch.handler = func(_ context.Context, action control.Action) *surface.ActionResult {
		got = action
		return nil
	}

	frame := msgCallbackFrame{
		ChatID:  "chat-1",
		MsgID:   "msg-1",
		Headers: frameHeaders{ReqID: "req-1"},
	}
	frame.From.UserID = "user-1"
	frame.Text.Content = "/list"

	ch.dispatchMessage(context.Background(), frame)

	if got.Kind != control.ActionListInstances {
		t.Fatalf("Kind = %q, want %q", got.Kind, control.ActionListInstances)
	}
	if got.ChatID != "chat-1" || got.ActorUserID != "user-1" || got.MessageID != "msg-1" || got.Text != "/list" {
		t.Fatalf("command lost inbound context: %+v", got)
	}
}
