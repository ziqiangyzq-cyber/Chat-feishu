package daemon

import (
	"path/filepath"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/adapter/wecom"
	"github.com/kxn/codex-remote-feishu/internal/config"
)

func adminWeComBots(cfg config.AppConfig) []adminWeComBotSummary {
	summaries := make([]adminWeComBotSummary, 0, len(cfg.WeCom.Bots))
	for _, bot := range cfg.WeCom.Bots {
		if isDefaultCompatWeComBot(bot) {
			continue
		}
		runtime := adminWeComRuntimeSummaryForConfig(bot)
		summaries = append(summaries, adminWeComBotSummary{
			ID:        strings.TrimSpace(bot.ID),
			Name:      strings.TrimSpace(bot.Name),
			BotID:     strings.TrimSpace(bot.BotID),
			HasSecret: strings.TrimSpace(bot.Secret) != "",
			Enabled:   bot.Enabled == nil || *bot.Enabled,
			Persisted: true,
			Runtime:   runtime,
		})
	}
	return summaries
}

func adminWeComRuntimeSummaryForConfig(bot config.WeComBotConfig) *adminWeComRuntimeSummary {
	gatewayID := wecomGatewayIDForBot(bot.ID)
	summary := adminWeComRuntimeSummary{
		GatewayID: gatewayID,
		Name:      strings.TrimSpace(bot.Name),
		Enabled:   bot.Enabled == nil || *bot.Enabled,
		State:     "disabled",
	}
	if summary.Name == "" {
		summary.Name = adminWeComDisplayName(gatewayID)
	}
	if runtimeBotID := strings.TrimSpace(bot.BotID); runtimeBotID != "" {
		summary.GatewayID = wecomGatewayIDForBot(bot.ID)
	}
	return &summary
}

func (a *App) mergeWeComRuntimeSummaries(persisted []adminWeComBotSummary) []adminWeComBotSummary {
	if a == nil {
		return persisted
	}
	runtimeByGateway := map[string]adminWeComRuntimeSummary{}
	a.mu.Lock()
	runtimeSummaries := a.runtimeWeComSummariesLocked()
	a.mu.Unlock()
	for _, runtimeSummary := range runtimeSummaries {
		runtimeByGateway[canonicalGatewayID(runtimeSummary.GatewayID)] = runtimeSummary
	}
	for i := range persisted {
		gatewayID := wecomGatewayIDForBot(persisted[i].ID)
		if runtime, ok := runtimeByGateway[canonicalGatewayID(gatewayID)]; ok {
			runtime.Name = firstNonEmpty(strings.TrimSpace(persisted[i].Name), runtime.Name)
			runtime.Enabled = persisted[i].Enabled
			persisted[i].Runtime = &runtime
			delete(runtimeByGateway, canonicalGatewayID(gatewayID))
			continue
		}
		if persisted[i].Runtime != nil {
			persisted[i].Runtime.Name = firstNonEmpty(strings.TrimSpace(persisted[i].Name), persisted[i].Runtime.Name)
			persisted[i].Runtime.Enabled = persisted[i].Enabled
		}
	}
	for _, runtime := range runtimeByGateway {
		if isPlaceholderWeComRuntime(runtime) {
			continue
		}
		botID := strings.TrimSpace(strings.TrimPrefix(runtime.GatewayID, wecomNamespacePrefix))
		persisted = append(persisted, adminWeComBotSummary{
			ID:        botID,
			Name:      runtime.Name,
			BotID:     botID,
			HasSecret: false,
			Enabled:   runtime.Enabled,
			Persisted: false,
			Runtime:   &runtime,
		})
	}
	return persisted
}

func buildWeComChannel(bot config.WeComBotConfig, tempDir string) *wecom.Channel {
	return wecom.NewChannel(wecom.Config{
		BotID:          strings.TrimSpace(bot.BotID),
		Secret:         strings.TrimSpace(bot.Secret),
		CallbackAESKey: strings.TrimSpace(bot.CallbackAESKey),
		TempDir:        strings.TrimSpace(tempDir),
	})
}

func (a *App) wecomInboundMediaTempDir(botID string) string {
	stateDir := strings.TrimSpace(a.headlessRuntime.Paths.StateDir)
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, "image-staging", sanitizeGatewayPath(wecomGatewayIDForBot(botID)))
}

func isDefaultCompatWeComBot(bot config.WeComBotConfig) bool {
	id := strings.TrimSpace(bot.ID)
	name := strings.TrimSpace(bot.Name)
	return id == "bot" && name == "WeCom Bot"
}

func isPlaceholderWeComRuntime(runtime adminWeComRuntimeSummary) bool {
	return canonicalGatewayID(runtime.GatewayID) == canonicalGatewayID(wecomGatewayID) &&
		!runtime.Enabled &&
		!runtime.Connected &&
		strings.TrimSpace(runtime.State) == "disabled"
}
