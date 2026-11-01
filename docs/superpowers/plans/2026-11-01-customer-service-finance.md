# Customer Service Finance

## Approved Scope

The platform-owned assistant uses DeepSeek Flash, with screenshots gated on an
actual authorized upstream vision acceptance. Its token use belongs in a separate
customer-service finance category, never in customer revenue or management tests.
No refunds, wallet balances, routes, historical bills, or hosted-search credentials
are changed. Paid acceptance for this work has not been performed.

## Read-Only Baseline

Fixed seven-day window: Unix 1792938130 through 1793542930, inclusive. Queries ran
in MySQL consistent-snapshot READ ONLY transactions, selecting metadata only.
The request-consumption projection has 548 consume rows and zero error rows.
Health evidence for non-admin users includes 453 valid outputs, four Opus length
terminations without visible output, five client cancellations, and six requests
not dispatched upstream. The four length outcomes have settled quota of 390.
The five cancellations remain pending, with charged_quota=0, net_charged_quota=0,
and reserved quota totaling 279133. Pending does not establish upstream zero cost.
Do not treat HTTP 200 or a consume row as proof of a usable answer. Do not infer a
client from the requested model or protocol.

## Implementation Sequence

1. [done] Read-only evidence and finance package baseline tests.
2. [done] Add an optional purpose to the existing immutable internal-call
   cost payload. Blank purpose remains management testing for old records and
   readers. Explicit customer_service is restricted to a verified admin-owned
   request/attempt, with nullable input/output/cache counters and input scope.
3. [done] Report customer-service rows and confirmed/estimated totals separately;
   preserve existing once-only monthly coverage and profit calculation. Keep the
   underlying fact kind readable by the preceding finance binary.
4. [done] Admin display and CSV export; read-only users cannot submit records.
5. [done locally] Backend/HTTP/store/UI regression, whole-change review and build.
   Production publication remains pending the assistant's automatic cost-emission
   integration. Preserve production identities, backups, rollback, and no main
   API restart. Do not invent live usage before the assistant is deployed.

## Verification And Rulings

- Backend baseline passed before edits. New cost classification, cache validation,
  owner-scope and CSV tests were observed failing before implementation.
- `go test ./finance ./cmd/ztapi-finance -count=1`: passed after the review fixes.
- `go vet ./finance ./cmd/ztapi-finance`: passed.
- Full classic frontend suite: 23 files, 254 tests passed after the fixes.
- Admin-target production build: passed. Local-only Chromium QA at 1440, 390,
  and 320 pixels: no document overflow, browser errors or unexpected requests;
  legacy report fallback passed. Screenshots use synthetic data only.
- Fresh review found missing role fields in Reader.Load and hosted-tool test
  contamination. Both were reproduced RED and fixed GREEN. Added a third guard:
  an immutable cost revision cannot strip or change its customer-service purpose.
- Ruling: keep the prior fact kind and optional new payload fields rather than a
  new kind/table -- old readers continue decoding without a finance 503. An old
  reader cannot display the new category; production rollback must stay read-only
  as already required by the finance release procedure.
- Ruling: customer-service hosted tool calls are exclusively itemized in the
  customer-service category, not customer/management-test tool statistics. Their
  supplier costs remain in the same monthly coverage calculation exactly once.
- Ruling: publish together with the assistant after its full cost-emission chain
  is tested -- deploying this foundation alone would not prove automatic tracking.
- No paid model calls, customer wallet writes, historical fact writes, price/route
  edits, production deployments or server configuration changes were performed.

## Verification Focus

Use TDD. Cover classification, old payload compatibility, immutable revisions,
idempotency, ordinary-user rejection, token zero versus missing, inclusive caches,
cash conversion exactly once, included/extra/unknown coverage, no double deduction,
CSV/UI parity and access controls. The broader approved assistant and handoff work
must bind this purpose server-side; a customer request header cannot choose it.
