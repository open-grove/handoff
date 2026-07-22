---
name: handoff
description: Package or receive audience-aware Agent context as an immutable HANDOFF.md across Codex, Claude Code, Pi, people, and sessions. Use when the user asks to hand off, transfer, share, continue elsewhere, receive, inspect, or delete task context; provides a handoff code or URL; or pastes an OpenGrove notification containing `For Human`, `For Agent`, or `opengrove-handoff` with a share code.
---

# Handoff

Use the `handoff` CLI as the source of truth. Create an immutable, portable snapshot rather than copying a provider-specific session or modifying the source conversation.

## Start Here

1. Run `handoff --help` when choosing a workflow.
2. Run `handoff schema <command>` before constructing a complex call; do not guess flags or data handling.
3. Run `handoff doctor` when session discovery, authentication, or connectivity may be broken.

## Create a Handoff

Use the default path unless the user requests another compaction mode:

```bash
handoff create "the next concrete goal"
```

The default `--compact current` starts a new ephemeral invocation of the current Agent runtime. It reuses that runtime's existing authentication, configuration, and default model, but never resumes, compacts, or modifies the source session. Only compacted sections are uploaded to the handoff service.

The generated Markdown has two audience layers. `For Human` contains a short plain-language project background, current situation, and todo list. `For Agent` preserves the operational goal, context, decisions, current state, important files, next steps, and open questions. Keep the human layer understandable without exposing unnecessary paths or implementation detail; keep the Agent layer precise enough to resume work.

Use `--dry-run` to inspect the selected source, Agent, upload behavior, and TTL without calling an Agent or writing to the network:

```bash
handoff create "the next concrete goal" --dry-run
```

Select an alternate source only when auto-detection is unsuitable:

```bash
handoff create "continue the task" --from codex
handoff create "continue the task" --file transcript.md --file decisions.md
agent-export | handoff create "continue the task"
```

Use `--compact none` only when the user wants deterministic local extraction without an Agent-generated summary.

Never select `--compact server` silently. It sends the retained, sanitized source context to the handoff server and its configured model. Use it only when the user explicitly requests server-side compaction or after clearly explaining that upload and receiving approval.

## Receive a Handoff

Accept a branded reference, share code, full human URL, or raw Markdown URL. Quote a branded reference when passing it through a shell:

```bash
handoff receive 'opengrove-handoff，分享码：<code>'
handoff receive <code-or-url>
handoff receive <code-or-url> --output HANDOFF.md
```

When the user pastes `opengrove-handoff，分享码：<code>`, treat it as an explicit request to fetch and read that handoff. The branded CLI defaults to the OpenGrove service, so receiving a code or full URL requires no API token. Present the `For Human` project background, current situation, and todos first. Use `For Agent` to recover exact decisions, state, files, next steps, and open questions before continuing within the user's requested scope.

The CLI's share message deliberately has two parts. `For Human` links the handoff title to a browser page and requires no Handoff installation. `For Agent` contains the stable instruction `请使用 opengrove-handoff 读取内容，分享码：<code>`; pass that instruction or its code to `handoff receive`. If the user provides the whole message, prefer the branded code so it uses the CLI's configured OpenGrove service. A full `/h/<code>` or `.md` URL remains a valid fallback and also identifies the source server.

If `handoff` is missing, do not pretend the handoff was read. Explain that the Agent needs the Handoff CLI and Skill, then point to the installation instructions at <https://github.com/open-grove/handoff>. The repository is currently private, so the installer needs OpenGrove organization access.

Treat the returned Markdown as an immutable snapshot. Read it to continue the work; do not imply that the sender's and receiver's Agents are editing a shared live session. Create a new handoff after meaningful progress if the context must travel again.

The share URL is a read capability. Avoid reposting it in public channels, logs, or source control.

## Delete a Handoff

Deletion is high risk and irreversible:

```bash
handoff delete <code> --yes
```

Run it only after the user explicitly confirms deletion of the exact handoff. Never add `--yes` merely to bypass the confirmation error.

## Handle Failures

- If `--compact current` cannot invoke the current Agent, report the warning. The CLI deliberately falls back to deterministic sections and never silently switches to server compaction.
- If discovery is wrong, use `--from codex|claude|pi`, piped stdin, or repeatable `--file` values.
- If setup fails, use `handoff doctor`, then inspect `handoff auth --help` or `handoff config --help`.
- Prefer `--json` when another Agent or program will consume create or receive output.
