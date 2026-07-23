package daemon

import (
	"net/http"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/config"
)

func (a *App) handleWeComBotsList(w http.ResponseWriter, _ *http.Request) {
	loaded, err := a.loadAdminConfig()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, apiError{
			Code:    "config_unavailable",
			Message: "failed to load config",
			Details: err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, wecomBotsResponse{Bots: a.mergeWeComRuntimeSummaries(adminWeComBots(loaded.Config))})
}

func (a *App) handleWeComBotCreate(w http.ResponseWriter, r *http.Request) {
	var req wecomBotWriteRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, apiError{
			Code:    "invalid_request",
			Message: "failed to decode wecom bot payload",
			Details: err.Error(),
		})
		return
	}

	a.adminConfigMu.Lock()
	loaded, err := a.loadAdminConfig()
	if err != nil {
		a.adminConfigMu.Unlock()
		writeAPIError(w, http.StatusInternalServerError, apiError{
			Code:    "config_unavailable",
			Message: "failed to load config",
			Details: err.Error(),
		})
		return
	}
	updated := loaded.Config
	bot, apiErr := buildWeComBotConfigForCreate(updated.WeCom.Bots, req)
	if apiErr != nil {
		a.adminConfigMu.Unlock()
		writeAPIError(w, http.StatusBadRequest, *apiErr)
		return
	}
	if indexOfConfigWeComBot(updated.WeCom.Bots, bot.ID) >= 0 {
		a.adminConfigMu.Unlock()
		writeAPIError(w, http.StatusConflict, apiError{
			Code:    "duplicate_wecom_bot",
			Message: "wecom bot id already exists",
			Details: bot.ID,
		})
		return
	}
	updated.WeCom.Bots = append(updated.WeCom.Bots, bot)
	if err := config.WriteAppConfig(loaded.Path, updated); err != nil {
		a.adminConfigMu.Unlock()
		writeAPIError(w, http.StatusInternalServerError, apiError{
			Code:    "config_write_failed",
			Message: "failed to save wecom bot config",
			Details: err.Error(),
		})
		return
	}
	a.adminConfigMu.Unlock()
	a.applyPersistedWeComBot(bot)
	writeJSON(w, http.StatusCreated, wecomBotResponse{Bot: a.findWeComBotSummary(updated, bot.ID)})
}

func (a *App) handleWeComBotUpdate(w http.ResponseWriter, r *http.Request) {
	botID := canonicalGatewayID(r.PathValue("id"))
	var req wecomBotWriteRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, apiError{
			Code:    "invalid_request",
			Message: "failed to decode wecom bot payload",
			Details: err.Error(),
		})
		return
	}

	a.adminConfigMu.Lock()
	loaded, err := a.loadAdminConfig()
	if err != nil {
		a.adminConfigMu.Unlock()
		writeAPIError(w, http.StatusInternalServerError, apiError{
			Code:    "config_unavailable",
			Message: "failed to load config",
			Details: err.Error(),
		})
		return
	}
	updated := loaded.Config
	index := indexOfConfigWeComBot(updated.WeCom.Bots, botID)
	if index < 0 {
		a.adminConfigMu.Unlock()
		writeAPIError(w, http.StatusNotFound, apiError{
			Code:    "wecom_bot_not_found",
			Message: "wecom bot not found",
			Details: botID,
		})
		return
	}
	current := updated.WeCom.Bots[index]
	nextBot, apiErr := mergeWeComBotConfig(current, req)
	if apiErr != nil {
		a.adminConfigMu.Unlock()
		writeAPIError(w, http.StatusBadRequest, *apiErr)
		return
	}
	if nextBot.ID != current.ID {
		if existing := indexOfConfigWeComBot(updated.WeCom.Bots, nextBot.ID); existing >= 0 && existing != index {
			a.adminConfigMu.Unlock()
			writeAPIError(w, http.StatusConflict, apiError{
				Code:    "duplicate_wecom_bot",
				Message: "wecom bot id already exists",
				Details: nextBot.ID,
			})
			return
		}
	}
	updated.WeCom.Bots[index] = nextBot
	if err := config.WriteAppConfig(loaded.Path, updated); err != nil {
		a.adminConfigMu.Unlock()
		writeAPIError(w, http.StatusInternalServerError, apiError{
			Code:    "config_write_failed",
			Message: "failed to save wecom bot config",
			Details: err.Error(),
		})
		return
	}
	a.adminConfigMu.Unlock()
	if current.ID != nextBot.ID {
		a.removePersistedWeComBot(current)
	}
	a.applyPersistedWeComBot(nextBot)
	writeJSON(w, http.StatusOK, wecomBotResponse{Bot: a.findWeComBotSummary(updated, nextBot.ID)})
}

