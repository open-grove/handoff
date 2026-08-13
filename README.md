# handoff

`handoff` 是一个给人和 Agent 用的通用上下文交接 CLI。它不复制 Codex / Claude / Pi / OpenCode 的原生 Session；它把当前上下文变成一份可阅读、可追溯、模型无关的 `HANDOFF.md`。默认生成与发布无需登录，只有可选的云端生成复用本机 OpenGrove 登录。

```text
当前 Session（只读快照，不执行 /compact）
                 │
                 ▼
    提取全部可读消息 + 规范化 + 尽力脱敏
                 │
                 ▼
          Canonical Context
            │           │
            │           └── --attach-context（可选持久化）
            ▼
 agent / preserve / cloud 生成 sections
        （deterministic 仅是本机无旁路 Agent 时的内部备用提取）
            │
            ▼
 handoffd 存储 / 分享 HANDOFF.md（默认只存 sections）
```

四个控制面各司其职：

| 参数 | 只负责什么 | 不负责什么 |
|---|---|---|
| `--source` / `--file` / stdin | 选择输入：Agent Session、文件或管道内容 | 不选生成 Agent、provider 或模型 |
| `--generator` | 选择 sections 如何产生：本机 Agent 旁路、原样保留已整理 Markdown，或云端生成 | 不决定是否持久化完整 Context |
| `--runtime` | 仅在 `--generator agent` 时选择启动哪个本机 Agent CLI 作为旁路 | 不选输入来源、provider 或模型 |
| `--attach-context` | 独立决定是否把完整脱敏 Canonical Context 作为附件持久化 | 不改变 sections 的生成方式 |

普通调用一个都不用设：`source=auto`、`generator=agent`、`runtime=auto`、默认不附完整 Context。CLI 会只读发现来源 Session，再启动一次全新、隔离的本机 Agent 旁路来生成 sections。比如从 Codex 中调用时，它会执行 `codex exec --ephemeral`；这个旁路沿用 Codex 已有认证、配置、provider 和默认模型，却不会 resume、compact 或改写来源 Session。Claude Code 和 Pi 使用同样的无状态旁路思路。OpenCode 会在独立临时目录创建生成 Session，并仅在目录、创建时间和 Session ID 都验证通过后删除这一枚临时 Session。`agent:<runtime>` 只是生成来源标记，不表示共享或继续了原 Agent Session。默认 generator 是 `agent`，不是原生 `/compact`。

这里要分清两个 Agent：当前处理用户请求的是“调用方 Agent”；`generator=agent` 再启动一个全新的“旁路归纳 Agent”。`generator=preserve` 则不启动第二个 Agent：调用方 Agent 先把需要分享的内容整理成 Markdown，CLI 仅做尽力脱敏并按原结构发布。

面向用户的交互不应等于 CLI 参数列表。Agent 优先从请求里推断 `share` 还是 `continue`；只有两者真的无法判断、且会实质改变交接内容时才追问。云端生成默认关闭，完整可读 Context 附件也默认关闭，两者都只在用户明确要求时打开；这个附件是脱敏后的 Canonical Context，不是原始 provider Session。`source` 和 `runtime` 属于 Agent/CLI 的内部自动路由，不应作为常规问题抛给用户；只有 `--dry-run` 确认发现错误时才覆盖。交接范围、需排除的对话、接收者或预期结果不清晰时，Agent 也只在这些歧义会改变成品时追问。`--review`、TTL 和输出路径都是可选操作项，不是创建 Handoff 前的必答题。

## 用起来

