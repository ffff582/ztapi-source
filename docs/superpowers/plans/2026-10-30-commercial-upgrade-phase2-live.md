# ZTAPI Phase 2 Live And Release Plan

> **For agentic workers:** Use superpowers:subagent-driven-development or superpowers:executing-plans. Do not skip the Phase 1 gate or call a scripted-provider pass a live-provider pass.

**Goal:** Qualify the admitted live routes within the approved budget, account for every attempt, and release the tested backend without repricing or overwriting unrelated work.

**Architecture:** Keep simulation and paid runners separate. Use the existing server-only acceptance listener through strict-host-key SSH, temporary model-limited tokens, durable budget reservations and immutable main/finance evidence. Exercise the candidate binary and compatible rollback on an isolated MySQL copy before any production cutover.

**Tech Stack:** Existing Go gateway, official pinned JavaScript SDKs, Node.js, MySQL, Docker and the independent finance module.

**Spec:** `docs/superpowers/specs/2026-10-30-commercial-upgrade.md`.

## Global Constraints

- Approved total ceiling 10U; all attempts of any one model together must stay at or below 1U.
- Reserve the maximum of platform debit and evidenced supplier cash exposure, not their sum. Unknown cost keeps its entire reservation. No automatic refund or budget recycling based on a timeout or missing reply.
- Only published, quotation-authorized route IDs. Freeze model versions and publication snapshot IDs before requests; check actual attempts against that list afterward. Do not publish, reprice, change channel keys or widen resource eligibility.
- Temporary test tokens expire in ten minutes, have a finite quota and one model limit, and disable cross-group retry. Revoke them in finally and record any cleanup failure.
- No public customer endpoint for paid acceptance. SSH must use existing strict known-host verification and the server-only listener on 127.0.0.1:18081.
- Never persist tokens, raw customer prompts, supplier keys or admin login responses. No shell interpolation of secrets.
- Keep every actual attempt, including retries, cancellations and non-2xx responses, linked to a main settlement before recording finance facts. Never invent source rows to satisfy finance validation.
- Xcloud credit costs use the actual group-billed amount times the confirmed recharge cash factor 0.5 exactly once. Yunxin quote estimates are not next-day supplier statements.
- No production database restoration on rollback. Preserve active finance facts and a compatible reader.

## Review Focus

- A hung request or unknown supplier charge must not release budget and enable more tests.
- One CLI action can make several inference requests; the admission proxy, not a verbal estimate, must bound each request.
- Actual tool execution, terminal SSE, positive output and matching settlement are different checks.
- Current-day Yunxin supplier receipts are unavailable until the next day; report this as missing external evidence, not zero cost or a confirmed profit.
- A clean source manifest and a healthy container do not prove that the deployed binary is the tested one.

## Task 1: Exact Budget And Test Cases

**Files:** `tests/compatibility/budget.mjs`, `.test.mjs`; separate live runner and tests; external immutable run receipts.

- [ ] TDD exact decimal budgets, cumulative per-model caps, idempotency and unknown-cost holds. Reject ambiguous numeric inputs and changes to confirmed entries.
- [ ] TDD strict loopback endpoint, redirect prevention, authorized-route manifest, exclusive evidence output, redacted errors and no SDK inference retry. Existing managed gateway retries are bounded to two authorized channels; reserve both possible attempts before the client request, retain unknown-cost holds and collect every actual attempt. Do not change global routing for acceptance.
- [ ] Start with the 14 published text models from the fresh catalog. Use small synthetic prompts and explicit output bounds; each request reserves a conservative cap before dispatch. Image/video tests require an exact admitted price dimension that fits the remaining per-model cap, otherwise record a bounded non-test reason.
- [ ] The first live tests qualify the existing deployed baseline only. Candidate certification requires separate candidate/deployed postcheck evidence and must say so.

## Task 2: Receipts And Financial Evidence

**Files:** External collection/annotation scripts using existing admin and read-only SQL interfaces. Do not change finance business logic unless a reproduced defect requires it.

- [ ] Capture model version, snapshot, token ID, station request ID, real attempt/channel, normalized usage and disjoint charge dimensions. Compare token debits, immutable ledger and settlement totals.
- [ ] Register every actual test attempt through `test_cost` facts with stable idempotency keys. Keep quote estimates distinct from supplier-confirmed debit and cash conversion.
- [ ] Read supplier receipts without paid probes. Match by request ID where supplied, otherwise require exact key/group, model, time and token/cost evidence. Do not claim exact matching from model/time alone.
- [ ] Re-read the finance report and assert all test attempts appear once; included test costs must not be deducted twice. Preserve unresolved supplier evidence and the budget hold.

## Task 3: Candidate Runtime, Guarded Release And Postchecks

**Files:** Versioned external preflight/release payload based on the current deployed source and exact candidate binary; existing source-integrity and rollback guards remain mandatory.

- [ ] Freeze the candidate and run fresh complete backend, compatibility and financial tests. Capture source hashes before and after, exact binary SHA-256, embedded asset identity and license/source attribution.
- [ ] Refresh actual server identity, image/binary, resource headroom and recent active requests. Use an isolated database/user and network without external egress for candidate startup/reader/rollback checks; no copied supplier credentials may be used for probes.
- [ ] Verify candidate startup does not change prices, channel authorization, balances or historic bills in the isolated copy. Start the compatible baseline reader on the same isolated state and verify rollback readability.
- [ ] Before cutover, take consistent gzip database backup, verify `gunzip -t` and SHA-256, keep original environment/source/image and a checked binary-only rollback. Do not modify the independent finance service.
- [ ] Deploy only after gates pass and no protected active request is present. Verify edge health/source identity, exact image/binary and frozen catalog. Perform budgeted private-listener postchecks and record their costs.
- [ ] Phase 2 completion requires actual client, provider, financial and release evidence. Missing external supplier evidence or unavailable client features remain explicit exceptions, never fabricated passes.

## Subsequent Work

Phase 3 begins only after the Phase 2 gate. Its frontend implementation and browser acceptance need a separate scoped plan based on the actually shipped paths.
