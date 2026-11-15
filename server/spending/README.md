# Spending Sidecar

Independent, OFF-by-default customer notifications. This package does not change
existing invoices, exports, balances, keys, channels, support or bot processes.
All successful API responses use `{ "success": true, "data": ... }`; errors use
`{ "success": false, "message": "fixed_public_code" }`.

## API

- `GET /api/spending/config`: data contains `preferences`, `telegram_bound`,
  `telegram_available`, `timezone`. Preferences match the approved spec.
- `PUT /api/spending/config`: all seven fields are required: `enabled`,
  `daily_spend_u`, `hourly_spend_u`, `low_balance_u`, `input_tokens`, `telegram`,
  `version`. Data is the saved preferences. CAS conflicts return 409.
  Enabled Telegram opt-in also requires current sender availability and a
  verified positive private binding, otherwise 409 without changing preferences.
  Disabling always skips that optional dependency: the Telegram preference is
  retained, pending deliveries are cancelled, and disabled rules never send.
- `GET /api/spending/overview`: approved overview plus
  `confirmed_refunds_daily_u`. Confirmed spend is GROSS, refunds are separate.
- `GET /api/spending/alerts?limit=20&before=123`: data contains `items` and
  owner-wide `unread_count`. Maximum limit 100; before is optional and positive.
- `POST /api/spending/alerts/{id}/read`: JSON `{}`; owner-only, idempotent.
- `GET /healthz`: minimal `{ "ok": true }`; startup validates source projections.

All customer routes require the existing Bearer access JWT and matching
`New-Api-User`, then recheck active/nondeleted ownership. Writes require exact
`Origin` and reject cross-site fetches. JSON is strict, duplicate-free and <=8KiB.
HS256 uses the existing shared signing key, which also has signing capability;
this is not a verification-only credential. No refresh/login endpoint is added.

## Charge Coverage

Primary settled quota is counted once per operation, independently verified
against its reservation/settlement/release ledger sum. Additional billing proofs
are counted only through a distinct billing operation and exact debit ledger.
Primary refund-charge anchors are NOT another charge. Hosted tools have their
own operations and are included once without filtering text/model families.
Confirmation windows use `ztapi_settlement_log_outboxes.created_at` Unix seconds,
not request start, mutable updated_at, log timestamp or delivery timestamp.
Beijing daily and rolling 60-minute windows are half open `[start, as_of)`.

Only posted applied/completed refunds with matching charge and wallet ledger are
reported, by refund ledger time. Pending refunds are never subtracted. Quota/U
is the owner-live-verified fixed deployment value 500000. Snapshots containing
`quota_per_unit` or `ztapi_quota_per_unit` must agree. Older text snapshots without
that field use the verified deployment configuration, never current sale prices.

Legacy logs overlapping managed requests or their receipt IDs are excluded.
Other legacy consumption logs are NOT assumed to be final wallet evidence:
overview complete=false and confirmed amounts=null. Unmatched usage/refund
ledgers also fail closed. Legacy finality is intentionally not reconstructed by
guessing from logs. Input notices require upstream-reported usage provenance and
the unique ordinary `input_tokens` dimension in `charge_dimensions_json`, decoded
as `model.ZTAPISupplierRefundDimension.Units` (a canonical positive integer string).
DTO input_tokens/prompt_tokens are not authoritative ordinary-input counters.
Absent/duplicate/invalid units do not become zero and cannot trigger a notice;
cache read/write dimensions are never added to ordinary input.

All source queries have a 3-second deadline and run on at most two connections
in the runtime. Financial reads use a consistent read-only transaction. Pages
are 32 records; scope lists use chunks of 64. The initial projection selects only
primary and extra operations whose outbox confirmation is in the current daily/
rolling-hour union. Today's posted refunds select their original requests too.
Only those requests' parent/ledger/charge proofs are materialized, including an
old parent for a newly confirmed extra. Pending uses COUNT, never history paging.
Legacy anti-joins exclude managed and extra log receipts in SQL. Recent settled
rows with missing confirmation time and current unproven ledgers mark coverage
unknown; unrelated old bad rows do not block today's total. updated_at is ONLY
an anomaly hint, never a posting time. A verified wallet remains available for
low-balance checks even when financial evidence fails.

Each current-window/proof projection is capped at 2048 records, with an 8MiB JSON
budget. Current-window overflow makes totals unavailable rather than partial;
it is not a lifetime per-owner cap. No primary index/migration is installed.
Row/query/memory bounds are tested, but they do not prove indexed physical I/O:
the release owner must check live MySQL/PostgreSQL EXPLAIN and required time/
join indexes. Local execution tests use SQLite only.
SQLite TEXT datetime filters normalize both stored offsets and UTC parameters
with julianday. MySQL/PostgreSQL retain direct datetime range predicates; Unix
outbox/log windows and the source-column grant dictionary are unchanged.