```bash
git clone https://github.com/open-grove/handoff.git
cd handoff
./install.sh

# install.sh 会同时安装匹配版本的 Skill；也可以随时手动检查或修复
handoff skills install

# 自动找当前工作区最近的 Codex / Claude / Pi / OpenCode Session
handoff create "让同事继续完成 CLI 部署"

# 分享讨论成果：保留结论、推理、例子和被纠正的误解，不强行生成待办
handoff create "MCP App 架构讨论" --intent share

# 交接未完成工作：强调当前状态和下一步
handoff create "继续完成 MCP App 兼容性修复" --intent continue

# 调用方已经整理好正文：保留 Prompt、URL、校验值和代码块，不再叫另一个 Agent 改写
handoff create "WW/Bedrock 单次长流测试方法" --intent share --generator preserve --file ./prepared-method.md

# 创建成功后输出一份 Markdown 消息，可直接粘贴到飞书、Slack 或聊天窗口：
#
# 🖐️ **For Human**
#
# 你收到一份 Handoff，请打开[让同事继续完成 CLI 部署](https://handoff.openmau.com/h/a-secure-share-code)查看。
#
# 🤖 **For Agent**
#
# 请使用 OpenGrove Handoff 读取：`opengrove-handoff:a-secure-share-code`
#
# 未安装时，请[查看安装方法](https://github.com/open-grove/handoff)。

# 可选：生成后在编辑器里检查 Markdown，保存关闭后才发布
handoff create "continue" --review

# 本机没有 Codex / Claude / Pi / OpenCode CLI 时：
# 对已收窄范围的文件或 stdin，默认命令会明确提示并使用 deterministic 备用提取
handoff create "continue" --file ./handoff-notes.md
some-agent-export | handoff create "continue"

# 可选：交给云端 Kimi K3 生成
# 这会临时发送完整 Canonical Context，仅 cloud generator 要求 OpenGrove 登录
handoff create "continue" --generator cloud

# 可选：把完整的、尽力脱敏后的可读 Context 附在最终 Handoff 后面
# 它和 generator 独立；发布附件本身不要求登录
handoff create "continue" --attach-context
handoff create "continue" --generator cloud --attach-context

# 可选：同一台机器上的 Agent 直接读取原始 Session 文件
# 不压缩、不上传、不生成链接或分享码
handoff session locate --goal "continue"

# 任何 Agent 都能用的通用入口
some-agent-export | handoff create "continue the investigation"
handoff create "continue" --file transcript.md --file decisions.md

# 接收方无需 Token，可以粘贴稳定引用、旧分享码或 URL。
# 人直接打开页面；Agent 使用稳定引用：
handoff receive 'opengrove-handoff:a-secure-share-code'
handoff receive a-secure-share-code
handoff receive https://handoff.openmau.com/h/a-secure-share-code
handoff receive https://handoff.openmau.com/h/a-secure-share-code.md
handoff receive a-secure-share-code --output HANDOFF.md

# 只有创建时显式 --attach-context 才存在；receive 不会自动下载
handoff context 'opengrove-handoff:a-secure-share-code'

# 查看当前登录、服务地址和云端生成权限
handoff whoami

# 检查或自动安装 GitHub Release
handoff update --check
handoff update
```

在 Codex、Claude Code、Pi 或 OpenCode 中执行 `create`、`receive`、`context`、`session` 时，CLI 会在真正交接前做一次有 24 小时缓存的更新预检。发现新版后会在 stderr 显示升级进度，完成校验和原子替换后以原参数重新执行本次命令；stdout 始终只保留 Handoff 的正式结果。网络、权限或 Skill 同步问题不会阻断交接，失败后会继续使用当前进程，且 24 小时内不反复尝试。`--dry-run` 不触发自动更新；设置 `HANDOFF_NO_AUTO_UPDATE=1` 可完全关闭。普通终端如需启用同一行为，可设置 `HANDOFF_AUTO_UPDATE=1`。当前自动替换支持 macOS 和 Linux；Windows 仍保留更新提示和手动下载路径。

云端生成使用端到端 SSE：Worker 会立即建立响应，并在等待模型首 token
期间持续发送心跳，随后逐段转发生成结果。Kimi 的长上下文请求可能需要数分钟，
CLI 会以 15 分钟为整体边界；普通发布、接收等请求仍使用较短超时。

