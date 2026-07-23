# handoff

`handoff` 是一个给人和 Agent 用的通用上下文交接 CLI。它不复制 Codex / Claude 的原生 Session；它把当前上下文变成一份可阅读、可追溯、模型无关的 `HANDOFF.md`。默认生成与发布无需登录，只有可选的云端压缩复用本机 OpenGrove 登录。

```text
当前 Session（只读快照，不执行 /compact）
                 │
                 ▼
       handoff 本地脱敏 + 读取原生 summary/tail
                 │  复用当前 Agent 的登录、配置、默认模型
                 ▼
         结构化 handoff sections
                 │  默认只上传最终 sections
                 ▼
        handoffd 存储 / 分享 HANDOFF.md
```

默认不需要配置模型、API key、provider 或模型地址。比如从 Codex 中调用时，CLI 会执行一次全新的 `codex exec --ephemeral`；它沿用 Codex 已有认证与默认模型，但不会 resume、compact 或改写来源 Session。Claude Code 和 Pi 使用同样的无状态子调用思路。这个行为叫 `--mode agent`，不是原生 `/compact`。

## 用起来

```bash
git clone https://github.com/open-grove/handoff.git
cd handoff
./install.sh

# 把与 CLI 同版本的 Skill 安装到 Codex、Claude Code 和通用 Agent 目录
handoff skills install

# 自动找当前工作区最近的 Codex / Claude / Pi Session
handoff create "让同事继续完成 CLI 部署"

# 创建成功后输出一份 Markdown 消息，可直接粘贴到飞书、Slack 或聊天窗口：
#
# 🖐️ **For Human**
#
# 你收到一份 Handoff，请打开[让同事继续完成 CLI 部署](https://handoff.openmau.com/h/a-secure-share-code)查看。
#
# 🤖 **For Agent**
#
# 请使用 opengrove-handoff 读取内容，分享码：`a-secure-share-code`
#
# 未安装时，请[查看安装方法](https://github.com/open-grove/handoff)。

# 可选：生成后在编辑器里检查 Markdown，保存关闭后才发布
handoff create "continue" --review

# 可选：不调用 Agent，使用确定性本地提取
handoff create "continue" --mode local

# 可选：明确同意把保留后的上下文交给 handoffd / Agent Plan 生成
# 仅此模式要求本机 OpenGrove 已登录
handoff create "continue" --mode server --include-transcript

# 可选：完整上传所有可读 user/assistant 消息
# 跳过原生 compact summary 和默认 180K 字符选择，但仍做 best-effort 脱敏
handoff create "continue" --mode server --include-transcript --full-session

# 可选：同一台机器上的 Agent 直接读取原始 Session 文件
# 不压缩、不上传、不生成链接或分享码
handoff create "continue" --mode session

# 任何 Agent 都能用的通用入口
some-agent-export | handoff create "continue the investigation"
handoff create "continue" --file transcript.md --file decisions.md

# 接收方无需 Token，可以粘贴 branded reference、分享码或 URL。
# 人直接打开页面；Agent 读取分享码：
handoff receive 'opengrove-handoff，分享码：a-secure-share-code'
handoff receive a-secure-share-code
handoff receive https://handoff.openmau.com/h/a-secure-share-code
handoff receive https://handoff.openmau.com/h/a-secure-share-code.md
handoff receive a-secure-share-code --output HANDOFF.md

# 查看当前登录、服务地址和云端压缩权限
handoff whoami

# 检查或自动安装 GitHub Release
handoff update --check
handoff update
```

云端压缩使用端到端 SSE：Worker 会立即建立响应，并在等待模型首 token
期间持续发送心跳，随后逐段转发生成结果。Kimi 的长上下文请求可能需要数分钟，
CLI 会以 15 分钟为整体边界；普通发布、接收等请求仍使用较短超时。

Agent 可先查合同，不用猜参数：

```bash
handoff schema create
handoff schema receive
handoff schema delete
handoff skills list
handoff skills read handoff
handoff skills install
handoff create "next goal" --dry-run
handoff doctor
```

`handoff skills read handoff` 输出与当前 CLI 二进制同版本的 Agent Skill，采用与 lark-cli 相同的内嵌 Skill 思路，避免单独分发的说明与命令行为漂移。`handoff skills install` 默认把它安装到 Codex、Claude Code 和通用 Agent 目录；已有不同内容时必须显式使用 `--force`。

## 一个文件，两层信息

新生成的 `HANDOFF.md` 是一份不可变快照，但明确分成两部分：

