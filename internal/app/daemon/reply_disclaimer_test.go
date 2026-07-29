package daemon

import (
	"strings"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/render"
)

const testReplyDisclaimer = "⚠️ 仅作为 EFC 方案设计使用，不作为最终依据。"

func TestReplyDisclaimersConfigMapsPerGateway(t *testing.T) {
	disclaimers := replyDisclaimersFromFeishuApps([]config.FeishuAppConfig{
		{ID: " app-1 ", ReplyDisclaimer: " " + testReplyDisclaimer + " "},
		{ID: "app-2"},
	})
	if len(disclaimers) != 1 || disclaimers["app-1"] != testReplyDisclaimer {
		t.Fatalf("unexpected disclaimers = %#v", disclaimers)
	}
}

func TestApplyReplyDisclaimerAppendsToFinalAssistantMarkdown(t *testing.T) {
	app := New("", "", nil, agentproto.ServerIdentity{})
	app.SetReplyDisclaimers(map[string]string{"app-1": testReplyDisclaimer})
	event := eventcontract.Event{
		Kind:      eventcontract.KindBlockCommitted,
		GatewayID: "app-1",
		Block: &render.Block{
			Kind:  render.BlockAssistantMarkdown,
			Text:  "计算完成。",
			Final: true,
		},
	}

	got := app.applyReplyDisclaimer(event, "app-1")
	if got.Block == nil || got.Block.Text != "计算完成。\n\n"+testReplyDisclaimer {
		t.Fatalf("unexpected block = %#v", got.Block)
	}
	if event.Block.Text != "计算完成。" {
		t.Fatalf("source event was mutated: %#v", event.Block)
	}
}

func TestApplyReplyDisclaimerIsIdempotentAndFinalOnly(t *testing.T) {
	app := New("", "", nil, agentproto.ServerIdentity{})
	app.SetReplyDisclaimers(map[string]string{"app-1": testReplyDisclaimer})
	finalEvent := eventcontract.Event{
		Kind:      eventcontract.KindBlockCommitted,
		GatewayID: "app-1",
		Block: &render.Block{
			Kind:  render.BlockAssistantMarkdown,
			Text:  "计算完成。\n\n" + testReplyDisclaimer,
			Final: true,
		},
	}
	got := app.applyReplyDisclaimer(finalEvent, "app-1")
	if strings.Count(got.Block.Text, testReplyDisclaimer) != 1 {
		t.Fatalf("disclaimer duplicated: %q", got.Block.Text)
	}

	streaming := finalEvent
	streaming.Block = &render.Block{
		Kind:  render.BlockAssistantMarkdown,
		Text:  "计算中",
		Final: false,
	}
	got = app.applyReplyDisclaimer(streaming, "app-1")
	if got.Block.Text != "计算中" {
		t.Fatalf("non-final block was modified: %q", got.Block.Text)
	}
}
