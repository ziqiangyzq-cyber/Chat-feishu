package daemon

import (
	"context"
	"log"
	"strings"

	"github.com/kxn/codex-remote-feishu/internal/core/eventcontract"
	"github.com/kxn/codex-remote-feishu/internal/core/surface"
)

// SetWeComChannel installs the OPTIONAL, opt-in WeCom (企业微信 aibot) second
// channel. It is called exactly once during startup (from entry.go) BEFORE
// Run, and only when WeCom credentials are configured. When it is never called,
// a.wecomChannel stays nil and every WeCom code path is a no-op branch, leaving
// the Feishu-only delivery path byte-identical.
//
// Passing a nil channel is treated as "not configured" and clears any prior
// value, so callers can guard purely on credential presence.
func (a *App) SetWeComChannel(channel surface.Channel) {
	a.wecomChannel = channel
}

// teeWeComEvent best-effort delivers a channel-neutral event to WeCom, ADDITIVE
// to (and never replacing) the Feishu delivery that already ran. It is a no-op
// when WeCom is unconfigured. Delivery happens in its own goroutine so it adds
// zero latency to, and cannot block, the Feishu hot path; all errors are
// logged and swallowed so a WeCom failure never affects Feishu.
//
// TODO(wecom Phase 4): chatID here is the Feishu chat identifier. Cross-channel
// session routing (mapping a Feishu surface/chat to the corresponding WeCom
// chat, and vice versa) is not yet implemented, so this tee only produces a
// correct WeCom message when the WeCom chat happens to share the same chatID.
// Until routing lands, the tee is wired-but-inert for distinct WeCom chats; it
// establishes the real fan-out structure without altering Feishu behavior.
func (a *App) teeWeComEvent(chatID string, event eventcontract.Event) {
	channel := a.wecomChannel
	if channel == nil {
		return
	}
	if strings.TrimSpace(chatID) == "" {
		return
	}
	go func() {
		if err := channel.Deliver(context.Background(), chatID, event); err != nil {
			log.Printf("wecom tee delivery failed (ignored): chat=%s kind=%s err=%v", chatID, event.Kind, err)
		}
	}()
}
