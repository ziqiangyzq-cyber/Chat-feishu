package daemon

import (
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/config"
	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/render"
)

func replyDisclaimersFromFeishuApps(apps []config.FeishuAppConfig) map[string]string {
	disclaimers := map[string]string{}
	for _, app := range apps {
		gatewayID := strings.TrimSpace(app.ID)
		disclaimer := strings.TrimSpace(app.ReplyDisclaimer)
		if gatewayID != "" && disclaimer != "" {
			disclaimers[gatewayID] = disclaimer
		}
	}
	return disclaimers
}

func (a *App) SetReplyDisclaimers(disclaimers map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(disclaimers) == 0 {
		a.replyDisclaimers = nil
		return
	}
	a.replyDisclaimers = make(map[string]string, len(disclaimers))
	for gatewayID, disclaimer := range disclaimers {
		gatewayID = strings.TrimSpace(gatewayID)
		disclaimer = strings.TrimSpace(disclaimer)
		if gatewayID != "" && disclaimer != "" {
			a.replyDisclaimers[gatewayID] = disclaimer
		}
	}
}

func (a *App) applyReplyDisclaimer(event eventcontract.Event, gatewayID string) eventcontract.Event {
	if event.Block == nil || !event.Block.Final || event.Block.Kind != render.BlockAssistantMarkdown {
		return event
	}
	disclaimer := strings.TrimSpace(a.replyDisclaimers[strings.TrimSpace(gatewayID)])
	if disclaimer == "" {
		return event
	}
	block := *event.Block
	body := strings.TrimSpace(block.Text)
	if strings.Contains(body, disclaimer) {
		return event
	}
	if body == "" {
		block.Text = disclaimer
	} else {
		block.Text = body + "\n\n" + disclaimer
	}
	event.Block = &block
	event.Payload = nil
	return event
}
