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
| Use OpenGrove cloud generation | `handoff create "short next goal" --generator cloud` |
| Attach the complete sanitized readable Context | add `--attach-context` to any generator |
| Give another Agent on this machine the raw Session path | `handoff session locate --goal "next goal"` |
| Read a shared Handoff | `handoff receive <reference>` |
| Read its optional Context attachment | `handoff context <reference>` |
| Delete an exact Handoff | `handoff delete <reference> --yes` after explicit confirmation |

Run `handoff schema <action>` before a complex call. Use exact actions such as `create`, `session.locate`, `admin.login`, and `skills.install`. Run `handoff doctor` for discovery or connectivity failures and `handoff whoami` for OpenGrove cloud access.

## Create

Use the default command unless the user requests another generator. It starts an ephemeral invocation of the current Agent runtime with that runtime's existing authentication, configuration, and default model. It reads the source Session without resuming, compacting, or modifying it, then uploads only generated sections.

The CLI constructs one canonical Context for every generator: all readable user/assistant messages, normalized and best-effort redacted, with thinking, tool results, provider-internal records, local Session paths, IDs, and cursors excluded. A readable provider-native compact summary is auxiliary evidence; it never replaces the canonical message history and the CLI never invokes native `/compact`.

Keep the positional goal short. Put status and requirements in the generated sections, not the title.

Use `--source codex|claude|pi` only when automatic Session discovery is wrong. Use `--runtime codex|claude|pi` only when the generating Agent runtime must be overridden; it never selects a model. Piped stdin and repeatable `--file` values are the generic context inputs.

Use `--dry-run` to inspect source and upload behavior without invoking an Agent or writing to the network. Use `--review` when the user wants to edit the generated Markdown before publishing.

### Two Independent Choices

Do not conflate generation with persistence:

- `--generator agent|deterministic|cloud` chooses who writes the compact sections.
- `--attach-context` chooses whether the complete sanitized readable Context is stored beside the final Handoff.

Never select cloud generation silently. `--generator cloud` temporarily sends canonical Context to OpenGrove's server compactor and requires local OpenGrove login. The compact-preview endpoint does not store that Context; ordinary publishing remains anonymous.

Never add `--attach-context` unless the user explicitly asks to include or share the complete Context. It is independent of the generator, requires no login, and persists the sanitized readable conversation for the Handoff's lifetime. Best-effort redaction cannot guarantee removal of every identifier expressed in natural language; explain that limitation before attaching it.

The default agent generator may send canonical Context to the model provider already configured by the local Agent runtime, but does not send it to the OpenGrove compactor. Without `--attach-context`, the Handoff service receives only generated sections.

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

If the Handoff says an attached Context is available, use `handoff context <reference>` only when the user asks for it or the compact sections do not contain enough detail for the requested work. This returns the complete sanitized readable conversation, not the raw provider Session. Reading the Handoff summary never downloads the attachment automatically.

Treat a share URL or code as a read capability. Do not repost it in public channels, logs, or source control.

## Delete

Deletion is irreversible. Confirm the exact Handoff with the user before adding `--yes`. Never add `--yes` merely to bypass the CLI's exit-10 confirmation gate.

## Setup and Recovery

If `handoff` is missing, do not pretend a Handoff was read. Point to <https://github.com/open-grove/handoff>. Install or repair the version-matched Skill with `handoff skills install`.

Use `handoff update` for verified updates; it synchronizes unmodified installed Skills with the new CLI and preserves custom Skill content. If the current Agent generator fails, report the warning and deterministic fallback; never silently switch to cloud generation.