Agent 可先查合同，不用猜参数：

```bash
handoff schema create
handoff schema session.locate
handoff schema receive
handoff schema context
handoff schema delete
handoff schema admin.login
handoff skills list
handoff skills read handoff
handoff skills install
handoff create "next goal" --dry-run
handoff doctor
```

`handoff skills read handoff` 输出与当前 CLI 二进制同版本的 Agent Skill，采用与 lark-cli 相同的内嵌 Skill 思路，避免单独分发的说明与命令行为漂移。`handoff skills install` 默认把 `SKILL.md` 和 `agents/openai.yaml` 安装到 Codex、Claude Code 和通用 Agent 目录；已有不同内容时必须显式使用 `--force`。`handoff update` 会同步未修改的旧 Skill，检测到自定义内容时会保留并提示。

## 两种意图，一个文件

新生成的 `HANDOFF.md` 是一份不可变快照。`--intent auto` 是默认值，Agent 会根据对话判断；也可显式选择：

- `share`：传递“这次讨论最后弄明白了什么”。`For Human` 是主体，Agent 会根据这次讨论自由选择文章结构和章节标题。同一主题的结论、理由、例子、纠错和取舍会放在一起，不再按“关键结论 / 为什么 / 例子”强行分桶，也不会把问题改写成待办。`For Agent` 只作技术附录。
- `continue`：传递“接下来谁要继续做什么”。`For Human` 说明背景、当前情况和待办；`For Agent` 保留 Goal、Context、Decisions、Current State、Important Files、Next Steps 和 Open Questions。

两种意图都保留 `For Human / For Agent` 两层：浏览器分享页会完整展示人类内容，把 Agent 内容默认收起。使用 `--attach-context` 时，完整 Context 仍通过独立接口按需获取。

分享页支持 LaTeX 公式定界符 `$...$`、`$$...$$`、`\(...\)` 和 `\[...\]`，服务端会将它们转成原生 MathML。反引号行内代码和 fenced code block 中的同类文字保持原样；无法解析的公式会安全回退为原始文本。

通过 Agent 调用 Handoff 时，Agent 应先从当前对话判断传递目的、范围和重点。只有缺失信息会实质改变成品时才追问，例如“是让接收方继续工作，还是理解讨论结果”、“哪一段对话应被排除”。这不是固定问卷；已能从对话推断的内容不会再问，每次只问最少、最有用的问题。

## CLI

| 命令 | 风险 | 作用 |
|---|---:|---|
| `handoff create "topic or goal"` | write | 只读上下文，由当前 Agent 生成；`--intent share|continue` 控制是分享结果还是交接任务 |
| `handoff session locate` | read | 返回仅限同机使用的原始 provider Session 路径 |
| `handoff receive <reference>` | read | 接受 `opengrove-handoff:<code>`、旧分享码、人类页面或 `.md` URL |
| `handoff context <reference>` | read | 读取创建时显式附带的完整脱敏可读 Context |
| `handoff delete <code> --yes` | high-risk-write | 在 TTL 到期前删除交接卡 |
| `handoff admin login/status/logout` | write/read/write | 管理可选的服务管理员凭据；不是 OpenGrove 用户登录 |
| `handoff config show/set-server` | read/write | 管理 profile |
| `handoff doctor [--offline]` | read | 检查会话发现、凭据和服务连通性 |
| `handoff whoami` | read | 显示 CLI、OpenGrove 身份和云端生成权限 |
| `handoff update [--check]` | high-risk-write/read | 校验 SHA-256 后更新 CLI，并同步未修改的 Skill |
| `handoff schema [action]` | read | 输出精确动作的 JSON Schema 合同 |
| `handoff skills list/read/install` | read/write | 列出、读取或安装二进制内嵌的 Agent Skill |

