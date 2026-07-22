# Unified Local Release Runbook

> Type: `general`
> Updated: `2026-07-22`
> Summary: 定义三套本地 relay stack 共用一个不可变构建产物的审计、预检、部署、回滚与 canonical checkout 操作规范。

## 1. 目标与边界

本流程只管理同一用户下的三套本地 stack：

- `codex-remote`
- `codex-remote-2`
- `claude-remote`

三套 stack 继续使用各自的 XDG config/data/state root、凭证和运行状态。统一的是二进制，不是配置或数据。operator 不读取、输出或复制配置和凭证。

入口只有一个：

```bash
./deploy-local-release.sh <audit|preflight|deploy|rollback|canonical-checkout>
```

它与 `./upgrade-local.sh`、`./upgrade-self.sh` 的边界不同：后两者只管理一个已有 `install-state.json` 的安装实例；本流程从 systemd user unit 发现实际 `ExecStart`，再以显式 manifest 管理完整三套 fleet。已经迁入 unified layout 的路径会带 ownership marker，普通 install、repair 和单实例 upgrade 不得覆盖它。

## 2. 不变量与目录布局

一次 deploy 只构建一个 binary，并计算一个 SHA-256。成功后所有 allowlisted unit 的 `ExecStart` 路径必须解析到同一个真实文件、同一个 inode、同一个 SHA-256 和同一个 full commit。

默认布局：

```text
~/.local/share/codex-remote-unified/
  releases/<full-commit>-<sha256>/
    codex-remote
    .codex-remote-unified-release
  stacks/codex-remote/current -> <release-root>/releases/<release-id>
  stacks/codex-remote-2/current -> <release-root>/releases/<release-id>
  stacks/claude-remote/current -> <release-root>/releases/<release-id>
  transactions/<transaction-id>/
  operator.log
  deploy.lock
```

每个 unit 原有的 `ExecStart` binary path 会变成指向对应 stack `current/codex-remote` 的 symlink。`claude-remote.service` 和 `claude-remote-site.service` 即使保留两个兼容入口名，也只能是指向同一 `current` 的 symlink，不能再各自持有 regular-file copy。

release 目录在发布后只读，名称同时绑定 full commit 和 binary hash。`.codex-remote-unified-release` 记录 version、commit、UTC build time、dirty state 和 SHA-256。部署只接受 `dirty=false`。

## 3. Manifest 合同

默认 allowlist 是 [`deploy/local-stacks.tsv`](../../deploy/local-stacks.tsv)。每行固定声明：

```text
stack | xdg_identity | health_base_url | unit | role | start_order | allowed_exec_paths | lifecycle
```

operator 会同时读取 `systemctl --user list-unit-files` 和 `list-units`，解析每个 unit 的有效 `ExecStart`，再与 manifest 对照。以下任一情况都 fail closed：

- 发现名称或 binary path 像本产品、但不在 manifest 的 unit；
- manifest unit 不存在、未加载、systemd `Id` 不精确匹配或 `ExecStart` 无法唯一解析；
- `ExecStart` 不在该 unit 的 allowed path 集合；
- 一个 path 被两个隔离 stack 共用；
- 两个 stack 共用同一个 health origin；
- stack 缺少 daemon 或 site role；
- transaction 开始前任一 allowlisted unit 不符合显式 `active/inactive` lifecycle。

`xdg_identity` 当前只是 manifest 内的唯一 stack 身份标签。operator 不解析 unit 的 `Environment` 或 `EnvironmentFile`，因此它不能替代人工核对实际 XDG root。它也不会修改这些环境。首次迁移前必须由 operator/Hermes 核对三套 unit 的 XDG config/data/state root 仍彼此隔离。

默认 health origin `9501`、`9701`、`9601` 已按当前现场监听器核对，但仍不是自动发现值。首次执行前必须再次对照实际 unit/config 验证；如不一致，先更新 manifest 并走 review，不能临时绕过 health check。

