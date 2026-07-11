package daemon

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kxn/codex-remote-feishu/internal/config"
)

func TestAdminWeComBotsListAndConfigRedaction(t *testing.T) {
	cfg := config.DefaultAppConfig()
	cfg.WeCom.Enabled = daemonBoolPtr(true)
	cfg.WeCom.Bots = []config.WeComBotConfig{{
		ID:      "ops",
		Name:    "Ops Bot",
		BotID:   "wx_ops",
		Secret:  "secret_ops",
		Enabled: daemonBoolPtr(true),
	}}
	app, _ := newFeishuAdminTestApp(t, cfg, defaultFeishuServices(), &fakeAdminGatewayController{}, false, "")

	rec := performAdminRequest(t, app, http.MethodGet, "/api/admin/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("config status = %d body=%s", rec.Code, rec.Body.String())
	}
	var configResp adminConfigResponse
	if err := json.NewDecoder(rec.Body).Decode(&configResp); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if len(configResp.Config.WeCom.Bots) != 1 || !configResp.Config.WeCom.Bots[0].HasSecret {
		t.Fatalf("unexpected wecom config view: %#v", configResp.Config.WeCom)
	}

	rec = performAdminRequest(t, app, http.MethodGet, "/api/admin/wecom/bots", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload wecomBotsResponse
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode wecom list: %v", err)
	}
	if len(payload.Bots) != 1 {
		t.Fatalf("expected one wecom bot, got %#v", payload.Bots)
	}
	if payload.Bots[0].ID != "ops" || payload.Bots[0].Runtime == nil || payload.Bots[0].Runtime.GatewayID != "wecom:ops" {
		t.Fatalf("unexpected wecom bot summary: %#v", payload.Bots[0])
	}
}

func TestAdminWeComBotCreateUpdateDeleteAndReconnect(t *testing.T) {
	cfg := config.DefaultAppConfig()
	app, configPath := newFeishuAdminTestApp(t, cfg, defaultFeishuServices(), &fakeAdminGatewayController{}, false, "")

	createRec := performAdminRequest(t, app, http.MethodPost, "/api/admin/wecom/bots", `{"id":"ops","name":"Ops Bot","botId":"wx_ops","secret":"secret_ops","enabled":true}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createRec.Code, createRec.Body.String())
	}
	var createResp wecomBotResponse
	if err := json.NewDecoder(createRec.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if createResp.Bot.ID != "ops" || createResp.Bot.Runtime == nil || createResp.Bot.Runtime.GatewayID != "wecom:ops" {
		t.Fatalf("unexpected create payload: %#v", createResp.Bot)
	}

	loaded, err := config.LoadAppConfigAtPath(configPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(loaded.Config.WeCom.Bots) != 1 || loaded.Config.WeCom.Bots[0].BotID != "wx_ops" {
		t.Fatalf("unexpected persisted wecom bots: %#v", loaded.Config.WeCom.Bots)
	}

	updateRec := performAdminRequest(t, app, http.MethodPut, "/api/admin/wecom/bots/ops", `{"name":"Ops Bot 2","botId":"wx_ops_2","secret":"secret_ops_2","enabled":false}`)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", updateRec.Code, updateRec.Body.String())
	}
	var updateResp wecomBotResponse
	if err := json.NewDecoder(updateRec.Body).Decode(&updateResp); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if updateResp.Bot.Name != "Ops Bot 2" || updateResp.Bot.Runtime == nil || updateResp.Bot.Runtime.Enabled {
		t.Fatalf("unexpected update payload: %#v", updateResp.Bot)
	}

	reconnectRec := performAdminRequest(t, app, http.MethodPost, "/api/admin/wecom/bots/ops/reconnect", "")
	if reconnectRec.Code != http.StatusOK {
		t.Fatalf("reconnect status = %d body=%s", reconnectRec.Code, reconnectRec.Body.String())
	}

	deleteRec := performAdminRequest(t, app, http.MethodDelete, "/api/admin/wecom/bots/ops", "")
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	var deleteResp wecomBotResponse
	if err := json.NewDecoder(deleteRec.Body).Decode(&deleteResp); err != nil {
		t.Fatalf("decode delete: %v", err)
	}
	if deleteResp.Bot.Persisted {
		t.Fatalf("expected deleted bot persisted=false, got %#v", deleteResp.Bot)
	}
}