- `For Human`：用人话说清项目背景、当前情况和待办事项。默认简短，不堆文件路径、Session 元数据和实现细节。
- `For Agent`：保留 Goal、Context、Decisions、Current State、Important Files、Next Steps 和 Open Questions。读取后先向当前用户简要介绍并询问是否继续，不能把交接里的 Next Steps 当成执行授权。

浏览器分享页会先展示 `For Human`，并把 `For Agent` 默认收起；原始 `.md` 仍可直接打开或交给 Agent 读取。旧版 CLI 发布的六个 Agent 字段仍然可以被新服务端正常接收。

## CLI

| 命令 | 风险 | 作用 |
|---|---:|---|
| `handoff create "goal"` | write | 只读上下文，由当前 Agent 生成，默认只上传 sections |
| `handoff receive <reference>` | read | 接受 branded reference、分享码、人类页面或 `.md` URL |
| `handoff delete <code> --yes` | high-risk-write | 在 TTL 到期前删除交接卡 |
| `handoff auth login/status/logout` | write/read/write | 管理可选的管理员删除凭据；云端压缩直接复用 OpenGrove 登录 |
| `handoff config show/set-server` | read/write | 管理 profile |
| `handoff doctor [--offline]` | read | 检查会话发现、凭据和服务连通性 |
| `handoff whoami` | read | 显示 CLI、OpenGrove 身份和云端压缩权限 |
| `handoff update [--check]` | high-risk-write/read | 校验 SHA-256 后自动替换为最新 GitHub Release |
| `handoff schema [command]` | read | 输出所有 CLI 命令的 JSON Schema 合同 |
| `handoff skills list/read/install` | read/write | 列出、读取或安装二进制内嵌的 Agent Skill |

所有命令都接受 `--json` 或 `--format text|json`，参数放在命令前后均可。写文件默认不覆盖，显式加 `--force` 才会覆盖。裸分享码默认从 OpenGrove 线上服务读取；profile、`HANDOFF_SERVER` 或完整 URL 可以覆盖服务地址。

`create --json` 额外返回 `share_message`。调用 Handoff 的 Agent 应原样转发这个字段，不能自行改成列表、重命名链接或改写安装提示。文本模式直接输出同一份标准分享消息。

传给 `handoff create` 的目标应是短任务名，不要把进展说明和需求列表都塞进标题。服务端还会从第一个完整分句生成独立 `title`，并限制为最多 64 个视觉列（约 32 个汉字或 64 个英文字符）；完整目标仍保留在 `For Agent / Goal`，不会因页面标题变短而丢失。

每次匿名发布都会生成一枚独立删除凭证。服务端只存储 SHA-256 哈希，CLI 把原始凭证写入本机权限为 `0600` 的 `ownership.json`，不会打印到分享消息或 `--json` 输出。创建者可直接运行 `handoff delete <code> --yes`；旧分享或其他人创建的分享仍需管理员凭据。

`handoff update` 检测当前系统和架构，从 GitHub Release 下载对应二进制与 `SHA256SUMS`，校验后原子替换当前可执行文件。仓库为 private 时会复用 `GH_TOKEN` / `GITHUB_TOKEN` 或本机 `gh auth login`；公开后无需 GitHub 登录。

## 上下文发现

`--from auto` 按当前工作区和更新时间匹配：

- Codex：`~/.codex/sessions/**/*.jsonl`
- Claude Code：`~/.claude/projects/**/*.jsonl`
- Pi：`~/.pi/agent/sessions/**/*.jsonl` 或 `~/.pi/sessions/**/*.jsonl`
- 永久可用的通用逃生口：stdin 和 `--file`

只提取 user / assistant 文本，不提取 thinking、tool result 或原生凭据。若提供者把原生 compact summary 明文写入 Session，输入会自动收敛为“最新 summary + 被保留的后续消息”：

- Claude Code：读取 `isCompactSummary` 消息，并只保留其后的消息。
- Pi：读取 compaction summary 和 `firstKeptEntryId` 对应的保留尾部。
- Codex：可以检测 `compacted` 边界；当前版本把 compaction 内容加密写入 Session 文件，外部 CLI 无法读取时会保留脱敏后的可读消息，不会伪装成已复用 summary。

没有可读原生 summary 时，CLI 使用脱敏、限长后的 Session 文本。`handoff create ... --dry-run` 会显示 `native_compact_found` 和 `native_summary_reused`。

