# ZTAPI Financial Reporting

## Approved Scope

The owner approved an independent financial module on 2026-10-26. It reports
monthly customer receipts, consumption-related earned revenue, actual upstream
consumption and gross profit. It must not change wallets, pricing, routing or
restart the main API. Missing evidence remains explicitly unresolved.

## Approaches

1. Integrate reporting into the main API: fewer deployment units, but the current
   main process does not gracefully drain streams on restart.
2. Independent reporting process and administrator UI: selected by the owner.
   Read original records with a restricted SELECT-only connection; financial
   annotations live in a separate store. Publish the administrator UI and a
   dedicated proxy location without restarting the main API.

## Accounting

- Reporting timezone: Asia/Shanghai, calendar months, half-open intervals.
- Base currency: USDT. Preserve original currency and explicit evidence-backed
  conversion rate. Never silently treat CNY, USD, points and USDT as equivalent.
- Receipts are successful payment amounts at completion, not order creation.
  Unspent wallet credits are not earned revenue. Supplier deposits are not costs.
- Revenue allocation uses chronological weighted-average cash basis of wallet
  credits, including gifts. A 100 USDT receipt funding 105 U yields 20 USDT of
  earned revenue when 21 U is consumed. Later receipts never reprice earlier use.
- Reservation transfers basis to a request hold, not revenue. Final settlement
  recognizes only actual charge. Unused reservation/refund restores basis.
  A settlement equal to its reservation still recognizes once even without a
  settlement wallet delta. Pending requests remain holds and never income.
- Unknown opening funds, unclassified manual adjustments, unmatched ledger
  movements, missing finalization lineage or inconsistent balances are reported
  as blockers. No guessed zero-cost funding and no inferred cash from note text.
- Operator-funded tests can be classified as non-revenue credits. Their real
  upstream cost remains cost.
- Gross profit equals earned cash revenue less actual upstream net consumption.
  Quota face value and procurement-quote estimates are separate, never actual
  profit. Refunds are linked/reversed once; overlapping statements are rejected.
- Confirmed monthly profit requires complete revenue basis and a matching
  actual-cost statement for every used channel for the complete closed month.
  Current/unclosed months and incomplete evidence show pending profit, not zero.
- Independently billed retry attempts are separate charge events, linked by their
  immutable original ledger anchor. A supplier refund reverses that charge's
  basis, not the parent request's aggregate refunded counter.
- Consumption exceeding available funded basis is unresolved receivable, not
  earned cash income. Subsequent top-ups do not silently reprice the prior event.
- Charge month follows the immutable financial posting timestamp (settlement
  outbox creation, not mutable intent completion/cache synchronization time).
  Refunds affect their posting month. Evidence revisions append audit versions
  and restate the affected report with a new as-of timestamp.
- An operator's complete-month cost attestation covers all historical credentials,
  failed/retried requests and tests on the selected channel, solely for this
  platform. Shared-account totals need a documented allocation before entry.
  Reusing one evidence reference across channel totals is rejected; each allocated
  statement line must have its own reference. This is operator-attested actual
  cost, not an independent audit of supplier accuracy.

## Data And UI

Extend the existing management finance navigation with a monthly overview.
Retain existing orders, immutable balance ledger and reconciliation routes.
Provide month selection, receipt table, supplier-cost table, completeness issues,
CSV export, financial-only funding classification and cost entry forms.
Cost entries preserve supplier, historical channel identity, original amount,
currency, conversion, period, consumption/refund/deposit kind, evidence reference
and operator. Amendments append a superseding version; never mutate customer
records. Monthly statements are consumed cost net of confirmed supplier credits;
supplier refund records must not be counted again as additional negative cost.

## Isolation And Security

- A dedicated Go command does not invoke main initialization, model migrations,
  repricing, model health calls, funding watchers or other background jobs.
- A dedicated database account has SELECT only on required original records.
  The sidecar writes only its own financial annotation database.
- Reuse the current administrator access-token validation and explicit finance
  permissions; check live account role/status on every authenticated request.
  Do not expose supplier credentials, payment secrets or customer prompt bodies.
- Bounded snapshots, small connection pool, request deadlines and caching avoid
  unbounded scans on customer request paths. Read failures fail closed.
- Finance mutations use same-origin protection, permission checks, idempotency,
  bounded payloads and immutable audit records. Exports neutralize CSV formulas.
- Nginx routes only the dedicated financial prefix. Check configuration before
  graceful reload; preserve old hashed UI assets and rollback files. Verify the
  main container identity/start time is unchanged.
- No paid model calls are necessary for implementation or acceptance.

## Verified Baseline

Read-only preflight identified incomplete historical funding classifications and
missing supplier actual-cost statements. Exact counts and private financial
results remain in restricted deployment evidence, not in the public source tree.
They are observations, never seeded or invented financial facts.

## Acceptance

Test gifts, reservation releases, retries, exact-reservation settlement, customer
refunds, unknown opening balances, future funding, duplicate annotations, mixed
currencies, statement coverage, month boundaries, authorization and export safety.
Verify responsive administrator views, existing administrator tests and healthy
public endpoints. No main API restart or customer financial mutation is allowed.
