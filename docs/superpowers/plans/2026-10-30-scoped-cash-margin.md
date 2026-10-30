# Scoped Cash Margin Implementation Plan

> Execute inline with local TDD. No production connections, writes, paid probes or deployment.

**Goal:** Versioned derived pricing for exactly Gemini 3 Flash, Gemini 3.1 Pro and Kimi K2.7 Code, without altering old evidence or policy semantics.

**Architecture:** New sources pin `CashMarginBaseSourceID` to an independently verified frozen supplier source. Rebuild each canonical procurement tier with frozen FX and round cash-margin sales upward. Explicit route contracts, not resource labels, select Flash channel 1 / 20%, Pro channel 2 / 30%, Kimi channel 2 / 30%. Preview, apply and rollback are model/version/snapshot-scoped.

**Constraints:** Preserve resource/cost/currency/FX and immutable history. Keep shared dirty changes, finance and relay untouched. Do not use exported question-mark conditions; derive original Unicode conditions from the embedded canonical manifest. An enterprise procurement source may support the channel-2 pool-margin policy, but never claim verified actual supplier cash cost.

## Milestones

- [x] TDD: deterministic tier derivation and strict base/derived-source validation, including missing tiers, forged costs/FX/identity and non-allowlisted models.
- [x] TDD: wire new policies into preview, publication trust, quotation trust and snapshot hooks without weakening old policies.
- [x] TDD: scoped preview/apply/rollback with atomic source/snapshot/audit writes, optimistic version guards, route/verification preservation, idempotent exact retries and batch rollback on failure.
- [x] TDD: controller/router contracts, explicit confirmations and read/write permissions; document original-price restore before old-binary rollback.
- [x] Run focused tests, model/controller/router regression and wider local suite where practical. Focused 13 top-level tests and the three full packages passed; full-repository short run hit the model-package 300s timeout while an existing media test was running. Full-repository green is not claimed. See the delivery manifest.

## Rollback Contract

Apply audits record the base source and previous/current snapshots. Rollback requires a selected model's current version/snapshot and the original snapshot ID, validates that snapshot belongs to the pinned base source and exact route, then creates a copied old-policy source and a new old-policy snapshot at the next config version. This also keeps old-binary latest-source admin gates compatible. No historical rows are rewritten. Restore all selected models with the new binary before starting an old binary; the executable downgrade guard must report no active cash policy and no new-policy or ambiguous unsettled requests after admissions are quiesced and in-flight requests drained. Keep schema additions and compatible readers until those conditions hold.

## Main Files

New `server/model/ztapi_cash_margin_policy_20261030.go`, scoped repricing/tests; new controller/tests; narrow edits to existing policy/evidence/publication/quotation/router files. Tests use isolated SQLite only and never invoke verification services.
