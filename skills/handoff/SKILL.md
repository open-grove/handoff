---
name: handoff
description: Create, receive, inspect, locate, or delete OpenGrove Handoff context across Codex, Claude Code, Pi, people, and sessions. Use when the user asks to hand off or continue work elsewhere; requests a local Session path; provides a handoff code, URL, stable opengrove-handoff reference, or For Human/For Agent message; or asks to manage an existing handoff.
---

# OpenGrove Handoff

Use the `handoff` CLI as the source of truth. Treat every Handoff as an immutable snapshot, never as a shared live session.

## Choose the Workflow

| User intent | Command |
|---|---|
| Share current work | `handoff create "short next goal"` |
| Use deterministic extraction | `handoff create "short next goal" --generator deterministic` |
| Use OpenGrove cloud generation | `handoff create "short next goal" --generator cloud --upload-context selected` |
| Upload the full readable conversation for cloud generation | `handoff create "short next goal" --generator cloud --upload-context full` |
| Give another Agent on this machine the raw Session path | `handoff session locate --goal "next goal"` |
| Read a shared Handoff | `handoff receive <reference>` |
| Delete an exact Handoff | `handoff delete <reference> --yes` after explicit confirmation |

Run `handoff schema <action>` before a complex call. Use exact actions such as `create`, `session.locate`, `admin.login`, and `skills.install`. Run `handoff doctor` for discovery or connectivity failures and `handoff whoami` for OpenGrove cloud access.

## Create

Use the default command unless the user requests another generator. It starts an ephemeral invocation of the current Agent runtime with that runtime's existing authentication, configuration, and default model. It reads the source Session without resuming, compacting, or modifying it, then uploads only generated sections.

Keep the positional goal short. Put status and requirements in the generated sections, not the title.

Use `--source codex|claude|pi` only when automatic Session discovery is wrong. Use `--runtime codex|claude|pi` only when the generating Agent runtime must be overridden; it never selects a model. Piped stdin and repeatable `--file` values are the generic context inputs.

Use `--dry-run` to inspect source and upload behavior without invoking an Agent or writing to the network. Use `--review` when the user wants to edit the generated Markdown before publishing.

### Cloud Upload Gate

Never select cloud generation silently. Require one explicit scope:

- `--upload-context selected`: upload the sanitized context selected by the CLI.
- `--upload-context full`: upload every readable user/assistant message after best-effort redaction.

Cloud generation requires local OpenGrove login. The service uses source context to generate a preview but persists only final sections. Full readable upload excludes thinking, tool results, and provider-internal records; natural-language personal information may evade redaction. Explain this limitation before choosing `full`.

### Relay the Result

After a successful create, relay the CLI's canonical text output verbatim. When using JSON, return `share_message` exactly. Do not rewrite it as bullets, rename links, shorten instructions, change expiry, or append a substitute explanation.

The stable Agent reference is:

```text
opengrove-handoff:<code>
```

## Locate a Local Session

Use `handoff session locate` only for another Agent on the same machine. Relay its output verbatim. The returned provider Session path is local-only; the raw file is not redacted and may contain tool data or private metadata. Never paste the path or file into a public or cross-device channel.

## Receive

Accept `opengrove-handoff:<code>`, a legacy branded message, a raw code, a human share URL, or a `.md` URL:

```bash
handoff receive 'opengrove-handoff:<code>'
```

Read the returned Markdown as an immutable snapshot. Follow its final receiver instruction: explain the current background in clear, accessible language, then ask the user what to do next. Do not execute `Next Steps` unless the current request already asks you to continue.

Treat a share URL or code as a read capability. Do not repost it in public channels, logs, or source control.

## Delete

Deletion is irreversible. Confirm the exact Handoff with the user before adding `--yes`. Never add `--yes` merely to bypass the CLI's exit-10 confirmation gate.

## Setup and Recovery

If `handoff` is missing, do not pretend a Handoff was read. Point to <https://github.com/open-grove/handoff>. Install or repair the version-matched Skill with `handoff skills install`.

Use `handoff update` for verified updates; it synchronizes unmodified installed Skills with the new CLI and preserves custom Skill content. If the current Agent generator fails, report the warning and deterministic fallback; never silently switch to cloud generation.