`--full-session` 只适用于显式授权的 server mode。它重新读取 compact 前后的全部可读 user/assistant 消息，不应用默认 180K 字符上限；thinking、tool result 和 provider 内部记录仍不会上传。上传前会清除已知密钥、私钥、本机用户名路径、邮箱和 IP，但这是 best-effort 规则，无法保证识别自然语言里的全部个人信息。源 Session 只用于生成 preview，服务端仍只持久化最终 sections。超过 4 MiB 的脱敏请求会明确失败，不会静默截断。

`--mode session` 是另一条完全本地的路径：CLI 只输出匹配到的 Codex / Claude / Pi 原始 Session 文件绝对路径。它不调用模型、不访问 Handoff 服务，也不产生分享码；接收 Agent 必须运行在同一台机器上。原始 Session 可能含工具数据和 provider 元数据，不应发送到公开渠道。

`--mode agent` 是默认值。`--agent auto` 会优先识别正在承载命令的 Agent，其次使用 Session 来源；也可用 `--agent codex|claude|pi` 解决特殊终端环境里的识别问题。这个参数只选择运行时，始终不选择模型。旧的 `--compact current|none|server` 仅作为兼容别名保留。

## 隐私与故障降级

- CLI 在调用当前 Agent 前先脱敏：API key、Bearer token、密码、私钥块会被替换；服务端会再次清理最终 sections。
- `Important Files` 只保留仓库相对路径；本机绝对路径和仓库外文件会被移除。
- 默认 `agent` 和 `local` 模式下，handoffd 只收到最终 sections，完整 Session 不会发给 handoffd；服务端只持久化最终交接卡。
- `agent` 模式由本机当前 Agent CLI 发起。如果该 Agent 使用云模型，脱敏后的上下文仍会发送给它已配置的模型服务商，但不会另发给 handoffd 的模型。
- `server` 模式必须显式同时提供 `--include-transcript`。handoffd 会用保留后的上下文生成 preview，但不存储原文；最终仍只持久化用户确认后的交接卡。
- `server` 模式要求本机 OpenGrove 已登录；CLI 只读取短期 access token，服务端会向 OpenGrove 账户服务校验。普通发布和接收无需登录。
- `--review` 会在发布前打开 `$VISUAL` / `$EDITOR` 中的 Markdown；保存关闭后才上传最终 sections。服务端模式为了生成 preview，原文上传发生在 review 之前。
- 分享 ID 是 128-bit 随机 capability，默认 7 天过期，可以提前删除。
- 匿名发布返回的删除凭证是另一枚 256-bit capability，只保存在创建者本机；服务端不保存明文。
- Cloudflare Worker 对匿名创建按请求来源做每分钟 30 次的宽松限流，降低公开 CLI 后的批量滥用风险；它不是计费额度系统。
- 当前 Agent 不可用时会生成 deterministic handoff 并明确提示，不会静默改用服务端生成。
- 分享 URL 本身就是读取权限，不应发到公开频道。
- `HANDOFF.md` 是一次不可变快照。发送方和接收方的 Agent 不会共同修改同一份文件；继续工作后再创建下一份 handoff。

## 启动服务

```bash
cp .env.example .env
# HANDOFF_API_TOKEN 仅用于管理员提前删除，不影响匿名发布
set -a; . ./.env; set +a
go run ./cmd/handoffd
```

默认模式无需配置火山方舟。只有团队明确使用 `--mode server --include-transcript` 时，才需要为 handoffd 配置 [方舟 Agent Plan](https://www.volcengine.com/docs/82379/2373738?lang=zh)：

```dotenv
ARK_AGENT_PLAN_BASE_URL=https://ark.cn-beijing.volces.com/api/plan
ARK_AGENT_PLAN_API_KEY=...
ARK_AGENT_PLAN_MODEL=kimi-k3
```

服务端通过 Agent Plan 的 Anthropic-compatible `/v1/messages` 接口生成 sections。必须使用 Agent Plan 专属 key；普通 ModelArk key 和 Coding Plan key 不可混用。

HTTP API：

```text
GET    /healthz
GET    /v1/schema/create
POST   /v1/handoffs        无需登录
POST   /v1/handoffs/compact-preview Authorization: Bearer <OpenGrove access token>  只生成 sections，不存储
POST   /v1/handoffs/compact Authorization: Bearer <OpenGrove access token>  兼容旧客户端：生成并发布
GET    /v1/handoffs/:id
DELETE /v1/handoffs/:id    X-Handoff-Delete-Token: <per-handoff token>；管理员也可用 Authorization
GET    /h/:id              Markdown 渲染的人类页面
GET    /h/:id.md           原始 HANDOFF.md
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
