// Package wecom is the WeCom (企业微信 / WeChat Work) aibot channel adapter. It
// implements the channel-neutral internal/core/surface.Channel contract so the
// daemon can bridge the AI CLI to a WeCom aibot in addition to Feishu.
//
// Status: wired into the daemon as an opt-in second channel (WECOM_BOT_ID +
// WECOM_SECRET / config.json wecom block). Feishu and WeCom surfaces use
// separate gateway namespaces so outbound events do not cross-deliver.
//
// # aibot long-connection protocol
//
// WeCom aibots receive events over a persistent WebSocket ("long connection")
// rather than HTTP webhooks. The high-level shape:
//
//   - Endpoint: wss://openws.work.weixin.qq.com — a long-lived WebSocket.
//   - Auth: a Bot ID + Secret pair identifies and authenticates the aibot.
//   - After dialing, the client sends an aibot_subscribe frame carrying the Bot
//     ID/Secret to register for that bot's event stream.
//   - The server then pushes frames the client dispatches by type:
//   - aibot_msg_callback   — an inbound user message to the bot.
//   - aibot_event_callback — a non-message event (e.g. menu click, enter chat).
//   - The client responds by sending:
//   - aibot_respond_msg        — a new outbound message (or stream open).
//   - aibot_respond_update_msg — an update to a previously streamed message
//     (successive updates share stream.id; finish=true ends the stream).
//   - aibot_send_msg           — proactive outbound message without req_id.
//   - Keepalive: the client sends a WebSocket ping every 30s.
//   - Streaming: Channel implements surface.StreamingRenderer; non-final
//     assistant blocks and RenderStream updates share a stream id per chat.
//   - Interactivity: buttons/actions use template_card messages.
//
// # Capabilities
//
//   - Streaming: true (markdown stream open/update/finish)
//   - InteractiveSameFrame: false (text + buttons must be two messages)
//   - FileSend: false (binary upload not wired; image.output degrades to a
//     markdown path notice)
//
// # Slash commands
//
// Inbound text is parsed with the same control.ParseFeishuTextActionWithoutCatalog
// catalog as Feishu, so `/stop` `/status` `/new` `/compact` `/help` and the rest
// of the shared command set work on both channels.
//
// # Key constraint (drives surface.Capabilities.InteractiveSameFrame=false)
//
// WeCom cannot place streaming text and interactive buttons in the SAME
// message. A streamed text reply and a template_card with buttons must be sent
// as two separate messages. This differs from Feishu, where a single card can
// both stream markdown and host action buttons.
package wecom
