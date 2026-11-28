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
- `FINANCE_REPORT_ONLY=true`: compatibility rollback mode; preserves all report
  reads but rejects writes with HTTP 423 without invoking the fact handler.

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

### Management Test Costs

`POST /api/admin/finance-reporting/facts` accepts `kind: "test_cost"` with
`ref: "test:<request_id>:<attempt>:<channel_id>"` and a `test_cost` object
containing those three fields, `status` (`estimate` or `confirmed`), `coverage`
(`included`, `extra` or `unknown`), `evidence_type` (`quote` or `other` for an
estimate; `supplier_statement` for a confirmed supplier debit), `amount`, `refund`, `currency`, `rate`,
`evidence`, `coverage_evidence` and `refund_evidence`. It uses the existing
finance-write authorization, origin, 8KB, idempotency and `previous_id` rules.
The server checks the triple against the read-only settlement and attempt
snapshot. The attempt timestamp determines the Asia/Shanghai reporting month,
including for failed or retried attempts. A correction appends a new version;
the history remains intact. No request is classified as a test based on its
user ID or a client header.

For `included`, `coverage_evidence` must match the current complete channel
month bill evidence to establish coverage; this amount is a breakdown and is
never added to the bill. For `extra`, it must identify evidence that the expense
is outside that bill, not the bill's own evidence ID. Only a confirmed extra
amount, less a separately evidenced confirmed supplier refund, increases cost.
An estimate outside the bill, unknown coverage, a missing source, or an
unmatched bill leaves actual cost and profit pending. Refund evidence used for
deducted amounts cannot be reused across channel costs or extra test costs.
Test totals and CSV rows separate estimates from confirmed amounts. Amounts
are operator attestations; the service does not independently authenticate
supplier receipts. Quoted prices and platform wallet debits are not proof of
supplier cost. The module cannot discover unrecorded management tests: a
complete channel bill still requires the operator's whole-month attestation.

### Direct Supplier Verification Expenses

`direct_cost` is a separate immutable fact. It must not use a fabricated
settlement or attempt. Supply a verification ID and channel that match the
read-only `ztapi_model_verifications` snapshot, plus the supplier, a stable
non-secret account identifier, supplier bill ID, supplier bill occurrence time
with timezone, original amount and currency, supplier evidence, and independent
evidence linking the bill to that verification. The verification record alone
does not authenticate the bill link. One verification may have several bills.

The identity is SHA-256 of `direct-bill-v1:` followed by the lowercased bill
ID, independently of supplier and account labels. This conservatively treats
the same ID across suppliers as a conflict. A unique database head and transactional
compare-and-swap guard creation and revision across store instances. Revisions
append a new fact with the current `previous_id`. Use the same stable supplier
name and account identifier consistently. The idempotency key is a request retry
key, not the supplier identity. Model and input/output token counts are optional
operator-supplied bill evidence; they are not independently validated by the
verification record. Missing counts remain unknown, distinct from zero.
Optional cache read, 5-minute write and 1-hour write counts are separate evidence.
`input_token_scope` records whether the input includes cache (`inclusive`),
excludes it (`exclusive`), or is `unknown`; it does not derive a token total or
amount. Missing counts are JSON null and blank in CSV, not zero. Counts are
limited to 9007199254740991 to preserve exact values in the frontend.

Verification `verified_at` is recorded at the end of the probe. Bill occurrence
must fall within 15 minutes before or after it, including across month and time
zone boundaries. This is a conservative plausibility check, not proof of linkage;
an actual delayed supplier posting can be rejected and needs separate review.
Previously recorded out-of-window entries remain immutable but block profit
confirmation until corrected. Supplier and verification clocks are not checked.

The two user-supplied examples for verification 656 are Xcloud bill IDs
`202610291432465335980358268d9d6lTSMhN5F` (0.000022 credit) and
`202610291432494237804578268d9d6MZKPxGZo` (0.000020 credit). They are
examples, not preloaded records or proof of billing time, account, linkage,
conversion, refund status or monthly coverage. Enter each only after obtaining
the actual supporting evidence. Do not derive timestamps from bill IDs.

Unconverted credit is displayed in original units and leaves profit pending.
Converted costs require a rate and, for non-USDT currency, conversion evidence.

Converted request-linked Xcloud test costs also retain their original credit
amount and an explicit `rate_evidence`. A 100-for-200 recharge has cash rate
0.5, not a second group discount. Direct-test cash totals and request-linked
test totals are separate; monthly inclusion still requires coverage evidence.

### Compatibility Rollback

After introducing `test_cost` or `direct_cost`, a reader that does not recognize
these fact kinds is no longer a valid binary-only rollback target. The r2
release first verifies a compatible reader, then enables writes. Its rollback
container uses the same complete immutable store in report-only mode. Never
restore an older database or delete new facts to make an obsolete reader start.
The release records its minimum reader and supplies a checked rollback command.
For example, a 0.5 USDT-per-credit rate requires evidence of the actual
recharge terms and must not be inferred from a quoted pricing discount.
`included` requires the current complete matching monthly fact and its evidence;
it is never added to the month cost, and other same-supplier monthly bills must
explicitly exclude the direct identity. `extra` additionally requires every current
monthly cost for that supplier to explicitly list the direct identity in
`excluded_direct_bill_refs`, plus independent exclusion evidence. Legacy
request-linked test expenses lack structured supplier bill IDs, so any such
test in the month conservatively blocks direct-cost confirmation until the
overlap can be reconciled. Assertions are operator evidence, not verification by
the supplier. This module cannot discover unrecorded bills or account aliases.
An empty month without a supplier cost fact remains unconfirmed rather than
claiming zero cost or profit. SQLite lock/busy errors are not duplicate writes;
clients must retry with the original idempotency key after resolving contention.
The source database user requires SELECT on `ztapi_model_verifications`.

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