func (a *App) handleWeComBotDelete(w http.ResponseWriter, r *http.Request) {
	botID := canonicalGatewayID(r.PathValue("id"))
	a.adminConfigMu.Lock()
	loaded, err := a.loadAdminConfig()
	if err != nil {
		a.adminConfigMu.Unlock()
		writeAPIError(w, http.StatusInternalServerError, apiError{
			Code:    "config_unavailable",
			Message: "failed to load config",
			Details: err.Error(),
		})
		return
	}
	updated := loaded.Config
	index := indexOfConfigWeComBot(updated.WeCom.Bots, botID)
	if index < 0 {
		a.adminConfigMu.Unlock()
		writeAPIError(w, http.StatusNotFound, apiError{
			Code:    "wecom_bot_not_found",
			Message: "wecom bot not found",
			Details: botID,
		})
		return
	}
	removed := updated.WeCom.Bots[index]
	updated.WeCom.Bots = append(updated.WeCom.Bots[:index], updated.WeCom.Bots[index+1:]...)
	if err := config.WriteAppConfig(loaded.Path, updated); err != nil {
		a.adminConfigMu.Unlock()
		writeAPIError(w, http.StatusInternalServerError, apiError{
			Code:    "config_write_failed",
			Message: "failed to save wecom bot config",
			Details: err.Error(),
		})
		return
	}
	a.adminConfigMu.Unlock()
	a.removePersistedWeComBot(removed)
	writeJSON(w, http.StatusOK, wecomBotResponse{Bot: adminWeComBotSummary{
		ID:        removed.ID,
		Name:      removed.Name,
		BotID:     removed.BotID,
		HasSecret: strings.TrimSpace(removed.Secret) != "",
		Enabled:   removed.Enabled == nil || *removed.Enabled,
		Persisted: false,
	}})
}

func (a *App) handleWeComBotReconnect(w http.ResponseWriter, r *http.Request) {
	botID := canonicalGatewayID(r.PathValue("id"))
	loaded, err := a.loadAdminConfig()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, apiError{
			Code:    "config_unavailable",
			Message: "failed to load config",
			Details: err.Error(),
		})
		return
	}
	index := indexOfConfigWeComBot(loaded.Config.WeCom.Bots, botID)
	if index < 0 {
		writeAPIError(w, http.StatusNotFound, apiError{
			Code:    "wecom_bot_not_found",
			Message: "wecom bot not found",
			Details: botID,
		})
		return
	}
	bot := loaded.Config.WeCom.Bots[index]
	a.applyPersistedWeComBot(bot)
	a.restartWeComGatewayRuntime(wecomGatewayIDForBot(bot.ID))
	writeJSON(w, http.StatusOK, wecomBotResponse{Bot: a.findWeComBotSummary(loaded.Config, bot.ID)})
}