所有命令都接受 `--json` 或 `--format text|json`，参数放在命令前后均可。写文件默认不覆盖，显式加 `--force` 才会覆盖。裸分享码默认从 OpenGrove 线上服务读取；profile、`HANDOFF_SERVER` 或完整 URL 可以覆盖服务地址。

`create --json` 额外返回 `agent_reference`、`share_message` 和 `fallback_used`。Agent 发起创建时应始终使用 `--json`，最终只原样返回 `share_message`，不加引导语、分隔线、代码块或本地 `Delete:` 状态。只有本机找不到受支持的 Agent CLI，并且输入已经通过 `--file` 或 stdin 收窄时，CLI 才会使用 deterministic 备用提取；此时 `fallback_used` 为 `true`，同时返回脱敏后的 Agent 发现错误。这些降级信息只属于创建端，不会写进共享页。Agent CLI 已存在但调用、认证或内容生成失败时，命令直接报错，不用低质量结果掩盖真实故障。如果备用来源是完整 Agent Session，CLI 也会拒绝直接发布，要求改用 `--file` / stdin，或加 `--review` 人工检查。稳定引用格式为 `opengrove-handoff:<code>`。

传给 `handoff create` 的位置参数应是短主题（`share`）或短任务目标（`continue`），不要把整段进展说明塞进标题。服务端还会从第一个完整分句生成独立 `title`，并限制为最多 64 个视觉列（约 32 个汉字或 64 个英文字符）；完整主题或目标仍保留在 Agent 层。

每次匿名发布都会生成一枚独立删除凭证。服务端只存储 SHA-256 哈希，CLI 把原始凭证写入本机权限为 `0600` 的 `ownership.json`，不会打印到分享消息或 `--json` 输出。创建者可直接运行 `handoff delete <code> --yes`；旧分享或其他人创建的分享仍需管理员凭据。

`handoff update` 检测当前系统和架构，从 GitHub Release 下载对应二进制与 `SHA256SUMS`，校验后原子替换当前可执行文件，并同步仍与旧内嵌版本一致的 Skill。用户自定义过的 Skill 不会被覆盖。Agent 环境中的实际交接命令默认复用这条校验与替换链路自动升级，然后以原参数重新执行；进度只写 stderr，正式输出不变，失败则继续本次交接。检查与失败重试都以 24 小时为间隔，并用本机锁避免多个 Agent 同时更新。机器可读命令仍可通过 `_notice.update` 返回非阻塞提示；设置 `HANDOFF_NO_AUTO_UPDATE=1` 可关闭自动安装，设置 `HANDOFF_NO_UPDATE_NOTIFIER=1` 可关闭提示。仓库为 private 时会复用 `GH_TOKEN` / `GITHUB_TOKEN` 或本机 `gh auth login`；公开后无需 GitHub 登录。

## 上下文发现

`--source auto` 先使用当前 Agent 暴露的 Session ID 做精确匹配；没有可用 ID 时，再按当前工作区、主线程优先级和更新时间匹配：

- Codex：`~/.codex/sessions/**/*.jsonl`
- Claude Code：`~/.claude/projects/**/*.jsonl`
- Pi：`~/.pi/agent/sessions/**/*.jsonl` 或 `~/.pi/sessions/**/*.jsonl`
- OpenCode：只读调用 `opencode session list` 与 `opencode export`；在 OpenCode 内运行时通过 `OPENCODE=1` 优先选择当前工作区最近的根 Session
- 永久可用的通用逃生口：stdin 和 `--file`

CLI 总是构造同一份 Canonical Context：有效的可读 user / assistant 历史，经过规范化和尽力脱敏，不提取 thinking、原始 tool result 或 Provider 内部记录。Codex fork 只使用第一层 `session_meta` 作为自身身份；已 rollback 的回合和 aborted 回合的 assistant 输出会移除，有正式 final 时同回合 commentary 会移除，没有 final 的 commentary 会明确标为临时信息。直属子 Agent 已完成的 final 会合并回来；Claude sidechain 的可读文本会保留并标为辅助信息。若 Provider 把原生 compact summary 明文写入 Session，它会作为辅助信息保留，但不会替代、删除或截断可读消息：

