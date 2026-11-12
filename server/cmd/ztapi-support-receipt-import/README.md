# Acceptance Receipt Import

Offline, append-only import of isolated customer-service acceptance evidence.
This command does not call any provider, Telegram API, main API or finance API.

## Interface

- `SUPPORT_IMPORT_STORE`: canonical absolute path of the existing support SQLite
  database, already in WAL mode. No new database or support-schema upgrade is
  allowed.
- `SUPPORT_IMPORT_MANIFEST`: absolute path of a protected regular JSON file, at most 64 KiB.
  Symlinks are rejected. Unix group/world permissions must be absent. On Windows,
  only the current user, Administrators and SYSTEM may have allow permissions.
- `SUPPORT_IMPORT_SHA256`: explicit lowercase SHA256 of the exact manifest bytes.
- `SUPPORT_IMPORT_OPERATOR_ID`: explicit positive, platform-owned Telegram operator ID. The
  operator must be supplied by the parent; no account or credential lookup runs.

The manifest is exactly `{"release":"<40 lowercase hex>","usages":[...]}`.
Each usage is the original `support.FinancialUsage`, not a platform settlement.
Duplicate JSON members, extra fields and trailing JSON values are rejected.
Only these four environment variables are read; CLI arguments are rejected.
The parent may run the command with Docker `--network none`, the existing bot
data volume mounted read-write and the manifest mounted read-only, after
verifying that the old bot container has `Running=false`.

## Operational Boundary

The parent must gracefully stop **only the support bot** first. The command
holds `<SUPPORT_IMPORT_STORE>.runtime.lock` until the SQLite connection closes.
It fails immediately if the bot or another importer owns this lease. Finance
and the main API need no restart. No interruption recovery or model retries run.

Usages must be from `telegram-acceptance-v2`, supplier `xcloud`, channel `8`,
model `deepseek-v4.1-flash`, purpose `customer_service`, with `dispatched=true`.
Only `inclusive` input scope is accepted because the existing finance reader
projects that scope. Batches contain 1..5 unique 32-lowercase-hex CallIDs.
Start/completion timestamps must be within the preceding 24 hours, with at most
30 seconds of future clock skew. The same validation applies on re-import.

Original token/cache nulls, timestamps, request IDs, quote evidence, quote strings
and credit strings are preserved. Quotes are at most 0.1 U and must equal credit
times 0.5 exactly; no additional discount is applied. A quoted unknown cache is
accepted only with `support.UnknownCacheUpperBoundSuffix`. Actual cost must be
null and actual evidence empty. Missing completion means no receipt is inserted;
receipt-only fields must also be absent, rather than silently discarded.
Amount strings must match the existing finance reader: canonical nonnegative
decimal notation with at most 12 fractional digits, never exponent notation,
leading zeroes, rounding or normalization of the original string.

The sole new table is `support_receipt_import_audits`, with immutable SQLite
triggers against update, delete and replacement. Imports create only private
administrative system messages, conversations, calls and available receipts.
They never create actor dialogue mappings or customer settlement lineage.

The batch and audit commit atomically. Identical re-import adds zero; changed
replays, existing production CallIDs and tampered persisted evidence conflict.
Stdout contains only `{"imported":N,"skipped":N,"total":N}`. A count-output failure after
commit is explicitly identified; retrying the identical pinned manifest is safe.

No actual import is part of implementation or automated verification. Tests use
temporary databases and manifests only.
