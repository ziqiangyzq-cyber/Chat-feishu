# 快速开始

## 方式一：一条命令安装最新正式版

```bash
curl -fsSL https://raw.githubusercontent.com/kxn/codex-remote-feishu/master/install-release.sh | bash
```

```powershell
irm https://raw.githubusercontent.com/kxn/codex-remote-feishu/master/install-release.ps1 | iex
```

这条命令会自动：

1. 识别当前平台
2. 下载 GitHub 构建好的 release 包
3. 解压到本地 release 缓存目录
4. 安装稳定路径下的 `codex-remote`
5. 启动本地 daemon 并打印 WebSetup 地址

如果你想固定到某个版本：

```bash
curl -fsSL https://raw.githubusercontent.com/kxn/codex-remote-feishu/master/install-release.sh | bash -s -- --version v1.0.0
```

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/kxn/codex-remote-feishu/master/install-release.ps1))) -Version <version>
```

如果你想安装某个 prerelease track 的最新版本，例如 `beta`：

```bash
curl -fsSL https://raw.githubusercontent.com/kxn/codex-remote-feishu/master/install-release.sh | bash -s -- --track beta
```

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/kxn/codex-remote-feishu/master/install-release.ps1))) -Track beta
```

## 方式二：下载 release 压缩包

1. 从 GitHub Releases 下载适合你平台的压缩包
2. 解压
3. 执行：

macOS / Linux：

```bash
./codex-remote install -bootstrap-only -start-daemon
```

Windows PowerShell：

```powershell
.\codex-remote.exe install -bootstrap-only -start-daemon
```

## 在 WebSetup 里完成首次配置

daemon 启动后，打开命令输出里的 `/setup` 地址。

推荐顺序：

1. 添加或验证飞书应用凭据
2. 看一下这台机器的运行环境检查结果
3. 如有需要，开启自动启动
4. 直接开始使用默认 `normal` 模式
5. 只有在你明确需要“飞书跟着编辑器当前焦点走”时，再去处理 VS Code 接入

当前默认推荐路径是：先用 `normal` 模式，再按需启用 VS Code 跟随能力。

## Linux `systemd --user` 常驻服务

如果你希望 Linux 上的 daemon 由长期运行的用户服务托管，而不是依赖 detached 进程：

```bash
codex-remote service install-user
codex-remote service enable
codex-remote service start
codex-remote service status
```

如果你希望系统重启后在没有手工打开终端的情况下也能恢复，需要额外执行：

```bash
loginctl enable-linger "$USER"
```

## 升级到当前 track 的最新版本

对于已经接入完成的用户，当前推荐的升级入口统一是：

```text
/upgrade latest
```

在飞书里发送这条命令即可。它适合：

- 检查是否有可用新版本
- 开始升级到当前 track 的最新 release
- 继续上一次未完成的升级

## 开始在飞书里使用

在测试前先确认：

- 飞书应用已经开通 `deploy/feishu/README.md` 里列出的基础消息 / 事件权限
- 如果你希望本地 `.md` 链接自动变成飞书预览链接，还需要 `drive:drive`
- 如果你希望在飞书里用 `/cron` 打开当前实例的定时任务多维表格，还需要 `bitable:app`

然后在飞书里：

- 如果你想在同一个聊天窗口里并行推进多个任务，用标签页：`/tab new` 新建一条并行会话线（自动弹出工作区选择卡），`/tab <编号>` 来回切换，`/tab` 查看列表。切换标签页不会打断其他标签页正在执行的任务，各自的过程卡与结果仍回复在自己的源消息下
- 默认先走 `normal` 模式：用 `/list` 选工作区，再 `/use` 继续已有会话，或者 `/new` 新开一个干净会话
- 如果当前还没有合适的工作区，可以在 `/list` 的目标卡里直接点“添加工作区”：接入已有目录，或导入 Git 仓库
- 如果只是想快速回到最近对话，直接发 `/use`
- 如果你想把补充说明直接并进当前正在执行的这一轮，可以直接回复那条源消息，或者发送 `/steerall`
- 如果你需要主动整理当前会话上下文，可以发送 `/compact`
- 如果你想把当前工作区里的某个文件直接发回飞书聊天，可以发送 `/sendfile`
- 只有当你明确想让飞书跟着编辑器当前焦点变化时，才切到 `vscode` 模式
- 如果你一时记不住命令，先发 `/help` 或 `/menu`
- 如果你想让当前 daemon 实例按计划后台执行任务，可以先发 `/cron` 配置任务表；任务既可以绑本地工作区，也可以绑 Git 仓库来源，编辑后再发 `/cron reload` 生效
- `/list` 在 `normal` 模式下显示工作区，在 `vscode` 模式下才显示在线 VS Code 实例
- `/use` 用来继续最近可见会话；`/threads` 仍可作为旧别名使用；`/useall` 会显示全部可见会话
- 优先使用卡片按钮；如果卡片提示已经过期，直接重发对应命令
- 最终回复会回在触发它的那条消息下面，群聊里更容易看懂上下文
- 如果一条文字还在排队，而当前已经有一条回复在执行，可以给这条排队文字点 `ThumbsUp`，把它升级成对当前执行回复的跟进
- 过程里如果你想少看点中间过程消息，可以发 `/verbose quiet`；想看更完整的共享过程卡，可以发 `/verbose verbose`
- `/detach` 会断开当前接管，并取消正在等待的后台恢复
- 如果你需要编辑器跟随行为，使用 `/mode vscode`，然后 `/list`，最后 `/follow`
- 默认执行权限是 `full`；如果你暂时想改成确认模式，可以发送 `/access confirm`
