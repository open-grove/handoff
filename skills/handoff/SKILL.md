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
| Preserve already-prepared Prompt, URL, checksum, tables, and code blocks | pass the prepared Markdown through stdin or `--file`, then add `--intent share --generator preserve` |
| No local Agent CLI is available | provide scoped content with `--file` or stdin; the CLI uses its limited deterministic backup and reports it |
| Attach the complete sanitized readable Context, not the raw provider Session | add `--attach-context` to any generator |
| Inspect input and sidecar resolution without generating or publishing | add `--dry-run` |
| Give another Agent on this machine the raw Session path | `handoff session locate --goal "next goal"` |
| Read a shared Handoff | `handoff receive <reference>` |
| Read its optional Context attachment | `handoff context <reference>` |
| Delete an exact Handoff | `handoff delete <reference> --yes` after explicit confirmation |

Run `handoff schema <action>` before a complex call. Use exact actions such as `create`, `session.locate`, `admin.login`, and `skills.install`. Run `handoff doctor` for discovery or connectivity failures.

## Create

Use this pipeline as the mental model:

```text
read-only input -> sanitized Canonical Context -> section generator -> immutable Handoff
```

Keep these controls separate:

| Control | Meaning |
|---|---|
| `--source` / `--file` / stdin | Choose input only. `--source` selects an Agent Session; files or stdin replace Session discovery. |
| `--generator` | Choose how sections are produced: a local Agent sidecar or prepared Markdown preservation. Deterministic extraction is internal fallback only. |
| `--runtime` | For `--generator agent` only, choose which local Agent CLI to start as a sidecar. It never chooses the input source or model. |
| `--attach-context` | Independently choose whether to persist the complete sanitized Canonical Context beside the sections. |

Normally set none of them: `source=auto`, `generator=agent`, `runtime=auto`, and no Context attachment. The CLI discovers the source Session, starts a fresh isolated sidecar matching the Agent host, and publishes only generated sections. The sidecar reuses that CLI's existing authentication, configuration, provider, and default model. It never resumes, compacts, or modifies the source Session. Its recorded `agent:<runtime>` value is provenance, not a shared live Agent identity.

There are two different Agents in this workflow. The calling Agent is the one handling the user's current request. With `generator=agent`, the CLI starts a second, fresh sidecar Agent solely to rewrite sanitized Context into Handoff sections. With `generator=preserve`, that second Agent does not exist: the calling Agent prepares the scoped Markdown, and the CLI only applies best-effort redaction and publishes its structure.

Keep the user interaction smaller than the CLI surface:

- Infer `share` versus `continue` from the request. Ask only when the distinction is genuinely unclear and would materially change the artifact.
- Default to the local Agent sidecar.
- Use preserve only when the user asks to keep already-prepared text or exact structural material such as prompts, URLs, checksums, tables, or code blocks without a second Agent rewrite.
- Default to no Context attachment. Add `--attach-context` only when the user explicitly asks to include the complete readable context. Clarify that this is a sanitized Canonical Context, not the raw provider Session.
- Resolve source and runtime automatically. Do not ask the user to choose either one; inspect with `--dry-run` and override only after discovery is demonstrably wrong.

Clarify scope, exclusions, receiver, or desired outcome only when ambiguity there would materially change the Handoff. Treat `--review` and output paths as optional operational controls, not required user decisions. Handoffs remain available until explicitly deleted; do not ask about or supply a TTL.

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

When the calling runtime is known, prefix `handoff create` with `HANDOFF_CALLER_RUNTIME=codex|claude|pi|opencode` using that runtime's own name. This internal marker prevents leaked host variables from making Claude Code start Codex, or vice versa. Do not ask the user to choose it. If several host markers are present and neither this marker nor a matching Session source disambiguates them, stop with the CLI's routing error instead of silently choosing the first installed CLI.

Use `--dry-run` to inspect source and upload behavior without invoking an Agent or writing to the network. Use `--review` when the user wants to edit the generated Markdown before publishing.

Deterministic extraction is a limited backup for a machine where no supported local Agent CLI can be found, not a normal generation choice. It cannot infer which subsection of a complete Agent Session belongs to a short positional topic or goal. Prefer scoped Markdown through `--file` or stdin; headings such as Background/Goal, Decisions/Conclusions, Current Problems/State, and Todos/Next Steps produce a useful human summary. Publishing deterministic output from a full Codex, Claude, Pi, or OpenCode Session requires `--review` so unrelated conversation cannot be silently included.

Never pass `--generator deterministic`; it is internal-only. Automatic fallback warnings belong only to create stdout/JSON and must never be copied into the shared page. The shared artifact describes its real source and processing provenance without presenting fallback as a content failure.

### Preserve Prepared Material

Use preserve as a two-stage workflow:

