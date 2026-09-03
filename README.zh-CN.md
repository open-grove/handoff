# Handoff

[English](README.md) | 简体中文

[![Release](https://img.shields.io/github/v/release/open-grove/handoff)](https://github.com/open-grove/handoff/releases/latest)
[![Release workflow](https://github.com/open-grove/handoff/actions/workflows/release.yml/badge.svg)](https://github.com/open-grove/handoff/actions/workflows/release.yml)
[![License](https://img.shields.io/github/license/open-grove/handoff)](LICENSE)

把一段 Agent 对话整理成可读、可分享、可继续工作的 `HANDOFF.md`。

Handoff 支持 Codex、Claude Code、Pi 和 OpenCode。它只读来源 Session，生成一份模型无关的不可变快照，并同时提供适合人阅读的分享页与适合 Agent 接手的结构化上下文。

## 特点

- **一次命令完成交接**：自动发现当前工作区的 Agent Session。
- **面向人和 Agent**：同一份 Handoff 同时包含人类摘要与 Agent 技术上下文。
- **不绑定模型**：接收方无需使用相同 Agent、模型或会话系统。
- **来源只读**：不会 resume、compact 或修改原 Session。
- **默认最小上传**：通常只向 Handoff 服务发送生成后的 sections。
- **无需账号**：创建和读取均可匿名完成。
- **永久可用**：Handoff 不会自动过期，直到创建者或管理员显式删除。
- **适合直接阅读**：分享页支持 Markdown 与 LaTeX 公式渲染。

## 安装

macOS 和 Linux：

```bash
curl -fsSL https://raw.githubusercontent.com/open-grove/handoff/main/install.sh | sh
```

安装脚本会下载最新版、校验 SHA-256，并把 CLI 安装到 `~/.local/bin/handoff`。它还会为 Codex、Claude Code 和兼容 Agent 安装同版本的 Handoff Skill。

如果 `~/.local/bin` 不在 `PATH` 中，请将它加入 shell 配置。也可以设置 `HANDOFF_INSTALL_DIR` 更改安装目录：

```bash
curl -fsSL https://raw.githubusercontent.com/open-grove/handoff/main/install.sh \
  | HANDOFF_INSTALL_DIR=/usr/local/bin sh
```

Windows 用户可从 [GitHub Releases](https://github.com/open-grove/handoff/releases/latest) 下载预编译包。发行包附带 `SHA256SUMS`。

更新已有安装：

```bash
handoff update
```

## 快速开始

在 Codex、Claude Code、Pi 或 OpenCode 的项目目录中运行：

```bash
handoff create "继续完成 CLI 部署" --intent continue
```

默认生成需要至少一个受支持的 Agent CLI 已安装并完成认证。Handoff 会自动发现来源 Session，启动一个隔离的本机 Agent 旁路生成交接内容，然后返回可直接发送的消息：

```markdown
🖐️ **For Human**

你收到一份 Handoff，请打开[继续完成 CLI 部署](https://handoff.openmau.com/h/<code>)查看。

🤖 **For Agent**

请使用 OpenGrove Handoff 读取：`handoff:<code>`
```

接收方可以打开分享页，也可以让 Agent 直接读取：

```bash
handoff receive 'handoff:<code>'
```

创建与接收都不需要登录。旧的 `opengrove-handoff:<code>` 引用仍可读取。

## 常见用法

### 分享讨论结果

适合传递结论、理由、示例和已经排除的方案：

```bash
handoff create "MCP App 架构讨论" --intent share
```

### 交接未完成工作

适合让另一个人或 Agent 接着做：

```bash
handoff create "继续修复 MCP App 兼容性" --intent continue
```

### 原样发布准备好的 Markdown

当 Prompt、URL、校验值、表格或代码块需要保留原结构时，使用 `preserve`。它不会启动第二个 Agent 改写内容：

```bash
handoff create "长流测试方法" \
  --intent share \
  --generator preserve \
  --file ./prepared-method.md
```

也可以从 stdin 读取：

```bash
some-agent-export | handoff create "调查结果" --intent share --generator preserve
```

### 附带完整可读上下文

默认只发布生成后的 sections。只有确实需要完整对话时才显式添加：

```bash
handoff create "继续排查线上问题" --intent continue --attach-context
```

附件是经过尽力脱敏的可读 Canonical Context，不是原始 Session，也不包含 thinking 或原始工具结果。它会与 Handoff 一起永久保存，直到显式删除。

接收方按需读取附件：

```bash
handoff context 'handoff:<code>'
```

### 只交付同机 Session 路径

如果接收 Agent 与发送方在同一台机器上，可以完全绕过上传：

```bash
handoff session locate --goal "继续完成剩余实现"
```

这条命令只支持具有独立 Session 文件的 Codex、Claude Code 和 Pi。输出的原始文件未脱敏，不应发送到公开或跨设备渠道。

### 发布前检查

```bash
# 查看来源、旁路 Agent 和上传范围，不生成也不发布
handoff create "下一步" --dry-run

# 在编辑器中检查生成结果，保存后再发布
handoff create "下一步" --review
```

## 工作原理

```text
Codex / Claude Code / Pi / OpenCode / file / stdin
                         │
                         ▼
              read-only Session snapshot
                         │
                         ▼
        normalize + best-effort redact readable context
                         │
                ┌────────┴────────┐
                ▼                 ▼
       local Agent sidecar     preserve Markdown
                └────────┬────────┘
                         ▼
                  immutable Handoff
                    │           │
                    ▼           ▼
                share page   HANDOFF.md
```

默认的 `agent` generator 会启动一个全新、隔离的本机 Agent CLI。它沿用该 CLI 已有的认证、provider 和默认模型，但不会继续或修改来源 Session。Handoff 服务本身不调用模型。

如果本机找不到受支持的 Agent CLI，只有通过 `--file` 或 stdin 提供了明确范围的内容时，CLI 才会使用有限的 deterministic 备用提取。已找到 Agent CLI 但调用或生成失败时，命令会直接返回真实错误。

## 核心概念

### 交接目的

| Intent | 用途 | 主要内容 |
|---|---|---|
| `share` | 分享一次讨论最终弄明白了什么 | 结论、推理、证据、示例与取舍 |
| `continue` | 让接收方继续未完成工作 | 背景、当前状态、文件、下一步与未决问题 |
| `auto` | 让 Agent 根据上下文判断 | 默认值；目的明确时建议显式选择 |

### 输入、生成与持久化

| Control | 作用 |
|---|---|
| `--source` / `--file` / stdin | 选择输入内容 |
| `--generator agent|preserve` | 选择由本机旁路 Agent 生成，或保留准备好的 Markdown |
| `--runtime` | 仅为 `agent` generator 选择本机旁路 CLI |
| `--attach-context` | 独立决定是否持久化完整的脱敏可读上下文 |

普通使用无需设置这些选项：`source=auto`、`generator=agent`、`runtime=auto`，且不附带完整 Context。

## 隐私与安全

- Canonical Context 只包含规范化后的可读 user/assistant 内容；thinking、原始工具结果和 provider 内部记录不会进入 Handoff。
- CLI 会尽力移除 API key、Bearer token、密码、私钥、本机用户路径、邮箱和 IP；自然语言中的所有身份信息无法保证完全识别。
- 本机旁路 Agent 如果使用云模型，脱敏后的上下文仍会发送给该 CLI 已配置的模型服务商。
- 默认情况下，Handoff 服务只接收最终 sections；`--attach-context` 是持久化完整可读上下文的唯一显式开关。
- 分享 URL 和 `handoff:<code>` 都是读取凭证，拿到它的人可以读取该 Handoff。
- 每次创建都会生成独立删除凭证。明文只保存在创建者本机，服务端仅保存 SHA-256 哈希。
- Handoff 不会自动过期。需要删除时运行 `handoff delete <reference> --yes`。

安全问题请通过仓库 Security 页面私下报告，详见 [SECURITY.md](SECURITY.md)。

## 命令参考

| Command | Description |
|---|---|
| `handoff create "topic or goal"` | 生成并发布 Handoff |
| `handoff receive <reference>` | 读取 Handoff |
| `handoff context <reference>` | 读取显式附带的完整可读 Context |
| `handoff session locate` | 返回同机可用的原始 Session 路径 |
| `handoff delete <reference> --yes` | 永久删除 Handoff |
| `handoff doctor [--offline]` | 检查 Session 发现、Agent CLI 和服务连通性 |
| `handoff update [--check]` | 检查或安装经过校验的新版本 |
| `handoff schema [action]` | 输出机器可读的命令合同 |
| `handoff skills list/read/install` | 查看或安装内嵌 Agent Skill |
| `handoff config show/set-server` | 查看配置或切换服务地址 |
| `handoff admin login/status/logout` | 管理自托管服务的管理员凭据 |

所有命令都支持 `--json` 或 `--format text|json`。完整参数以 CLI 为准：

```bash
handoff --help
handoff create --help
handoff schema create
```

## Agent 集成

安装或修复内嵌 Skill：

```bash
handoff skills install
```

Skill 会指导 Agent 判断 `share` 与 `continue`、控制上下文范围，并以机器可读模式调用 CLI。已有自定义 Skill 时不会自动覆盖；如需替换，显式运行：

```bash
handoff skills install --force
```

Agent 调用 `create` 时应添加 `--json`，并把返回的 `share_message` 原样交给用户；删除凭证等创建者私有状态不应进入分享消息。

在 macOS 和 Linux 的 Agent 环境中，`create`、`receive`、`context` 和 `session` 最多每 24 小时执行一次更新预检。更新经过 SHA-256 校验，失败不会阻断当前任务。设置 `HANDOFF_NO_AUTO_UPDATE=1` 可关闭自动安装。

## 自托管

CLI 默认使用 `https://handoff.openmau.com`。创建时可通过 profile 或 `HANDOFF_SERVER` 切换服务；`receive` 和 `context` 也可直接接受完整分享 URL。

### Go 服务

```bash
cp .env.example .env
set -a; . ./.env; set +a
go run ./cmd/handoffd
```

`handoffd` 默认监听 `127.0.0.1:7391`，并把数据写入 `HANDOFF_DATA_DIR`。生产环境可使用仓库中的 [Dockerfile](Dockerfile)、[systemd unit](deploy/handoff.service) 和 [Caddyfile](deploy/Caddyfile)。不要直接向公网开放 7391。

### Cloudflare Worker

线上分享页也可部署为 Cloudflare Worker + D1。首次部署时，先在 `wrangler.jsonc` 中配置自己的 D1 数据库和域名，然后执行：

```bash
npm ci --prefix cloudflare
npm test --prefix cloudflare

cd cloudflare
npx wrangler d1 migrations apply HANDOFF_DB --remote
npx wrangler deploy
```

从 protocol v6 升级到 v7 时，必须先部署新 Worker，再应用 `0004_permanent_handoffs.sql`。这样旧 Worker 不会在迁移窗口中把已经永久化的记录当成过期数据清理。

### HTTP API

```text
GET    /healthz
GET    /v1/schema/create
POST   /v1/handoffs
GET    /v1/handoffs/:id
GET    /v1/handoffs/:id/context
DELETE /v1/handoffs/:id
GET    /h/:id
GET    /h/:id.md
```

发布和读取无需登录。删除时使用创建者的 `X-Handoff-Delete-Token`，或自托管服务配置的管理员 Bearer token。

## 开发

需要 Go 1.24+ 和 Node.js 20+：

```bash
make test
make test-worker
make build
./bin/handoff --help
```

Worker 打包检查：

```bash
cd cloudflare
npx wrangler deploy --dry-run
```

## 项目边界

Handoff 是一次性的不可变交接，不是实时协作系统。当前不提供组织成员管理、细粒度权限、群聊、原生 Session 恢复或多人并发编辑。

## 许可证

[Apache License 2.0](LICENSE)
