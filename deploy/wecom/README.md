# WeCom / 企业微信通道配置

WeCom 通道使用企业微信 aibot 长连接。它是可选第二通道：不配置时 daemon 仍按 Feishu-only 模式运行；配置后 Feishu 和 WeCom 可以同时接入同一台 `codex-remote` daemon。

## 需要准备

在企业微信管理后台准备一个可用的 aibot，并取得：

- `botId`
- `secret`

WeCom 通道通过长连接 `wss://openws.work.weixin.qq.com` 接收消息，不使用 Feishu 的 WebSetup 自动配置流程。

## 推荐配置方式

编辑统一配置文件：

```bash
$EDITOR ~/.config/codex-remote/config.json
```

加入或更新：

```json
{
  "wecom": {
    "enabled": true,
    "botId": "YOUR_WECOM_BOT_ID",
    "secret": "YOUR_WECOM_SECRET"
  }
}
```

然后重启 daemon：

```bash
launchctl kickstart -k gui/$(id -u)/com.codex-remote.service
```

Linux `systemd --user`：

```bash
systemctl --user restart codex-remote.service
```

## 环境变量覆盖

如果你更希望由服务管理器注入密钥，也可以设置：

```bash
WECOM_BOT_ID=YOUR_WECOM_BOT_ID
WECOM_SECRET=YOUR_WECOM_SECRET
```

环境变量优先级高于 `config.json`。只要任一 WeCom 环境变量存在，运行时就按环境变量解析 WeCom 凭据。

## 当前能力

- 支持企业微信文本消息进入 Codex Remote。
- 支持基础文本、Markdown、计划更新、目标选择卡、确认/拒绝请求卡输出到企业微信。
- 支持 WeCom surface namespace，Feishu 和 WeCom 会话不会互相串线。
- 支持长连接自动重连。

当前暂不声明支持：

- streaming 单消息增量更新
- 文件发送
- 企业微信侧 WebSetup 自动配置

这些能力没有实现前，项目不会在 `surface.Capabilities` 中声明可用，避免上层误走未实现路径。

