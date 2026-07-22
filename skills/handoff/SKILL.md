---
name: handoff
description: Package the current Codex, Claude Code, Pi, stdin, or file context into a compact HANDOFF.md and share it with another person, Agent, or session. Use when the user asks to hand off, transfer, share, continue elsewhere, receive, inspect, or delete an Agent task context or provides a handoff code or URL.
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

Accept either a share code or a full URL:

```bash
handoff receive <code-or-url>
handoff receive <code-or-url> --output HANDOFF.md
```

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