- Claude Code：识别 `isCompactSummary` 消息，同时保留前后的可读对话。
- Pi：识别 compaction summary，同时保留全部可读对话。
- Codex：识别 `compacted` 边界；能读取 summary 时把它作为辅助信息，不能读取时也不伪装成已复用。
- OpenCode：只提取非 synthetic 的 `text` part；排除 reasoning、tool、patch、snapshot、文件数据和中止输出，并把已完成的 compaction summary 作为辅助信息。

`handoff create ... --dry-run` 会显示消息数、字符数、`native_compact_found`、是否存在辅助 summary，以及生成阶段与发布阶段各自会发送什么。

`preserve` 是面向精确材料的公开模式。调用方 Agent 先从当前讨论中选择并整理好 Markdown，再通过 stdin 或 `--file` 交给 CLI；CLI 不叫旁路 Agent 再压缩，Prompt、URL、文件大小、SHA-256、表格和代码块的结构在尽力脱敏后保留。单个 stdin/file 会显示为一篇连续正文，不暴露临时文件名；如果开头 H1 与 Handoff 主题一致，它只作为页面标题显示一次。只有多文件时才用文件名区分章节。`preserve` 只用于 `share`，不会选择 Session 里哪些回合相关，也不会把整段 Session 自动变成任务。

`deterministic` 不再是公开可选 generator，也不应显式传 `--generator deterministic`。它仅在 CLI 根本找不到受支持的旁路 Agent CLI 时内部启用。降级警告只出现在创建端的 stdout/JSON 中；共享页只描述内容的真实来源和处理方式，不再显示“Agent 归纳不可用”这类误导文案，也不在 Human/Agent 区重复整篇正文。

stdin 超过 4 MiB、Session 中出现损坏的 JSONL 记录、OpenCode 导出不是合法 JSON 或原始导出超过 64 MiB 本地解析上限时，CLI 会明确报错，不会把前半段当成完整上下文继续发布。确定性提取会同时保留辅助 summary 和全部已筛选消息；若最终发布体超过服务端上限，则发布会明确失败。

`--attach-context` 与 generator 完全独立。它把“本次选中输入的 Canonical Context”作为单独附件持久化到 Handoff 生命周期结束；默认不开启。普通 Session 流程附带的是尽力脱敏后的可读 Session；`preserve` 的输入已被 stdin/file 取代，所以它附带的是这份已整理材料，不是原 Agent Session。附件不包含 thinking、tool result、Provider 内部记录、本机 Session 路径、Session ID 或 cursor。上传前会清除已知密钥、私钥、本机用户名路径、邮箱和 IP，但这是 best-effort 规则，无法保证识别自然语言里的全部个人信息。超过 4 MiB 的发布请求会明确失败，不会静默截断。

`handoff session locate` 是另一条完全本地的路径：CLI 只输出匹配到的 Codex / Claude / Pi 原始 Session 文件绝对路径，不调用模型、不访问 Handoff 服务，也不产生分享码；接收 Agent 必须运行在同一台机器上。OpenCode 使用数据库存储，不存在可安全交付的单 Session 文件，因此不支持这条路径；需要便携的完整可读对话时，使用 `handoff create ... --source opencode --attach-context`。原始 Session 可能含工具数据和 provider 元数据，不应发送到公开渠道。

`--generator agent` 是默认值。`--runtime auto` 先看调用方 Agent 设置的 `HANDOFF_CALLER_RUNTIME`，再识别当前 host，其次使用 Session 来源，最后才在已安装 CLI 中选择；它选中的只是“新启动哪个旁路 CLI”。Skill 在知道自己是 Claude Code 时会设 `HANDOFF_CALLER_RUNTIME=claude`，因此即使环境残留 `CODEX_THREAD_ID`，也不会误启 Codex。如果多个 host 标记冲突又无法用来源消歧，CLI 会明确报错，不再按固定顺序猜。仅当 host 识别错误或用户明确指定不同旁路时，才使用 `--runtime codex|claude|pi|opencode`。