func (a *App) applyPersistedWeComBot(bot config.WeComBotConfig) {
	channel := buildWeComChannel(bot, a.wecomInboundMediaTempDir(bot.ID))
	if bot.Enabled != nil && !*bot.Enabled {
		a.removePersistedWeComBot(bot)
		return
	}
	if strings.TrimSpace(bot.BotID) == "" || strings.TrimSpace(bot.Secret) == "" {
		return
	}
	a.SetWeComChannelWithGateway(wecomGatewayIDForBot(bot.ID), channel)
	a.mu.Lock()
	a.attachWeComGatewayRuntimeLocked(wecomGatewayIDForBot(bot.ID), channel)
	a.mu.Unlock()
}

func (a *App) removePersistedWeComBot(bot config.WeComBotConfig) {
	a.SetWeComChannelWithGateway(wecomGatewayIDForBot(bot.ID), nil)
}

func (a *App) findWeComBotSummary(cfg config.AppConfig, botID string) adminWeComBotSummary {
	summaries := a.mergeWeComRuntimeSummaries(adminWeComBots(cfg))
	for _, summary := range summaries {
		if canonicalGatewayID(summary.ID) == canonicalGatewayID(botID) {
			return summary
		}
	}
	return adminWeComBotSummary{ID: botID}
}

func buildWeComBotConfigForCreate(_ []config.WeComBotConfig, req wecomBotWriteRequest) (config.WeComBotConfig, *apiError) {
	id := optionalStringValue(req.ID)
	name := optionalStringValue(req.Name)
	botID := optionalStringValue(req.BotID)
	secret := optionalStringValue(req.Secret)
	callbackAESKey := optionalStringValue(req.CallbackAESKey)
	if botID == "" {
		return config.WeComBotConfig{}, &apiError{Code: "wecom_bot_id_required", Message: "wecom botId is required"}
	}
	if secret == "" {
		return config.WeComBotConfig{}, &apiError{Code: "wecom_bot_secret_required", Message: "wecom secret is required"}
	}
	if id == "" {
		id = botID
	}
	if name == "" {
		name = firstNonEmpty(id, botID, "WeCom Bot")
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return config.WeComBotConfig{
		ID:             id,
		Name:           name,
		BotID:          botID,
		Secret:         secret,
		CallbackAESKey: callbackAESKey,
		Enabled:        daemonBoolPtr(enabled),
	}, nil
}

func mergeWeComBotConfig(current config.WeComBotConfig, req wecomBotWriteRequest) (config.WeComBotConfig, *apiError) {
	if req.ID != nil {
		current.ID = optionalStringValue(req.ID)
	}
	if req.Name != nil {
		current.Name = optionalStringValue(req.Name)
	}
	if req.BotID != nil {
		current.BotID = optionalStringValue(req.BotID)
	}
	if req.Secret != nil && strings.TrimSpace(*req.Secret) != "" {
		current.Secret = optionalStringValue(req.Secret)
	}
	if req.CallbackAESKey != nil && strings.TrimSpace(*req.CallbackAESKey) != "" {
		current.CallbackAESKey = optionalStringValue(req.CallbackAESKey)
	}
	if req.Enabled != nil {
		current.Enabled = daemonBoolPtr(*req.Enabled)
	}
	current.ID = strings.TrimSpace(current.ID)
	current.Name = strings.TrimSpace(current.Name)
	current.BotID = strings.TrimSpace(current.BotID)
	current.Secret = strings.TrimSpace(current.Secret)
	current.CallbackAESKey = strings.TrimSpace(current.CallbackAESKey)
	if current.ID == "" {
		current.ID = current.BotID
	}
	if current.Name == "" {
		current.Name = firstNonEmpty(current.ID, current.BotID, "WeCom Bot")
	}
	if strings.TrimSpace(current.BotID) == "" {
		return config.WeComBotConfig{}, &apiError{Code: "wecom_bot_id_required", Message: "wecom botId is required"}
	}
	if strings.TrimSpace(current.Secret) == "" {
		return config.WeComBotConfig{}, &apiError{Code: "wecom_bot_secret_required", Message: "wecom secret is required"}
	}
	return current, nil
}
