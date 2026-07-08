package wecom

import (
	"context"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/core/control"
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
		ChatID:     " chat-1 ",
		MsgID:      " msg-1 ",
		FromUserID: " user-from ",
	}
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
}