## 4. Source 与版本溯源

统一 build helper 是：

```bash
scripts/build/build-codex-remote.sh
```

它是仓库内唯一组装 `codex-remote` ldflags 的地方。`upgrade-local.sh`、`upgrade-self.sh`、release packaging 和 unified deploy 都复用它。部署构建要求：

- checkout `HEAD` 精确等于 full commit 或 exact tag；
- worktree 包括 untracked files 在内完全干净；
- shipping/alpha build 使用已存在且精确指向 `HEAD` 的同名 semantic version tag；
- build 总是在临时 snapshot 内进行；clean source 只来自目标 commit 的 `git archive`，embed generation 不修改 canonical checkout；
- host build 的 `--version-detail` 与预期 metadata 完全一致，`--version` 继续保持旧兼容输出。

兼容命令 `codex-remote version` 仍只输出原版本字符串。精确诊断使用：

```bash
codex-remote --version-detail
```

输出为一行 machine-readable metadata：

```text
codex-remote version=<semver> commit=<full-commit> built_at=<UTC> dirty=false branch=<label> flavor=<flavor>
```

直接 `go build ./cmd/codex-remote` 只适合本地 debug，会显示未证明的 provenance，不能部署。

## 5. 标准操作

先从 canonical checkout 切到 exact tag。正式发布优先使用 reviewed annotated tag：

```bash
./deploy-local-release.sh canonical-checkout --ref vX.Y.Z
cd ~/deploy/Chat-feishu
```

只读审计不会 build、写路径或控制服务：

```bash
./deploy-local-release.sh audit
```

迁移前 audit 预期可能返回 non-zero，因为 legacy binary hash/path 不一致。输出按 stack 包含 unit state、有效 `ExecStart`、resolved path/inode、SHA-256、`--version-detail` metadata，以及 stack/global consensus。

显式预检：

```bash
./deploy-local-release.sh preflight --ref vX.Y.Z
```

以 full commit 部署时必须同时给 semantic version：

```bash
./deploy-local-release.sh preflight \
  --ref <full-commit> \
  --version vX.Y.Z
```

预检会验证 source、inventory、writable paths、所有 unit 的运行状态，执行 repository checks 与 `go test ./...`，只构建一次并校验 metadata/hash；临时 artifact 在结果输出后删除，不会成为可部署缓存。`deploy` 会在事务内重新执行同一 preflight，不能把旧 preflight 结果当作部署授权：

```bash
./deploy-local-release.sh deploy --ref vX.Y.Z
```

部署和 rollback 会通过 `systemd-run --user` 进入独立 transient service，并使用 `--wait --collect --service-type=exec`。内部 recursion guard 只有在当前 cgroup unit 名、systemd `Id` 和 `Transient=yes` 同时匹配时才有效，调用者伪造环境变量不能跳过 re-exec。operator 日志写到 release root 的 `operator.log`。

发布事务还会对每个有效 `ExecStart` 路径持有 `<binary>.codex-remote-mutation.lock` 内核文件锁。旧单实例 upgrade/repair 使用同一把锁，并在拿锁后重新检查 unified ownership；因此等待中的旧 helper 不能在首次迁移后覆盖 symlink。任何锁竞争都会在停止服务前 fail closed。

成功后再次审计：

```bash
./deploy-local-release.sh audit
```

只有 `global_consensus=true` 才算收敛。

## 6. Transaction 与自动回滚

deploy 的顺序固定为：

1. tests + one clean build + provenance/hash validation；
2. 发布或复用 immutable release；
3. 捕获所有 current/alias 的 before state；
4. 在不改 live path 的前提下 stage 全部 current/alias links；
5. 按 reverse order 停止全部 daemon/site units，并确认 PID 已退出；
6. 原子发布全部 current links 和 alias links；
7. 验证所有 configured path 收敛到同一 artifact；
8. 按 manifest order 只启动 lifecycle=`active` 的daemon/site；lifecycle=`inactive` 的历史unit继续保持inactive；
9. 检查 active/running/result、PID/restart stability、running inode/hash/provenance，以及每套 stack 的 HTTP endpoints；
10. 全部通过后提交 transaction journal。

