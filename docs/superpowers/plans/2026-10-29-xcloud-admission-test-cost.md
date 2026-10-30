# Xcloud Admission and Test Cost Implementation Plan

**Goal:** Restore verified Xcloud models and show attributable test costs without overstating or double-counting gross profit.

**Architecture:** Separate route admission from the Yunxin frozen quotation; bind exact supplier routes to immutable publication evidence. Extend the isolated finance service with explicit request-linked test facts and aggregate covered versus unconfirmed costs separately.

**Spec:** `docs/superpowers/specs/2026-10-29-xcloud-admission-test-cost.md`

## Tasks

1. Capture current production image, source, routes, publication snapshots and finance baseline; retain existing deployed usage fix.
2. Add failing Claude cache accounting tests, correct the normalization, and run billing regressions.
3. Add Xcloud route/price admission tests and implement exact identity, snapshot and final outgoing-model validation without relaxing Yunxin rules.
4. Add immutable finance test-fact and aggregation tests, implement authorized recording, cost coverage and monthly display/export; do not infer tests from account IDs.
5. Verify local suites, permissions, no duplicate cost and desktop/mobile UI. Back up and deploy using existing guarded workflows.
6. Re-test eligible models within the authorized paid scope; record requests and supplier cost evidence, publish only after result and settlement closure. Keep unverified models off sale.
