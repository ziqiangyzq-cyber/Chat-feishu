package daemon

import "github.com/kxn/codex-remote-feishu/internal/config"

type adminWeComSettingsView struct {
	Enabled bool                `json:"enabled"`
	Bots    []adminWeComBotView `json:"bots,omitempty"`
}

type adminWeComBotView struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	BotID     string `json:"botId,omitempty"`
	HasSecret bool   `json:"hasSecret"`
	Enabled   bool   `json:"enabled"`
}

type wecomBotsResponse struct {
	Bots []adminWeComBotSummary `json:"bots"`
}

type wecomBotResponse struct {
	Bot adminWeComBotSummary `json:"bot"`
}

type wecomBotWriteRequest struct {
	ID      *string `json:"id,omitempty"`
	Name    *string `json:"name,omitempty"`
	BotID   *string `json:"botId,omitempty"`
	Secret  *string `json:"secret,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
}

type adminWeComBotSummary struct {
	ID        string                   `json:"id"`
	Name      string                   `json:"name,omitempty"`
	BotID     string                   `json:"botId,omitempty"`
	HasSecret bool                     `json:"hasSecret"`
	Enabled   bool                     `json:"enabled"`
	Persisted bool                     `json:"persisted"`
	Runtime   *adminWeComRuntimeSummary `json:"runtime,omitempty"`
}

func indexOfConfigWeComBot(bots []config.WeComBotConfig, id string) int {
	for i, bot := range bots {
		if canonicalGatewayID(bot.ID) == canonicalGatewayID(id) {
			return i
		}
	}
	return -1
}

