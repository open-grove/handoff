# Handoff

English | [简体中文](README.zh-CN.md)

[![Release](https://img.shields.io/github/v/release/open-grove/handoff)](https://github.com/open-grove/handoff/releases/latest)
[![Release workflow](https://github.com/open-grove/handoff/actions/workflows/release.yml/badge.svg)](https://github.com/open-grove/handoff/actions/workflows/release.yml)
[![License](https://img.shields.io/github/license/open-grove/handoff)](LICENSE)

Turn an Agent conversation into a readable, shareable, and resumable `HANDOFF.md`.

Handoff supports Codex, Claude Code, Pi, and OpenCode. It reads the source Session without modifying it, produces a model-independent immutable snapshot, and provides both a human-friendly share page and structured context for the receiving Agent.

## Features

- **One-command handoff** — automatically discovers the Agent Session for the current workspace.
- **Built for people and Agents** — one Handoff contains both a human summary and technical Agent context.
- **Model-independent** — the receiver does not need the same Agent, model, or Session system.
- **Read-only source access** — never resumes, compacts, or changes the source Session.
- **Minimal uploads by default** — normally sends only the generated sections to the Handoff service.
- **No account required** — creation and retrieval work anonymously.
- **No automatic expiration** — a Handoff remains available until its creator or an administrator deletes it.
- **Readable on the web** — share pages render Markdown and LaTeX formulas.

## Installation

macOS and Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/open-grove/handoff/main/install.sh | sh
```

The installer downloads the latest release, verifies its SHA-256 checksum, and installs the CLI at `~/.local/bin/handoff`. It also installs the matching Handoff Skill for Codex, Claude Code, and compatible Agents.

If `~/.local/bin` is not in your `PATH`, add it to your shell configuration. To use another install directory, set `HANDOFF_INSTALL_DIR`:

```bash
curl -fsSL https://raw.githubusercontent.com/open-grove/handoff/main/install.sh \
  | HANDOFF_INSTALL_DIR=/usr/local/bin sh
```

Windows users can download a prebuilt archive from [GitHub Releases](https://github.com/open-grove/handoff/releases/latest). Every release includes `SHA256SUMS`.

Update an existing installation:

```bash
handoff update
```

## Quick start

Prepare the handoff in the current conversation, save it as `prepared.md`, then run:

```bash
handoff create "Continue the CLI deployment" --intent continue --file prepared.md
```

The default `current-session` mode publishes Markdown prepared by the Agent in this conversation. The CLI reads stdin or files, applies best-effort redaction, and starts no second AI. It needs no additional Agent CLI. Missing prepared input returns an error instead of silently starting a new session. The command returns a message ready to send:

```markdown
🖐️ **For Human**

You received a Handoff. Open [Continue the CLI deployment](https://handoff.openmau.com/h/<code>).

🤖 **For Agent**

Use OpenGrove Handoff to read: `handoff:<code>`
```

The receiver can open the share page or let an Agent read it directly:

```bash
handoff receive 'handoff:<code>'
```

Neither creation nor retrieval requires an account. Legacy `opengrove-handoff:<code>` references remain readable.

## Common workflows

### Share the result of a discussion

Use `share` to preserve conclusions, reasoning, examples, and rejected alternatives:

```bash
handoff create "MCP App architecture discussion" --intent share --file prepared.md
```

### Continue unfinished work

Use `continue` when another person or Agent should pick up the remaining work:

```bash
handoff create "Continue fixing MCP App compatibility" --intent continue --file prepared.md
```

### Publish prepared Markdown unchanged

The default `current-session` mode preserves prepared Markdown for both `share` and `continue`. Prompts, URLs, checksums, tables, and code blocks retain their structure, subject to best-effort redaction:

```bash
handoff create "Long-stream test method" \
  --intent share \
  --file ./prepared-method.md
```

You can also pipe content through stdin:

```bash
cat prepared.md | handoff create "Investigation result" --intent share
```

### Attach the complete readable context

By default, only prepared sections are published. To explicitly ask a new session to prepare from a source Session and also attach its full readable conversation:

```bash
handoff create "Continue investigating the production issue" --generator new-session --intent continue --attach-context
```

Both modes support attachments. With current-session, the attachment contains the supplied stdin/file material, not the original conversation. The Session-source example above attaches a best-effort redacted, readable Canonical Context—not the raw Session—and excludes thinking and raw tool results. It remains stored with the Handoff until explicitly deleted.

The receiver can fetch it on demand:

```bash
handoff context 'handoff:<code>'
```

### Pass a same-machine Session path only

When the receiving Agent runs on the same machine, you can avoid uploading anything:

```bash
handoff session locate --goal "Continue the remaining implementation"
```

This command supports Codex, Claude Code, and Pi, which keep standalone Session files. The returned file is raw and unredacted; do not send it through public or cross-device channels.

### Review before publishing

```bash
# Inspect the source, sidecar Agent, and upload scope without generating or publishing
handoff create "Next step" --file prepared.md --dry-run

# Review the generated result in your editor, then publish after saving
handoff create "Next step" --intent continue --file prepared.md --review
```

## How it works

```text
current-session (default)          new-session (explicit)
prepared stdin / files             Session / stdin / files
          │                                 │
          ▼                                 ▼
best-effort redaction              best-effort redaction
          │                                 │
          │                          fresh Agent session
          └────────────────┬────────────────┘
                           ▼
                   immutable Handoff
                   share / continue
                     │           │
                     ▼           ▼
                 share page   HANDOFF.md
```

`--generator` selects where preparation happens:

- `current-session` (default): the calling conversation prepares the Markdown; the CLI publishes the supplied stdin/files.
- `new-session` (explicit): a fresh isolated local Agent session prepares it, using that CLI's authentication, provider, and default model.

Both support sharing discussion results and continuing work. A new session never continues or modifies the source Session. The Handoff service itself does not call a model. A conversational Agent should choose new-session only when the user explicitly requests it.

For example: `handoff create "Continue investigating the release failure" --generator new-session --intent continue`.

In explicitly selected new-session mode, if no supported Agent CLI is available, the CLI uses a limited deterministic fallback only when explicitly scoped content was provided through `--file` or stdin. If an Agent CLI is found but its invocation or output fails, the command returns the underlying error.

## Core concepts

### Intent

| Intent | Use it to | Emphasizes |
|---|---|---|
| `share` | Share what a discussion ultimately established | Conclusions, reasoning, evidence, examples, and trade-offs |
| `continue` | Let the receiver continue unfinished work | Background, current state, files, next steps, and open questions |
| `auto` | current-session uses share; new-session asks its new Agent to infer intent | The calling conversation should choose share or continue explicitly |

### Input, generation, and persistence

| Control | Purpose |
|---|---|
| `--source` / `--file` / stdin | Select the input content |
| `--generator current-session|new-session` | Prepare here or start a fresh AI conversation; defaults to current-session |
| `--runtime` | Select the local sidecar CLI for new-session only |
| `--attach-context` | Independently choose whether to store the full redacted readable context |

Normal use supplies prepared stdin/`--file` content and an explicit intent, with `generator=current-session`, `source=auto`, `runtime=auto`, and no full Context attachment. Session discovery via `--source` applies only to new-session.

Legacy `preserve` / `agent` names alias current-session / new-session. Artifact provenance keeps `preserve` / `agent:<runtime>` for existing hosted renderers. For continue, Context retains the complete prepared document; state and next steps are copied from recognizable headings, with a source excerpt and the supplied goal as defaults when those headings are absent.

## Privacy and security

- Canonical Context contains only normalized, readable user and assistant content. Thinking, raw tool output, and provider-internal records are excluded.
- The CLI attempts to redact API keys, bearer tokens, passwords, private keys, local user paths, email addresses, and IP addresses. It cannot guarantee detection of every identity detail written in natural language.
- If the local sidecar Agent uses a cloud model, the redacted context is still sent to the model provider configured for that CLI.
- By default, the Handoff service receives only the final sections. `--attach-context` is the sole explicit switch that stores the complete readable context.
- The share URL and `handoff:<code>` are bearer-style read credentials. Anyone who has either can read the Handoff.
- Every creation returns a separate delete credential. Its plaintext is stored only on the creator's machine; the service stores only its SHA-256 hash.
- Handoffs do not expire automatically. Delete one with `handoff delete <reference> --yes`.

Report security issues privately through the repository's Security page. See [SECURITY.md](SECURITY.md).

## Command reference

| Command | Description |
|---|---|
| `handoff create "topic or goal"` | Generate and publish a Handoff |
| `handoff receive <reference>` | Retrieve a Handoff |
| `handoff context <reference>` | Retrieve an explicitly attached complete readable Context |
| `handoff session locate` | Return a same-machine raw Session path |
| `handoff delete <reference> --yes` | Permanently delete a Handoff |
| `handoff doctor [--offline]` | Check Session discovery, Agent CLIs, and service connectivity |
| `handoff update [--check]` | Check for or install a verified release |
| `handoff schema [action]` | Print the machine-readable command contract |
| `handoff skills list/read/install` | Inspect or install the embedded Agent Skill |
| `handoff config show/set-server` | Inspect configuration or select a service endpoint |
| `handoff admin login/status/logout` | Manage administrator credentials for a self-hosted service |

All commands support `--json` or `--format text|json`. Use the CLI for the complete option reference:

```bash
handoff --help
handoff create --help
handoff schema create
```

## Agent integration

Install or repair the embedded Skill:

```bash
handoff skills install
```

The Skill teaches an Agent how to choose between `share` and `continue`, limit context scope, and invoke the CLI in machine-readable mode. Existing customized Skills are not overwritten automatically; replace one explicitly with:

```bash
handoff skills install --force
```

When an Agent invokes `create`, it should add `--json` and relay the returned `share_message` verbatim. Creator-private state, including delete credentials, must not appear in the shared message.

In Agent environments on macOS and Linux, `create`, `receive`, `context`, and `session` perform an update preflight at most once every 24 hours. Updates are SHA-256 verified, and a failed update never blocks the current task. Set `HANDOFF_NO_AUTO_UPDATE=1` to disable automatic installation.

## Self-hosting

The CLI uses `https://handoff.openmau.com` by default. For creation, select another service with a profile or `HANDOFF_SERVER`; `receive` and `context` also accept a complete share URL directly.

### Go server

```bash
cp .env.example .env
set -a; . ./.env; set +a
go run ./cmd/handoffd
```

`handoffd` listens on `127.0.0.1:7391` by default and stores data under `HANDOFF_DATA_DIR`. For production, the repository includes a [Dockerfile](Dockerfile), [systemd unit](deploy/handoff.service), and [Caddyfile](deploy/Caddyfile). Do not expose port 7391 directly to the public internet.

### Cloudflare Worker

The hosted share page can also run on Cloudflare Workers + D1. For a fresh deployment, configure your D1 database and domain in `wrangler.jsonc`, then run:

```bash
npm ci --prefix cloudflare
npm test --prefix cloudflare

cd cloudflare
npx wrangler d1 migrations apply HANDOFF_DB --remote
npx wrangler deploy
```

When upgrading protocol v6 to v7, deploy the new Worker before applying `0004_permanent_handoffs.sql`. This prevents an old Worker from treating permanent records as expired during the migration window.

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

Publishing and retrieval require no login. Deletion requires either the creator's `X-Handoff-Delete-Token` or an administrator bearer token configured by the self-hosted service.

## Development

Handoff requires Go 1.24+ and Node.js 20+:

```bash
make test
make test-worker
make build
./bin/handoff --help
```

Verify the Worker bundle with:

```bash
cd cloudflare
npx wrangler deploy --dry-run
```

## Project scope

Handoff is an immutable point-in-time transfer, not a real-time collaboration system. It does not currently provide organization membership, fine-grained permissions, group chat, native Session restoration, or concurrent editing.

## License

[Apache License 2.0](LICENSE)
