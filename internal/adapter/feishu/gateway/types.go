package gateway

import (
	"context"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/kxn/codex-remote-feishu/internal/core/agentproto"
	"github.com/kxn/codex-remote-feishu/internal/core/control"
)

type RoutingEnv struct {
	GatewayID            string
	SurfaceForCardAction func(messageID, chatID, operatorID string) string
}

type QuotedMessageInputs struct {
	Inputs []agentproto.Input
	Files  []control.ActionFileAttachment
}

type InboundEnv struct {
	GatewayID                        string
	LookupSurfaceMessage             func(messageID string) string
	ParseTextActionWithoutCatalog    func(text string) (control.Action, bool)
	QuotedInputs                     func(context.Context, *larkim.EventMessage) []agentproto.Input
	QuotedMessageInputs              func(context.Context, *larkim.EventMessage) QuotedMessageInputs
	ParsePostInputs                  func(context.Context, string, string) ([]agentproto.Input, string, error)
	BuildMergeForwardStructuredInput func(context.Context, *larkim.EventMessage) (string, []agentproto.Input, error)
	RecordSurfaceMessage             func(messageID, surfaceSessionID string)
	DownloadImage                    func(context.Context, string, string) (string, string, error)
	DownloadFile                     func(context.Context, string, string, string) (string, error)
	DeliverAsyncInboundFailure       func(context.Context, string, string, string, string, string)
	// ApplySurfaceSlot maps a base surface ID to the active virtual tab
	// surface for that chat (identity when nil / no tab is active).
	ApplySurfaceSlot func(baseSurfaceID string) string
	// HandleTabCommand executes a gateway-local /tab command. The action is
	// consumed by the gateway and never forwarded to the daemon core.
	HandleTabCommand func(ctx context.Context, req TabCommandRequest)
	// BotOpenID is the open_id of this app's bot. When set, group-chat inbound
	// messages that neither @mention the bot nor carry a command are ignored,
	// so the bot only reacts when it is explicitly called. Empty disables the
	// gate (fail open to the legacy "reply to everything" behavior).
	BotOpenID string
}

type ActionDispatcher func(context.Context, control.Action) error