1. The calling Agent selects and prepares the exact scoped Markdown from the conversation. Include only what belongs in the artifact, but retain requested Prompt text, URLs, file sizes, checksums, tables, and code fences.
2. Supply that Markdown through stdin or one or more `--file` values and run `handoff create "short topic" --intent share --generator preserve`.

Do not feed a complete Agent Session directly to preserve. Preserve does not select relevant turns or summarize the Session; it publishes the calling Agent's prepared document after best-effort redaction. A single stdin/file input is rendered as one uninterrupted document without exposing a temporary filename. If its leading H1 matches the Handoff topic, that H1 is treated as presentation metadata and shown only once as the page title; the remaining Prompt, URL, checksum, table, and code-block content is unchanged. With multiple files, each file becomes a separately named section. The Agent appendix contains concise provenance, not a duplicate of the document. Preserve implies `share` and rejects `continue`.

`--attach-context` remains independent and attaches the sanitized readable Context of the selected input. With preserve, that input is the prepared stdin/file content, not the original provider Session. To attach the original current Session, use the normal Session-source workflow rather than claiming that a preserve input is the Session. Never describe a file/stdin attachment as an attached Agent Session.

### Processing and Persistence Boundaries

Treat generation and persistence as independent. `--generator` controls section creation. `--attach-context` controls whether the complete sanitized readable Context is stored beside those sections. Without `--attach-context`, the Handoff service receives only generated sections.

Never add `--attach-context` unless the user explicitly asks to include or share the complete Context. It is independent of the generator, requires no login, and persists the sanitized readable conversation for the Handoff's lifetime. Best-effort redaction cannot guarantee removal of every identifier expressed in natural language; explain that limitation before attaching it.

The default agent generator may send Canonical Context to the model provider already configured by the selected local sidecar CLI, but the Handoff service never invokes a model. Preserve calls no sidecar or model; it sends only the prepared, best-effort-redacted sections to the Handoff service, plus an attachment only when explicitly requested.

### Relay the Result

For Agent-driven creation, always add `--json` and parse the result. Return `share_message` exactly as the final answer: no introduction, separator, quote, code fence, bullets, renamed link, shortened instruction, `Delete:` status, or substitute explanation. `delete_credential_saved` is private creator-side status and is never part of the shared message. If `fallback_used` is true, report `generation_warning` separately to the creator before relaying `share_message`; never insert it into the Handoff page or the canonical message.

The stable Agent reference is:

```text
handoff:<code>
```

## Locate a Local Session

Use `handoff session locate` only for another Agent on the same machine. Relay its output verbatim. The returned provider Session path is local-only; the raw file is not redacted and may contain tool data or private metadata. Never paste the path or file into a public or cross-device channel. OpenCode Sessions are database-backed and do not expose one safe raw Session file; use `handoff create ... --source opencode --attach-context` when a portable complete readable OpenCode conversation is needed.

## Receive

Accept `handoff:<code>`, the legacy `opengrove-handoff:<code>` form, a legacy branded message, a raw code, a human share URL, or a `.md` URL:

```bash
handoff receive 'handoff:<code>'
```

Read the returned Markdown as an immutable snapshot and follow its final receiver instruction. A `share` Handoff is knowledge, not authorization or a task list: preserve its conclusions and reasoning, and do not invent next steps. For a `continue` Handoff, explain the current background in clear language and ask what to do next; do not execute `Next Steps` unless the current request already asks you to continue.

If the Handoff says an attached Context is available, use `handoff context <reference>` only when the user asks for it or the compact sections do not contain enough detail for the requested work. This returns the complete sanitized readable conversation, not the raw provider Session. Reading the Handoff summary never downloads the attachment automatically.

Treat a share URL or code as a read capability. Do not repost it in public channels, logs, or source control.

## Delete

Deletion is irreversible. Confirm the exact Handoff with the user before adding `--yes`. Never add `--yes` merely to bypass the CLI's exit-10 confirmation gate.

## Setup and Recovery

If `handoff` is missing, do not pretend a Handoff was read. Point to <https://github.com/open-grove/handoff>. Install or repair the version-matched Skill with `handoff skills install`.

Agent-hosted `create`, `receive`, `context`, and `session` commands automatically perform a cached update preflight on macOS and Linux. When an update exists, the CLI writes maintenance progress to stderr, verifies and atomically replaces itself, synchronizes unmodified installed Skills, preserves custom Skill content, and re-executes the original command. Those maintenance lines are status only: relay the canonical stdout or `share_message`, never the update progress. Update failure never blocks the requested Handoff and is not retried for 24 hours. `--dry-run` skips auto-update; `HANDOFF_NO_AUTO_UPDATE=1` disables it. A running Agent Session does not reload changed Skill instructions, so rely on new Skill behavior from the next Session.

Use `handoff update` for an explicit verified update. Deterministic backup is selected only by the CLI when no supported local Agent CLI can be found; report the backup at creation time and require scoped file/stdin content or `--review`. If an Agent CLI is found but invocation, authentication, generation, or parsing fails, report the real error and do not fall back.