## 隐私与故障降级

- CLI 在调用当前 Agent 前先脱敏：API key、Bearer token、密码、私钥块会被替换；服务端会再次清理最终 sections。
- `Important Files` 只保留仓库相对路径；本机绝对路径和仓库外文件会被移除。
- 默认 `agent`、`preserve` 和内部 deterministic 备用路径下，handoffd 只收到最终 sections；只有显式 `--attach-context` 才会额外收到并持久化 Canonical Context。`preserve` 的 Context 是准备好的 stdin/file，不能误称为原 Agent Session。
- `agent` generator 由选中的本机 Agent 旁路 CLI 发起。如果该 CLI 使用云模型，脱敏后的上下文仍会发送给它已配置的模型服务商，但不会另发给 handoffd 的模型。
- OpenCode agent generator 会强制关闭 OpenCode 自动分享并拒绝工具权限；生成后仅删除经过目录、创建时间与 ID 三重校验的临时 Session，来源 Session 始终只读。
- `cloud` generator 会把 Canonical Context 临时交给 handoffd 的模型生成 preview；该生成接口不存储原文。是否持久化仍只由独立的 `--attach-context` 决定。
- `cloud` generator 要求本机 OpenGrove 已登录；CLI 只读取短期 access token，服务端会向 OpenGrove 账户服务校验。普通发布和接收无需登录。
- `--review` 会在发布前打开 `$VISUAL` / `$EDITOR` 中的 Markdown；保存关闭后才上传最终 sections。服务端模式为了生成 preview，原文上传发生在 review 之前。
- 分享 ID 是 128-bit 随机 capability，默认 7 天过期，可以提前删除。
- 匿名发布返回的删除凭证是另一枚 256-bit capability，只保存在创建者本机；服务端不保存明文。
- Cloudflare Worker 对匿名创建按请求来源做每分钟 30 次的宽松限流，降低公开 CLI 后的批量滥用风险；它不是计费额度系统。
- 只有本机找不到受支持的 Agent CLI，且来源已通过 `--file` 或 stdin 收窄时，才会使用 deterministic 备用提取。降级原因只在创建端输出，不写入共享页。Agent 已找到但生成失败时直接报错；任何情况都不会静默改用服务端生成。
- Agent 发起实际交接前可能自动安装经过 SHA-256 校验的新版本；状态只写 stderr，失败不阻断原命令，`HANDOFF_NO_AUTO_UPDATE=1` 可关闭。
- 分享 URL 本身就是读取权限，不应发到公开频道。
- `HANDOFF.md` 是一次不可变快照。发送方和接收方的 Agent 不会共同修改同一份文件；继续工作后再创建下一份 handoff。

## 启动服务

```bash
cp .env.example .env
# HANDOFF_API_TOKEN 仅用于管理员提前删除，不影响匿名发布
set -a; . ./.env; set +a
go run ./cmd/handoffd
```

默认 generator 无需配置火山方舟。只有团队明确使用 `--generator cloud` 时，才需要为 handoffd 配置 [方舟 Agent Plan](https://www.volcengine.com/docs/82379/2373738?lang=zh)：

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
GET    /v1/handoffs/:id/context  仅在创建时显式附带 Context 才存在
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
make test-worker
make build
./bin/handoff --help
```

Cloudflare Worker 首次开发或依赖变更后先运行 `npm ci --prefix cloudflare`。发布前可用
`(cd cloudflare && npx wrangler deploy --dry-run)` 验证打包；确认 Cloudflare 凭据和 D1 绑定后，运行
`npm run deploy --prefix cloudflare` 部署线上分享页。

当前是最小闭环。本版不做组织成员、权限系统、群聊、原生 Session 恢复或并发编辑。
