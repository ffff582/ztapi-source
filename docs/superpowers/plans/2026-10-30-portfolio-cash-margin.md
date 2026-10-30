# Portfolio Cash Margin Implementation Plan

> Use local TDD and verify each milestone. Existing design and execution are authorized by the user's continuation and latest thresholds; no additional design-choice loop is required.

**Goal:** Reconcile every active model and raise only deficient dimensions. OpenAI/Anthropic/Google keep 20% enterprise or 30% pool margins after a 5% customer bonus; other providers need at least 10%. Do not lower prices merely because the threshold changed.

**Architecture:** Extend the already deployed immutable cash-margin source/snapshot path with explicit model, base-policy and channel contracts. Rebuild canonical procurement tiers with frozen FX, compare eligible-channel costs and retain any higher existing retail dimension. Preserve the previous three models' exact prices and evidence. A new Fable uniform mode takes a validated equal TTL price from its frozen source, never the legacy fixed constant.

**Tech Stack:** Go/GORM/common JSON/decimal and rational rounding; existing SQLite/MySQL transaction tests, separate finance sidecar.

## Constraints

- No customer balance changes or historical repricing; no direct SQL catalog writes.
- Sources, snapshots, channels, provider identity and paid-test evidence must be versioned and read back.
- Xcloud selected-group quotes already include the group factor; apply owner-confirmed cash factor 0.5 exactly once.
- Cache input/read/write are mutually exclusive; preserve original tier conditions and fail closed on absent or conflicting evidence.
- Keep higher prices unchanged; inactive models are out of scope. No global route changes.
- Paid acceptance remains within previously approved 10U total / 1U per model; stop a model before exceeding its cap. Record successful, failed and direct verification costs separately with supplier identities, not client wallet credits.

## Work

- [x] Read-only fresh baseline: all active sources, snapshots, effective routes, publication modes, supplier group quotes and financial facts.
- [x] AB-basis worker: canonical CNY cost tiers for eight enterprise OpenAI/Google models and the Fable pool source; reject forged or unsupported bases.
- [x] Fable-mode worker: versioned dynamic uniform cache-write projection and legacy publisher guard, with red/green tests; no pricing-policy edits.
- [x] Main worker: extend scoped derivation, publication/restore/batch contracts and downgrade protection. Sonnet/Fable mixed-channel floors use separately evidenced Xcloud group prices, not a substituted Yunxin cost.
- [x] Prove original three prices are unchanged, old writers cannot overwrite new policies, bad channels/parents/tiers reject atomically, and restore creates new old-policy sources.
- [x] Full local regression and isolated production-shaped database acceptance with external network disabled before release. Final frozen suite exit0 with unchanged source; Windows executable-lock failure preserved separately and rerun in an independent temp directory.
- [x] Deploy compatible backend reader before exact CAS price activation; verify all active public prices and unchanged unrelated snapshots. Four prices activated, two stale enterprise admissions paused, 16 models remain active; 27 historical snapshots preserved. Finance/nginx container identities unchanged.
- [x] Bounded paid acceptance: 25 initial tests and nine controlled retests; 16 succeeded, nine failed twice and were paused. 34 immutable financial records added. Confirmed Xcloud cash 0.000214U; 14 successful Yunxin quote estimates 0.02551624U. No paid exploration after the user's quotation-only direction.
- [x] Final per-model report: current route/cost evidence, changed dimensions, retained prices, test fees and explicit remaining evidence limitations. Chinese Markdown plus 126-dimension and all-60-model CSV reports written to the delivery output folder.

## Review Focus

Dynamic Fable cache prices must not reintroduce TTL-split failures. A route allowlist is not proof that every route is usable. Provider branding, not a source's legacy margin-policy name, determines the new threshold. Embedded supplier prices remain quotation evidence, not actual invoice deductions. Binary downgrade must remain blocked while any expanded policy or ambiguous unsettled snapshot still needs a compatible reader.

## Confirmed Scope Update

- User requires quotation-authorized resource routes, not self-discovered resources. Gemini 3.1 Pro and Qwen must use enterprise channel 1 only. Preserve their current retail prices.
- Image2 remains on enterprise channel 1. Replace the incorrect legacy pool procurement basis with D100 enterprise costs, including frozen upstream USD and platform FX; guarantee 20% cash margin after the bonus across generation and edit buckets.
- The new shared media reader must accept the exact cash-margin upward rounding while old policies retain their own stricter validation.
- Finance r4 is deployed independently: validated management tests are excluded from customer income, units/quantity parsing is precise, immutable facts and read-only rollback are verified. Main API was not restarted for this finance release.
- Initial ten-model price/read/restore preflight passed. The final four-model activation (Fable, Sonnet, Gemini 3.5, Image2) and two quotation-only route changes require a new frozen-code full test and isolated preflight before main deployment.
- Final enterprise-only rebinding of Gemini Pro/Qwen was correctly blocked by stale discovery/verification. Do not bypass freshness or perform paid exploration. Safely pause these two models until enterprise admission is available; do not label the upstream unavailable based on missing evidence.
