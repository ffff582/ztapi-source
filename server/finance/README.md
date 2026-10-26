# Independent Administrator Finance Module

This module does not invoke main initialization, migrations on the production
schema, relay/model APIs, repricing or background billing workers. Its only
production-source access is SELECT through a dedicated restricted database user.

## Runtime

Build `go build ./cmd/ztapi-finance`. Environment:

- `FINANCE_READ_DRIVER`: mysql, postgres or sqlite.
- `FINANCE_READ_DSN`: SELECT-only source connection, never the main writer DSN.
- `FINANCE_STORE`: persistent private SQLite database path.
- `ZTAPI_SESSION_SIGNING_KEY`: same access-token verification key as the main API.
- `FINANCE_ADMIN_ORIGIN`: exact management origin, e.g. https://admin.ztapi.vip.
- `FINANCE_LISTEN`: defaults to 127.0.0.1:3011; container deployment uses :3011.
- `FINANCE_SOURCE_COMMIT`: published source commit exposed only as release metadata.

Only route `/api/admin/finance-reporting/` on the management hostname to this
service. Do not expose its port publicly or proxy it on the customer hostname.
The main API, login, balance ledger and routing configuration remain untouched.

## Accounting And Evidence

Financial annotations NEVER perform customer top-ups, debits, supplier refunds
or settlement actions. They append classifications/cost evidence in the private
store, retaining operator, time, idempotency key, hash and superseded version.
`GET history` provides the complete annotation audit trail.

Receipt values come from actual successful USDT transfers or explicit offline
cash evidence. Gifts/internal credits do not create cash income. Earned revenue
uses chronological weighted-average funded wallet basis, held across requests.
Pending reservations do not earn revenue; releases restore original basis.
Each independently charged retry owns its own basis and refund anchor. Unknown
opening credits, overdraft and inconsistent lineage block confirmed revenue.

Settlement posting time comes from immutable settlement outbox creation;
supplier customer refunds use their immutable wallet posting time. Later cash
recharges never reprice old usage. Annotated offline receipts may precede their
wallet credit date: receipt and consumption months remain independent.

Costs are actual supplier-consumed amounts, NOT supplier top-ups or quoted
estimates. Preserve original currency and explicit USDT conversion. A confirmed
supplier credit has its own evidence and original-bill reference; a prior-period
credit may exceed current consumption. Net cost can therefore be negative.
One whole-month channel attestation must include all historical credentials,
failures/retries and tests, solely attributable to this platform. Allocate shared
supplier invoices with distinct evidence references before entry. Current and
partial months cannot be presented as confirmed gross profit.

Confirmed gross profit means operator-attested evidence completeness, not an
independent guarantee that a supplier's billed amount is correct. Missing
evidence is displayed as null/pending, never as zero. Reports restate from the
latest immutable annotation version and include an as-of time.

## Operations And Recovery

Use a persistent, restricted private store directory; back up that database
alongside production database backups. Take a live SQLite backup with the SQLite
backup API, not an uncoordinated filesystem copy. Preserve the financial store
on deployment rollback. Reverting only the administrator index/proxy restores
the old UI without restarting the main API or touching customer balances.
The provided deployment registers a separate daily private-store backup at
19:47 UTC (03:47 Asia/Shanghai). It checks SQLite integrity, gzip round-trip,
`gunzip -t` and SHA-256, and reuses the existing Telegram failure-alert settings.
Existing main database and deployment backups are preserved.

Snapshot reads use a consistent read-only transaction, two source connections,
an eight-second request deadline, two concurrent requests and a 15-second cache.
Each source table is capped at 50,000 rows. Exceeding limits or unavailable data
fails closed; archive/paginate deliberately before that size is reached.

Local acceptance: `go test ./finance`, administrator Vitest suite, actual browser
checks on desktop/mobile, restricted-reader live smoke test and proof the main
API container ID/start time did not change. No paid model calls are needed.
