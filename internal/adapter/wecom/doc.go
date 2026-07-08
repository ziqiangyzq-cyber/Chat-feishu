// Package wecom is the WeCom (企业微信 / WeChat Work) aibot channel adapter. It
// implements the channel-neutral internal/core/surface.Channel contract so the
// daemon can bridge the AI CLI to a WeCom aibot in addition to Feishu.
//
// This package is a Phase-1 foundation: the connection scaffolding (wss dial,
// subscribe, ping/pong) is real and correct, while message-handling bodies are
// clearly-marked TODOs that return safely. It is NOT yet wired into the daemon.
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
//   - aibot_respond_msg        — a new outbound message.
//   - aibot_respond_update_msg — an update to a previously sent message
//     (used for streaming: successive updates carry the same stream.id and a
//     finish flag on the terminal update).
//   - Keepalive: the client sends a WebSocket ping every 30s and expects pongs;
//     a missed pong indicates a dead connection that must be re-dialed.
//   - Streaming: outbound text can be streamed by sending an initial
//     aibot_respond_msg followed by aibot_respond_update_msg frames sharing a
//     stream identifier (stream.id) until a frame with finish=true.
//   - Interactivity: buttons/actions use template_card messages.
//
// # Key constraint (drives surface.Capabilities.InteractiveSameFrame=false)
//
// WeCom cannot place streaming text and interactive buttons in the SAME
// message. A streamed text reply and a template_card with buttons must be sent
// as two separate messages. This differs from Feishu, where a single card can
// both stream markdown and host action buttons.
package wecom
