# RMB Price Display And License Entry

Status: the user approved these two changes on 2026-11-07. Cash referral implementation is not part of this release.

## Goal

Default model price displays to CNY reference amounts, retaining a U switch, and replace the documentation's hard-coded frontend repository link with an open-source license entry.

## Constraints

- Preserve all billed U prices, procurement records, historical snapshots, balances, keys, model visibility and channel configuration.
- Read the display rate from the existing public status API (`usd_exchange_rate`, currently 7.3), not procurement FX (6.63).
- The authenticated catalog mirrors that same read-only setting as `display_cny_per_unit` in its existing response. No second authenticated fetch may consume, gate or delay the user's model catalog.
- Show the selected conversion rate and that actual settlement remains in U/USDT. A missing or invalid rate must fall back to original U prices; never invent a rate or hide the catalog.
- Convert input, output, cache, context tiers, request and media prices consistently across public models, authenticated model lists and homepage price examples.
- Preserve the native CNY official benchmark using structured reference metadata, not parsing human-readable notes. USD benchmarks convert using the chosen display rate. Comparisons use unrounded values in the selected display currency.
- Maintain AGPL source availability and original New API attribution. The license page must be publicly accessible, offer the corresponding source, preserve original author/project notices, and fail explicitly rather than linking to an unrelated stale production version.
- Do not issue paid model requests, enable commissions, or add financial mutations.

## Approaches

1. Change billing currency and rewrite balances: rejected; the user approved display conversion, not a financial migration.
2. Convert only visible headline text: rejected; it would leave caches/media and official comparisons inconsistent.
3. A shared display formatter with explicit status FX and native-reference metadata: selected. Backend additions are read-only public reference metadata.

## Verification

Test invalid FX, small values, all 16 live-model dimensions, native CNY references, unchanged U values, Fable's missing TTL comparison, source-offer failures, bilingual pages and responsive layouts. Require exact-commit financial CI, immutable source tag, isolated server checks and rollback-preserving deployment. Verify unchanged production business digests and all public prices after deployment.
