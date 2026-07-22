# handoff

`handoff` 是一个给人和 Agent 用的通用上下文交接 CLI。它不绑定 OpenGrove，也不复制 Codex / Claude 的原生 Session；它把当前上下文变成一份可编辑、可追溯、模型无关的 `HANDOFF.md`。

```text
Codex / Claude / Pi / stdin / files
                 │
                 ▼
        handoff create "next goal"
                 │  redact + compact
                 ▼
              handoffd
                 │
          code / URL / Markdown
                 ▼
        handoff receive <code>
```

## 用起来

```bash
go install ./cmd/handoff

# 首次配置；Token 通过 stdin 读取，避免进入 shell history
printf '%s' "$HANDOFF_TOKEN" | \
  handoff auth login --server https://handoff.example.com --token-stdin

# 自动找当前工作区最近的 Codex / Claude / Pi Session
handoff create "让同事继续完成 CLI 部署"

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
| `handoff create "goal"` | write | 自动读取上下文、脱敏、compact 并上传交接卡 |
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

只提取 user / assistant 文本，不上传 thinking、tool result 或原生凭据。Codex Session 已有 compact summary 时会一起利用。

## 隐私与故障降级

- CLI 和服务端各做一次脱敏：API key、Bearer token、密码、私钥块会被替换。
- 本机绝对路径改写为 `$HOME` / `$WORKSPACE`。
- 服务端只持久化最终交接卡，不保存 create request 或原始 transcript。
- 分享 ID 是 128-bit 随机 capability，默认 7 天过期，可以提前删除。
- Ark 不可用时会生成 deterministic handoff，不让交接链路整体失败；CLI 会明确标记。
- 分享 URL 本身就是读取权限，不应发到公开频道。

## 启动服务

```bash
cp .env.example .env
# 编辑 .env：生产必须设 HANDOFF_API_TOKEN
set -a; . ./.env; set +a
go run ./cmd/handoffd
```

Ark 服务端推荐使用标准 ModelArk API，而不是个人 Coding Plan 网关：

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
