# Long Text And Secret Boundary Repair

The owner approved removing the text-workbench4000-character restriction, fixing text-attachment truncation and preventing leaks through errors, response headers and logs. Test before deploy; do not interrupt users or change keys, wallets, prices or model channels.

## Scope

- Preserve full typed text and attachment contents; no silent trimming. Only text workbench/its chat client change, not media or support message limits.
- Keep bounded resources: text attachments at most1MiB each, serialized full chat request at most4MiB including history. Reject explicitly before network dispatch; retain draft/history. These are transport/memory boundaries, not a new character cap or a claim about model context size.
- Keep existing max_tokens256 and existing upstream/model context limits; do not invent unknown model context metadata or change sales/billing behavior.
- Public upstream errors use a safe structured boundary: no arbitrary vendor metadata, credentials or raw stack/environment content. Preserve status/category, local request ID and known actionable error categories. Internal classification/billing must retain original error semantics; logs only receive redacted evidence.
- Response headers use a deliberate protocol allowlist; drop auth/key/cookie/redirect/hop-by-hop/CORS/unknown provider headers. Preserve local request ID, content-type, streaming and useful safe rate/retry information.
- Shared credential-only redaction covers common assignment/JSON/header/env/URL formats and known token prefixes without redacting usage/cost counters. Apply to runtime logs, new error-log records and customer read projection; do not rewrite historical database rows.
- SQL logging must not interpolate bound parameters. Preserve SQLite/MySQL/PostgreSQL support, crypto identities and source attribution.

## Verification And Release

Use TDD with synthetic sentinels, no real keys or paid models. Cover OpenAI/Claude/Responses errors, SSE/tool continuation, billing request IDs and token/cost fields, binary/media response compatibility, frontend long Chinese/emoji/attachments/history/account switching and over-budget rejection.

Freeze only authorized files on deployed combined source901ccb536652eb8c78879f4d7e1b3368276c5b87. Main backend baseline5e716eee834fca4b70f8f47a7ee2935dc8fb9160. Preserve dirty worktree HEAD/index, public docs, support/bot/finance sidecars and all credentials/backups. Publish exact AGPL source and immutable artifacts.

Release must maintain a serving backend, drain existing streams and finish pending batch accounting before stopping an old backend. Validate compatibility on isolated/fake upstreams. If the safe handoff cannot be proven, do not force-stop users. No new supplier model calls, key rotation, wallet adjustment, price/channel writes or SSH changes.
