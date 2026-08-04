# 飞书与企业微信独立 systemd 部署

这套部署让一个独立的 `chat-feishu-codex` daemon 同时承载飞书和企业微信通道。两个通道共享同一套编排能力，但凭据、Codex 身份和运行状态与登录用户完全隔离。

## 设计边界

- system service 使用独立的 `chat-feishu-codex` 系统用户，不借用操作员的 `$HOME`。
- `HOME`、`CODEX_HOME` 和全部 XDG 目录固定在 `/var/lib/chat-feishu-codex` 下。
- LLM、飞书和企业微信密钥只从 `/etc/chat-feishu-codex/chat-feishu-codex.env` 注入，不写入 Git。
- `config.json` 只保存非密钥配置。飞书与企微均使用长连接，不需要为两个通道分别运行 daemon。
- 二进制和部署模板可以重复发布；升级默认保留 env/config，并在 root-only 目录建立可回滚快照。
- relay/admin 默认只监听 loopback。确需远程访问时应由反向代理、隧道和独立鉴权负责。

## 准备配置

```bash
cp deploy/linux-systemd/chat-feishu-codex.env.example /tmp/chat-feishu-codex.env
cp deploy/linux-systemd/config.example.json /tmp/chat-feishu-codex-config.json
chmod 600 /tmp/chat-feishu-codex.env
```

编辑 env 文件：

- `OPENAI_API_KEY` 必填。
- 启用飞书时，`FEISHU_GATEWAY_ID`、`FEISHU_APP_ID`、`FEISHU_APP_SECRET` 必须同时填写。
- 启用企微时，`WECOM_BOT_ID`、`WECOM_SECRET` 必须同时填写。
- 至少启用一个消息通道。

飞书的事件、卡片回调和权限清单继续按 [`../feishu/README.md`](../feishu/README.md) 配置；企业微信 aibot 按 [`../wecom/README.md`](../wecom/README.md) 配置。

## 安装、升级和回滚

```bash
sudo scripts/deploy/chat-feishu-codex.sh install \
  --binary ./bin/codex-remote \
  --codex-binary /path/to/codex \
  --codex-config /path/to/config.toml \
  --env-file /tmp/chat-feishu-codex.env \
  --config-file /tmp/chat-feishu-codex-config.json
```

升级默认保留服务器上的密钥和业务配置：

```bash
sudo scripts/deploy/chat-feishu-codex.sh upgrade --binary ./bin/codex-remote
```

Codex CLI 会先检查 `app-server` 能力，再复制到 root 管理的固定路径；Codex 的 `config.toml` 也会安装到独立 `CODEX_HOME`，避免误用操作员的配置或登录态。升级 Codex 本身时额外传 `--codex-binary /path/to/new/codex`；调整 provider 时传 `--codex-config /path/to/new-config.toml`。只有明确需要轮换配置时，才在 upgrade 时再次传入 `--env-file` 或 `--config-file`。部署会等待 daemon HTTP、托管 Codex headless instance、所有已配置飞书/企微通道以及 PID/restart 稳定窗全部通过；失败会自动恢复部署前快照。也可以手工回滚最近一次事务：

```bash
sudo scripts/deploy/chat-feishu-codex.sh rollback
```

检查生成路径和服务状态：

```bash
sudo scripts/deploy/chat-feishu-codex.sh status
journalctl -u chat-feishu-codex.service -f
```

不要直接修改生成的 unit 或 wrapper。需要调整模板时先提交仓库，再通过 upgrade 发布；服务器 env/config 是持久数据，不随代码版本替换。

## 参考方案带来的取舍

- 多平台机器人项目通常让多个 IM adapter 共用一个持久化 gateway，而不是为每个平台复制整套进程；本项目沿用该边界。
- 同类容器部署会把状态目录挂成 persistent volume；host-native systemd 对应做法是把状态放在独立 service home，发布只切换二进制。
- 飞书官方工具推荐用环境变量隔离服务端密钥；这里进一步用权限受限的 systemd `EnvironmentFile` 管理。
- 没有默认采用 Docker Compose：当前仓库已有原生二进制、systemd 和事务升级基础，额外容器层会引入 Codex CLI、工作区挂载和宿主权限映射问题。以后需要集群化时可另加容器部署目标，但不与本机事务脚本混用。

参考资料：

- [LangBot：一套 gateway 支持飞书、企业微信等多个平台](https://github.com/langbot-app/LangBot)
- [DeerFlow：通道密钥放入 `.env`，状态目录持久化](https://github.com/bytedance/deer-flow)
- [飞书官方 OpenAPI MCP：服务端凭据优先使用环境变量](https://github.com/larksuite/lark-openapi-mcp/blob/main/docs/usage/configuration/configuration.md)
- [飞书官方 Node SDK：长连接模式及单应用多实例限制](https://github.com/larksuite/node-sdk)
- [systemd.exec：服务身份、目录保护、环境与凭据机制](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html)
