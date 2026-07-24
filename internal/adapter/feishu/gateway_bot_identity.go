package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

type botIdentityResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Bot  struct {
		OpenID  string `json:"open_id"`
		AppName string `json:"app_name"`
	} `json:"bot"`
}

// currentBotOpenID returns the cached bot open_id, or empty when it has not
// been resolved yet.
func (g *LiveGateway) currentBotOpenID() string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.botOpenID
}

// ensureBotOpenID resolves this app's bot open_id once and caches it. The value
// lets group-chat inbound routing tell whether a message @mentions the bot.
// Failures are logged and cached as "resolved but empty" so the mention gate
// simply fails open (legacy "reply to everything" behavior) rather than
// silently dropping traffic.
func (g *LiveGateway) ensureBotOpenID(ctx context.Context) string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	if g.botOpenIDResolved {
		openID := g.botOpenID
		g.mu.Unlock()
		return openID
	}
	g.mu.Unlock()

	openID, err := g.fetchBotOpenID(ctx)

	g.mu.Lock()
	g.botOpenIDResolved = true
	g.botOpenID = openID
	g.mu.Unlock()

	switch {
	case err != nil:
		log.Printf("feishu gateway %s: resolve bot open_id failed, group mention gate disabled: %v", g.config.GatewayID, err)
	case openID == "":
		log.Printf("feishu gateway %s: bot open_id empty, group mention gate disabled", g.config.GatewayID)
	default:
		log.Printf("feishu gateway %s: resolved bot open_id, group mention gate active", g.config.GatewayID)
	}
	return openID
}

func (g *LiveGateway) fetchBotOpenID(ctx context.Context) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	apiResp, err := DoSDK(callCtx, g.broker, CallSpec{
		GatewayID:  g.config.GatewayID,
		API:        "bot.v3.info",
		Class:      CallClassMetaHTTP,
		Priority:   CallPriorityInteractive,
		Retry:      RetrySafe,
		Permission: PermissionFailFast,
	}, func(reqCtx context.Context, sdkClient *lark.Client) (*larkcore.ApiResp, error) {
		return sdkClient.Get(reqCtx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	})
	if err != nil {
		return "", err
	}
	var decoded botIdentityResponse
	if err := json.Unmarshal(apiResp.RawBody, &decoded); err != nil {
		return "", err
	}
	if decoded.Code != 0 {
		return "", fmt.Errorf("bot info response code %d: %s", decoded.Code, decoded.Msg)
	}
	return strings.TrimSpace(decoded.Bot.OpenID), nil
}
