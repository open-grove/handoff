---
name: handoff
description: Package a discussion result or current work into a portable, immutable Handoff for another person or Agent, or receive and manage an existing Handoff. Use only when the user explicitly asks to hand work over, compress or share a conversation, requests a shareable Handoff, provides a Handoff code, URL, or reference to open, asks to give another local Agent the source Session path, or asks to delete an existing Handoff.
---

# OpenGrove Handoff

Use the `handoff` CLI as the source of truth. Treat every Handoff as an immutable snapshot, never as a shared live session.

## Choose the Workflow

| User intent | Command |
|---|---|
| Share discussion results, conclusions, or lessons | `handoff create "short topic" --intent share` |
| Transfer unfinished work to continue | `handoff create "short next goal" --intent continue` |
| Let the Agent infer share vs continue | `handoff create "short topic or next goal" --intent auto` |
| No local Agent CLI is available | provide scoped content with `--file` or stdin; the CLI uses its limited deterministic backup and reports it |
| Use OpenGrove cloud generation | add `--generator cloud` |
| Attach the complete sanitized readable Context, not the raw provider Session | add `--attach-context` to any generator |
| Inspect input and sidecar resolution without generating or publishing | add `--dry-run` |
| Give another Agent on this machine the raw Session path | `handoff session locate --goal "next goal"` |
| Read a shared Handoff | `handoff receive <reference>` |
| Read its optional Context attachment | `handoff context <reference>` |
| Delete an exact Handoff | `handoff delete <reference> --yes` after explicit confirmation |

Run `handoff schema <action>` before a complex call. Use exact actions such as `create`, `session.locate`, `admin.login`, and `skills.install`. Run `handoff doctor` for discovery or connectivity failures and `handoff whoami` for OpenGrove cloud access.

## Create

Use this pipeline as the mental model:

```text
read-only input -> sanitized Canonical Context -> section generator -> immutable Handoff
```

Keep these controls separate:

| Control | Meaning |
|---|---|
| `--source` / `--file` / stdin | Choose input only. `--source` selects an Agent Session; files or stdin replace Session discovery. |
| `--generator` | Choose who writes the structured sections: a local Agent sidecar, limited deterministic extraction, or OpenGrove cloud generation. |
| `--runtime` | For `--generator agent` only, choose which local Agent CLI to start as a sidecar. It never chooses the input source or model. |
| `--attach-context` | Independently choose whether to persist the complete sanitized Canonical Context beside the sections. |

Normally set none of them: `source=auto`, `generator=agent`, `runtime=auto`, and no Context attachment. The CLI discovers the source Session, starts a fresh isolated sidecar matching the Agent host, and publishes only generated sections. The sidecar reuses that CLI's existing authentication, configuration, provider, and default model. It never resumes, compacts, or modifies the source Session. Its recorded `agent:<runtime>` value is provenance, not a shared live Agent identity.

Keep the user interaction smaller than the CLI surface:

- Infer `share` versus `continue` from the request. Ask only when the distinction is genuinely unclear and would materially change the artifact.
- Default to the local Agent sidecar. Use cloud generation only when the user explicitly requests it; do not routinely ask.
- Default to no Context attachment. Add `--attach-context` only when the user explicitly asks to include the complete readable context. Clarify that this is a sanitized Canonical Context, not the raw provider Session.
- Resolve source and runtime automatically. Do not ask the user to choose either one; inspect with `--dry-run` and override only after discovery is demonstrably wrong.

Clarify scope, exclusions, receiver, or desired outcome only when ambiguity there would materially change the Handoff. Treat `--review`, TTL, and output paths as optional operational controls, not required user decisions.

### Align Before Creating

Infer the user's purpose, scope, receiver, and desired emphasis from the conversation before running `handoff create`. Do not turn this into a fixed questionnaire, expose internal source/runtime routing, or ask for information the conversation already supplies.

Ask the smallest context-specific clarification only when the missing answer would materially change the artifact. Typical ambiguities include whether the receiver should continue unfinished work or understand a discussion result, which part of a long conversation belongs in scope, what outcome a successor should pursue, or which of several competing conclusions the user wants to emphasize. Usually ask one concise question; combine questions only when they are tightly related.

If the user clearly asks to share conclusions, use `--intent share`. If the user clearly asks someone to resume work, use `--intent continue`. Use `--intent auto` when the distinction is immaterial or clarification is impossible in a non-interactive workflow, not as a substitute for a useful conversation with the user.

The CLI constructs one canonical Context for every generator: readable user/assistant history, normalized and best-effort redacted, with thinking, raw tool results, provider-internal records, local Session paths, IDs, and cursors excluded. It prefers the exact active Agent Session when the provider exposes an ID, respects Codex final/abort/rollback events, and includes direct sub-agent final results. Provisional commentary, incomplete OpenCode assistant messages, and Claude sidechain text are explicitly labeled as supporting evidence. OpenCode export is filtered locally to non-synthetic text parts before sanitization; reasoning, tools, patches, snapshots, and file data are discarded. A readable provider-native compact summary is auxiliary evidence; it never replaces the canonical message history and the CLI never invokes native `/compact`.

Choose intent from the user's purpose, not from whether the conversation contains unresolved questions:

