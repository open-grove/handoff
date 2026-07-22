# handoff

`handoff` 是一个给人和 Agent 用的通用上下文交接 CLI。它不绑定 OpenGrove，也不复制 Codex / Claude 的原生 Session；它把当前上下文变成一份可编辑、可追溯、模型无关的 `HANDOFF.md`。

```text
当前 Session（只读快照，不执行 /compact）
                 │
                 ▼
       handoff 本地脱敏 + 临时子调用
                 │  复用当前 Agent 的登录、配置、默认模型
                 ▼
         结构化 handoff sections
                 │  只上传压缩结果
                 ▼
        handoffd 存储 / 分享 HANDOFF.md
```

默认不需要配置模型、API key、provider 或模型地址。比如从 Codex 中调用时，CLI 会执行一次全新的 `codex exec --ephemeral`；它沿用 Codex 已有认证与默认模型，但不会 resume、compact 或改写来源 Session。Claude Code 和 Pi 使用同样的无状态子调用思路。

## 用起来

```bash
go install ./cmd/handoff

# 首次配置；Token 通过 stdin 读取，避免进入 shell history
printf '%s' "$HANDOFF_TOKEN" | \
  handoff auth login --server https://handoff.example.com --token-stdin

# 自动找当前工作区最近的 Codex / Claude / Pi Session
handoff create "让同事继续完成 CLI 部署"

# 可选：不调用 Agent，生成确定性本地摘要
handoff create "continue" --compact none

# 可选：明确同意把保留后的上下文交给 handoffd 压缩
handoff create "continue" --compact server

# 任何 Agent 都能用的通用入口
some-agent-export | handoff create "continue the investigation"
handoff create "continue" --file transcript.md --file decisions.md

# 接收方可以用分享码，也可直接粘贴分享 URL
handoff receive a-secure-share-code
handoff receive https://handoff.example.com/h/a-secure-share-code
handoff receive a-secure-share-code --output HANDOFF.md
```

Agent 可先查合同，不用猜参数：

```bash
handoff schema create
handoff create "next goal" --dry-run
handoff doctor
```

## CLI

| 命令 | 风险 | 作用 |
|---|---:|---|
| `handoff create "goal"` | write | 只读上下文，由当前 Agent 临时压缩，只上传 sections |
| `handoff receive <code-or-url>` | read | 读取 Markdown；URL 会自动决定服务地址 |
| `handoff delete <code> --yes` | high-risk-write | 在 TTL 到期前删除交接卡 |
| `handoff auth login/status/logout` | write/read/write | 管理服务凭据 |
| `handoff config show/set-server` | read/write | 管理 profile |
| `handoff doctor [--offline]` | read | 检查会话发现、凭据和服务连通性 |

所有命令支持根级 `--profile NAME`。`create` / `receive` 支持 `--json`。写文件默认不覆盖，显式加 `--force` 才会覆盖。

## 上下文发现

`--from auto` 按当前工作区和更新时间匹配：

- Codex：`~/.codex/sessions/**/*.jsonl`
- Claude Code：`~/.claude/projects/**/*.jsonl`
- Pi：`~/.pi/agent/sessions/**/*.jsonl` 或 `~/.pi/sessions/**/*.jsonl`
- 永久可用的通用逃生口：stdin 和 `--file`

只提取和传递 user / assistant 文本，不提取 thinking、tool result 或原生凭据。Codex Session 已有 compact summary 时会一起利用。

`--compact current` 是默认值。`--agent auto` 会优先识别正在承载命令的 Agent，其次使用 Session 来源；也可用 `--agent codex|claude|pi` 解决特殊终端环境里的识别问题。这个参数只选择运行时，始终不选择模型。

## 隐私与故障降级

- CLI 在调用当前 Agent 前先脱敏：API key、Bearer token、密码、私钥块会被替换；服务端会再次清理最终 sections。
- 本机绝对路径改写为 `$HOME` / `$WORKSPACE`。
- 默认模式下，handoff 服务只收到压缩后的 sections，完整 Session 不会发给 handoffd；服务端只持久化最终交接卡。
- “本地压缩”指压缩流程由本机当前 Agent CLI 发起。若当前 Agent 背后使用云模型，脱敏后的上下文仍会发送给该 Agent 已配置的模型服务商；不会另发给 handoffd 的模型。
- 分享 ID 是 128-bit 随机 capability，默认 7 天过期，可以提前删除。
- 当前 Agent 不可用时会生成 deterministic handoff 并明确提示，不会静默改用服务端压缩。
- 分享 URL 本身就是读取权限，不应发到公开频道。
- `HANDOFF.md` 是一次不可变快照。发送方和接收方的 Agent 不会共同修改同一份文件；继续工作后再创建下一份 handoff。

## 启动服务

```bash
cp .env.example .env
# 编辑 .env：生产必须设 HANDOFF_API_TOKEN
set -a; . ./.env; set +a
go run ./cmd/handoffd
```

默认模式无需配置 Ark。只有团队明确使用 `--compact server` 时，才需要为 handoffd 配置标准 ModelArk API：

```dotenv
ARK_API_BASE=https://ark.cn-beijing.volces.com/api/v3
ARK_API_KEY=...
ARK_MODEL=<控制台已开通的 model id 或 endpoint id>
```

HTTP API：

```text
GET    /healthz
GET    /v1/schema/create
POST   /v1/handoffs        Authorization: Bearer <token>
POST   /v1/handoffs/compact Authorization: Bearer <token>  显式服务端压缩
GET    /v1/handoffs/:id
DELETE /v1/handoffs/:id    Authorization: Bearer <token>
GET    /h/:id              只读网页
```

## 部署到火山云 ECS

```bash
make test
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o handoffd-linux ./cmd/handoffd

scp handoffd-linux root@your-ecs:/usr/local/bin/handoffd
scp deploy/handoff.service root@your-ecs:/etc/systemd/system/handoff.service
scp .env root@your-ecs:/etc/handoff/handoff.env

ssh root@your-ecs 'chmod 600 /etc/handoff/handoff.env && systemctl daemon-reload && systemctl enable --now handoff'
```

`handoffd` 默认只监听 `127.0.0.1:7391`。用 [`deploy/Caddyfile`](deploy/Caddyfile) 终止 TLS，只在安全组开放 80/443，不要公开 7391。也可以用根目录 `Dockerfile` 构建。

## 开发

```bash
make test
make build
./bin/handoff --help
```

当前是最小闭环。本版不做组织成员、权限系统、群聊、原生 Session 恢复或并发编辑。
