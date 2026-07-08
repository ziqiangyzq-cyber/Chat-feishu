# Messaging Channels

> Type: `general`
> Updated: `2026-07-08`
> Summary: 记录 Feishu / WeCom 多通道 surface 架构、配置和能力声明规则。

Codex Remote uses a channel-neutral surface contract to connect one daemon to one or more IM backends.

## Current channels

| Channel | Status | Inbound | Outbound | Configuration |
| --- | --- | --- | --- | --- |
| Feishu / 飞书 | Mature primary channel | Text, image, card callbacks, reactions | Text, cards, files, images, videos, Feishu Drive preview links | WebSetup / Admin UI, `config.json` `feishu.apps[]`, env overrides |
| WeCom / 企业微信 | Optional second channel | aibot text messages, template card callbacks | Text, Markdown, target picker cards, request confirmation cards | `config.json` `wecom`, env overrides |

## Surface namespace

Every remote conversation is keyed by a surface session id. Channel ownership is encoded in the gateway namespace:

- Feishu: `feishu:<gateway>:<scope>:<id>`
- WeCom: `wecom:bot:chat:<chatid>`

The daemon routes outbound events by resolving the surface's gateway id. A WeCom surface is delivered only through the WeCom channel; a Feishu surface is delivered only through the Feishu gateway. This prevents cross-channel leakage when both channels are enabled.

## Runtime configuration

Feishu remains the default product setup path. It is configured through WebSetup / Admin UI and persisted under:

```json
{
  "feishu": {
    "apps": [
      {
        "id": "main",
        "name": "Main",
        "appId": "cli_xxx",
        "appSecret": "secret",
        "enabled": true
      }
    ]
  }
}
```

WeCom is configured as an optional second channel:

```json
{
  "wecom": {
    "enabled": true,
    "botId": "YOUR_WECOM_BOT_ID",
    "secret": "YOUR_WECOM_SECRET"
  }
}
```

Environment variables can override runtime credentials:

```bash
FEISHU_GATEWAY_ID=main
FEISHU_APP_ID=cli_xxx
FEISHU_APP_SECRET=secret
WECOM_BOT_ID=bot_xxx
WECOM_SECRET=secret
```

WeCom environment variables intentionally override `config.json`, including a disabled `wecom.enabled=false`, so service managers can inject secrets without editing disk config.

## Capability rules

Channels must report only implemented capabilities through `surface.Capabilities`.

- Do not set `Streaming=true` until the channel can update a single logical message incrementally.
- Do not set `FileSend=true` until the channel can upload and deliver file attachments.
- If a channel cannot combine text and interactive controls in one message, set `InteractiveSameFrame=false` and render separate frames.

This keeps the core renderer from selecting paths the concrete adapter cannot actually deliver.

## Adding another channel

1. Implement `surface.Channel`.
2. Define an adapter-local operation type implementing `surface.Operation`.
3. Map inbound platform events to `control.Action`.
4. Project `eventcontract.Event` to platform-native outbound frames.
5. Reserve a unique gateway namespace.
6. Add daemon routing tests proving the new channel and Feishu do not cross-deliver.
7. Add focused adapter tests for callback round-trips and capability declarations.
