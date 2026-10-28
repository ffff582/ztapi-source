# Xcloud Primary Routing and Finance Estimate Plan

> **Execution contract:** follow the test-first order below; do not deploy
> route changes until the listed checks pass.

## 1. Baseline and fixtures

- Record current git status and current production channel/model evidence.
- Keep Xcloud public pricing evidence in the task output without storing any
  credential.

## 2. Test-first finance estimator

- Add a failing test for token and cache dimension pricing.
- Implement quote records for the six configured Claude models with public
  Xcloud quote provenance.
- Parse settlement charge dimensions and calculate per-channel estimates.
- Add tests for unknown quote, missing usage, and cache exclusivity.

## 3. Finance report and UI

- Extend the read-only finance snapshot with immutable settlement usage and
  public/source model identity.
- Add Xcloud estimate rows and aggregate estimate fields to the report.
- Render a separate estimate panel in the finance overview with explicit
  non-confirmed wording.

## 4. Test-first routing

- Add failing routing tests for Xcloud-primary/Yunxin-backup behavior.
- Use the existing channel ability priority and publication snapshot APIs.
- Preserve immutable publication history by creating new snapshots.

## 5. Verification

- Run focused Go tests, the finance package tests, middleware tests, and the
  frontend build/lint checks.
- Start or use the local finance service and verify report JSON.

## 6. Production rollout

- Back up affected production rows.
- Apply only the admitted model abilities, channel status/priority, and new
  publication snapshots.
- Refresh the existing catalog/channel caches through the supported path.
- Verify Xcloud primary and Yunxin fallback with live read-only checks; do not
  claim supplier cost confirmation from the public quote.