任一步失败都会尝试停止完整 fleet、恢复所有已变更路径、重启旧 fleet 并重新做 health check。原始失败码、rollback 结果和 cleanup 结果分别写入 transaction journal，rollback 失败不会覆盖原始失败码。不能确认全部 unit 已停止时，operator 拒绝继续改路径。

首次 migration 的失败事务可以自动恢复 legacy regular files，因为 before snapshot 仍在当前 transaction 内。已成功提交的首次 migration 不支持手动退回 legacy split-copy 状态；这会重新允许 Claude daemon/site 漂移，因此 `rollback` 会 fail closed。

后续 unified release 之间可以手动 rollback：

```bash
./deploy-local-release.sh rollback --transaction <transaction-id>
```

未指定 transaction 时选择最新 committed transaction。rollback 会先验证 active manifest、journal、release marker/hash/provenance 和当前 configured release；任何歧义都拒绝执行。

## 7. 首次迁移检查表

首次 live 操作由 Hermes 在 diff review 和生产侧核对后执行，本实现任务不执行这些动作。

1. 核对六个 unit、实际 `ExecStart`、三套 XDG root 和三个 health origin。
2. 确认 `deploy/local-stacks.tsv` 精确覆盖全部候选 unit，没有额外本产品 user service。
3. 从 accepted exact tag/commit 准备 canonical read-only checkout。
4. 保存 deploy 前 audit 输出。
5. 执行 preflight，再执行 deploy；不要并发 stop/start/restart 同一 daemon。
6. 保存 transaction id、artifact SHA-256、deploy 后 audit 和 `operator.log`。
7. 确认 Claude 两个入口都是 symlink，resolved path/inode/hash/commit 完全相同。
8. 确认三套配置、数据、状态和凭证仍在原隔离 root，未被复制或合并。

## 8. Canonical Checkout 与旧 worktree 收敛

目标 checkout 是 `~/deploy/Chat-feishu`。helper 只 init/fetch 所请求的 exact tag/full commit、detached checkout、验证 clean HEAD，然后把 working tree 和 `.git` 设为只读：

```bash
./deploy-local-release.sh canonical-checkout \
  --ref vX.Y.Z \
  --checkout "$HOME/deploy/Chat-feishu"
```

更新时重复同一命令；helper 会临时恢复 owner write permission，只接受自己写有 managed marker 的 standalone checkout，并拒绝 symlink、手工改动或 origin 不匹配，结束时重新设为只读。不要在这个 checkout 开发、merge 或手工改文件。

现有六套以上 worktree 不在本脚本的删除范围。收敛步骤是：先用 `git worktree list --porcelain` 盘点，再建立并验证 canonical checkout；确认没有未提交工作后，才由 Hermes 逐一归档或移除旧 worktree。不要用 broad recursive delete 代替 `git worktree remove` 和逐路径核对。

## 9. GitHub 与审计边界

本次收敛验收后的 GitHub 目标状态是：

- 默认分支只保留 `master`；
- 保留一个最终验收通过的 annotated release tag；
- Hermes 先创建、校验并外置保存包含历史 refs 的 git bundle；
- bundle 可恢复性和最终 tag/commit 均验证后，旧 remote feature/release branches 与旧 tags 才可删除。

这些 GitHub side effects 不属于 deploy operator。source-control publish 与本机部署始终是两个独立、可审计动作：push/tag 不自动调用 `deploy-local-release.sh`，deploy 也不 push、merge、打 tag 或删除 ref。未来 CI 继续产生 release/dev feed 时，也只能发布 source artifacts，不能隐式部署本地三套 stack。