## Exact Source SELECT Grants

Use a NEW dedicated SELECT-only account. Do not enlarge support's grants.
Column grants must include filter/join/order columns, not only output columns.
The ONLY canonical machine-readable dictionary is
`spending.RequiredSourceColumns() map[string][]string` in source_columns.go.
It returns fresh map/slice copies without DB/environment/secret reads or writes.
Preflight uses that same metadata. The release audit CLI should marshal its
return value with common JSON wrappers, freeze/hash it, and validate an explicit
safe allowlist before issuing column grants. Do not infer grants with regex or
maintain a second deployment dictionary from this README.

It still lists the same seven existing tables. Relative to the initial grant,
the settlement additions are charge_dimensions_json and updated_at. The latter
only detects recent missing-confirmation anomalies. Settlement created_at is
not queried and is not requested; created_at is required on outboxes, ledgers
and logs. No account key/content/other/submission/credential columns are added.

The owner verified LOG_SQL_DSN is empty: logs must be in this same source DB.
No channel, token/key, prompt, response content, private refund submission,
supplier credential or JWT session table access is required.

## Runtime

Build from `server` with the existing module and offline cache:

```powershell
$env:GOPROXY='off'; $env:GOSUMDB='off'; $env:GOMAXPROCS='2'
go test -p 2 ./spending ./cmd/ztapi-spending
go build -p 2 ./cmd/ztapi-spending
```

Required: `SPENDING_READ_DSN`, absolute `SPENDING_STORE`,
`SPENDING_PUBLIC_ORIGIN` (HTTPS origin only), `ZTAPI_SESSION_SIGNING_KEY`.
`SPENDING_LISTEN` defaults to `127.0.0.1:3014`.
`SPENDING_READ_DRIVER` defaults to mysql (postgres/sqlite also accepted).
`SPENDING_QUOTA_PER_UNIT` defaults to the verified 500000; any other value fails
startup. MySQL forces parseTime, Beijing DATETIME decoding and 3-second timeouts.
The runtime never calls InitDB or any main-schema migration. Only the isolated
SQLite store is migrated. It refuses existing unrelated tables and path overlap
with the binding/config/source file. Restrict and persist its directory.
Before opening/migrating the private DB or recovering sending records, the
runtime exclusively locks `<SPENDING_STORE>.runtime.lock` using the same native
Unix/Windows locking pattern as support. A second process cannot recover another
worker's sending state. The private SQLite connection URI and startup both set
synchronous=FULL; WAL and the dedup/delivery state stay in the new private store.

Optional `SPENDING_BINDING_STORE` is the absolute existing SITE support SQLite
path. Mount its containing directory read-only, including live WAL/SHM; use
mode=ro/query_only. Do not set immutable=1 on a live WAL database. No migration
or support write is attempted. Lookup reads only
`support_telegram_bindings(user_id,telegram_id,generation)`.
Optional `SPENDING_TELEGRAM_CONFIG` is a protected regular JSON file containing
only `{ "bot_token": "..." }`; mode 0600/0400 on Unix, restricted ACL on Windows.
No new token is generated. Optional configuration failures disable delivery,
not the inbox. An unavailable binding file at startup requires a sidecar restart
after repair; it never restarts the existing bot.

Only bilingual generic sendMessage is implemented, to a positive verified private binding,
with a fixed ZTAPI link and no amount, account or request identifiers. There is
no getUpdates, webhook mutation, polling bot, model call or administrator fallback.
Preferences, active account and binding generation are rechecked after claiming
delivery. Telegram has no transactional send/SQLite boundary: concurrent unbind
immediately after the last check cannot be made atomic without changing the
existing binding service. HTTP timeouts/5xx/malformed responses become unknown,
are never automatically retried, and a crashed sending state is recovered as
unknown. Only explicit 429 retry_after may retry, at most three total attempts,
after all revalidation. Delivery states: not_requested,pending,sending,sent,
cancelled,blocked,failed,unknown. Store availability never gates the model API.
Transient account/binding reads leave unsent notifications pending for later
revalidation, without calling the sender. Actual disable/unbind cancels them.
HTTP 5xx stays unknown even if its body claims a 400/403 error. RunOnce stops on
context expiry without advancing past owners it did not process.

Daily notices deduplicate per owner/day. Hourly notices have >=60-minute
cooldown. Low balance rearms only on recovery and still has a 24-hour cooldown.
Input notices deduplicate owner/request and do not backfill before enabling.
Repeated saves do not reset rule state. Any save cancels pending notifications
from the old preference version; disabling never dispatches queued old alerts.
