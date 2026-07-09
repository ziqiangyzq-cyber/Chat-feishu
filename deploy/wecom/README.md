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

## 当前能力（与飞书对齐情况）

| 能力 | 状态 | 说明 |
|------|------|------|
| 文本入站 / 出站 | ✅ | 单聊 / 群聊 |
| Markdown 回复 | ✅ | |
| 流式更新 | ✅ | `aibot_respond_msg` + `aibot_respond_update_msg` |
| 计划更新 | ✅ | Markdown checklist |
| 目标选择卡 | ✅ | button / dropdown template_card |
| 审批 / 确认卡 | ✅ | 按钮 + 正文 sections |
| 通用 slash 命令 | ✅ | `/stop` `/status` `/new` `/compact` `/help` 等与飞书同源解析 |
| 图片输出 | ⚠️ 最小集 | 以 Markdown 路径提示展示，**不上传二进制** |
| 文件发送 | ❌ | `FileSend=false`；`/sendfile` 可解析，企微侧无原生上传 |
| WebSetup 自动配置 | ❌ | 需手写 botId/secret |
| 会话 idle 清理 | ✅ | 默认 30 分钟清理 req_id / 流状态 |
| 单轮 MaxTurn | ✅ 可选 | `Config.MaxTurn` 超时后结束流并提示 |

## 常用命令（飞书 / 企微通用）

```
/stop      中断当前执行
/status    查看工作区与会话
/new       新开会话
/compact   压缩上下文
/help      完整帮助
/use       选择工作区 / 会话
```

## 尚未实现（勿在 Capabilities 中声明）

- 企业微信二进制图片 / 文件上传
- 企业微信侧 WebSetup 扫码配置
- 读超时 / pong 看门狗（连接仍依赖 ping + 读失败重连）

这些能力没有实现前，项目不会在 `surface.Capabilities` 中把 `FileSend` 设为 true，避免上层误走未实现路径。