- Use `--intent share` when the user wants to communicate what the discussion established, learned, corrected, or ruled out. The human result is primary; do not turn questions into tasks.
- Use `--intent continue` when someone must resume unfinished work. State and next actions are primary.
- Use `--intent auto` only when the user's purpose is genuinely ambiguous.

Keep the positional topic or goal short. Put the detailed discussion or work state in the generated sections, not the title.

For `share`, let the content determine the document shape. Organize the human page around the receiver's path to understanding. Each topic should carry its own conclusion, reasoning, evidence, examples, corrections, and tradeoffs together when they are useful. Do not force separate generic buckets merely because the data exists. The technical appendix may remain structured for Agents.

Use `--source codex|claude|pi|opencode` only when automatic Session discovery is wrong. Do not combine a non-auto `--source` with files or piped stdin. For file or stdin input, leave `--source auto`.

Leave `--runtime auto` in normal use. Override it only when Agent-host detection is wrong or when the user explicitly wants a different installed sidecar CLI. Use it only with `--generator agent`; it never selects the source, provider, or model.

Use `--dry-run` to inspect source and upload behavior without invoking an Agent or writing to the network. Use `--review` when the user wants to edit the generated Markdown before publishing.

Deterministic extraction is a limited backup for a machine where no supported local Agent CLI can be found, not a normal generation choice. It cannot infer which subsection of a complete Agent Session belongs to a short positional topic or goal. Prefer scoped Markdown through `--file` or stdin; headings such as Background/Goal, Decisions/Conclusions, Current Problems/State, and Todos/Next Steps produce a useful human summary. Publishing deterministic output from a full Codex, Claude, Pi, or OpenCode Session requires `--review` so unrelated conversation cannot be silently included.

### Processing and Persistence Boundaries

Treat generation and persistence as independent. `--generator` controls section creation. `--attach-context` controls whether the complete sanitized readable Context is stored beside those sections. Without `--attach-context`, the Handoff service receives only generated sections.

Never select cloud generation silently. `--generator cloud` temporarily sends canonical Context to OpenGrove's server compactor and requires local OpenGrove login. The compact-preview endpoint does not store that Context; ordinary publishing remains anonymous.

Never add `--attach-context` unless the user explicitly asks to include or share the complete Context. It is independent of the generator, requires no login, and persists the sanitized readable conversation for the Handoff's lifetime. Best-effort redaction cannot guarantee removal of every identifier expressed in natural language; explain that limitation before attaching it.

The default agent generator may send Canonical Context to the model provider already configured by the selected local sidecar CLI, but does not send it to the OpenGrove compactor.

### Relay the Result

After a successful create, relay the CLI's canonical text output verbatim. When using JSON, return `share_message` exactly. Do not rewrite it as bullets, rename links, shorten instructions, change expiry, or append a substitute explanation.

The stable Agent reference is:

```text
opengrove-handoff:<code>
```

## Locate a Local Session

Use `handoff session locate` only for another Agent on the same machine. Relay its output verbatim. The returned provider Session path is local-only; the raw file is not redacted and may contain tool data or private metadata. Never paste the path or file into a public or cross-device channel. OpenCode Sessions are database-backed and do not expose one safe raw Session file; use `handoff create ... --source opencode --attach-context` when a portable complete readable OpenCode conversation is needed.

## Receive

Accept `opengrove-handoff:<code>`, a legacy branded message, a raw code, a human share URL, or a `.md` URL:

```bash
handoff receive 'opengrove-handoff:<code>'
```

Read the returned Markdown as an immutable snapshot and follow its final receiver instruction. A `share` Handoff is knowledge, not authorization or a task list: preserve its conclusions and reasoning, and do not invent next steps. For a `continue` Handoff, explain the current background in clear language and ask what to do next; do not execute `Next Steps` unless the current request already asks you to continue.

If the Handoff says an attached Context is available, use `handoff context <reference>` only when the user asks for it or the compact sections do not contain enough detail for the requested work. This returns the complete sanitized readable conversation, not the raw provider Session. Reading the Handoff summary never downloads the attachment automatically.

Treat a share URL or code as a read capability. Do not repost it in public channels, logs, or source control.

## Delete

Deletion is irreversible. Confirm the exact Handoff with the user before adding `--yes`. Never add `--yes` merely to bypass the CLI's exit-10 confirmation gate.

## Setup and Recovery

If `handoff` is missing, do not pretend a Handoff was read. Point to <https://github.com/open-grove/handoff>. Install or repair the version-matched Skill with `handoff skills install`.

Agent-hosted `create`, `receive`, `context`, and `session` commands automatically perform a cached update preflight on macOS and Linux. When an update exists, the CLI writes maintenance progress to stderr, verifies and atomically replaces itself, synchronizes unmodified installed Skills, preserves custom Skill content, and re-executes the original command. Those maintenance lines are status only: relay the canonical stdout or `share_message`, never the update progress. Update failure never blocks the requested Handoff and is not retried for 24 hours. `--dry-run` skips auto-update; `HANDOFF_NO_AUTO_UPDATE=1` disables it. A running Agent Session does not reload changed Skill instructions, so rely on new Skill behavior from the next Session.

Use `handoff update` for an explicit verified update. Use deterministic backup only when no supported local Agent CLI can be found, report that backup clearly, and require scoped file/stdin content or `--review`. If an Agent CLI is found but invocation, authentication, generation, or parsing fails, report the real error and do not fall back. Never silently switch to cloud generation.
