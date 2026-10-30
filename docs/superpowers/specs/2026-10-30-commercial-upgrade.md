# ZTAPI Commercial Upgrade Design

## Approved Direction

The owner approved the previously discussed three phases, in order: (1) protocol correctness and financial safety, (2) actual client acceptance and operational reliability, (3) workbench and customer operations experience. Preserve the existing Go gateway and two frontend build targets. Do not rewrite the platform or advertise universal compatibility.

The owner separately approved paid acceptance capped at 10U total and 1U per model, only on quotation-authorized resources, with test expenses recorded separately.

## Approaches

- Per-client patches alone: fastest initial change, but do not address silent semantic loss, repeatable release acceptance, or customer support and billing trust.
- Incremental full-journey upgrade (selected): repair shared semantics, establish reproducible client/SDK acceptance, then upgrade the existing console and workbench. Reuse immutable accounting, finance, routing authorization and release mechanisms.
- Full rewrite: rejected because it adds migration and financial-history risk without evidence that the existing architecture is the limitation.

## Constraints

- Work from the current delivery source including all preexisting uncommitted work; never reset or overwrite it.
- Quotation evidence determines eligible resources. No exploratory provider/model discovery may authorize a resource or discount.
- Do not change prices, existing model publication state, customer balances, recharge rules or historical bills as part of this upgrade.
- Unsupported semantic requirements must fail before provider dispatch, without retained customer debit; never silently delete a requested capability and report success.
- Actual confirmed upstream usage must still settle exactly once after downstream failure. Missing/conflicting usage remains unresolved, not free or guessed.
- Never expose upstream keys or customer prompts in logs, screenshots, reports, process arguments or diagnostics.
- Paid tests: total <=10U and per model <=1U. Track both platform debits and evidenced supplier cash; stop before an uncertain cost could exceed either limit. Temporary model-scoped keys and synthetic projects only.
- Distinguish local mocks, actual clients with simulated providers, actual clients with quoted live providers, and production postchecks. None substitutes for another.
- Test SDK/client versions explicitly. Closed product functionality is not an API-gateway promise (for example Cursor Tab).
- Keep text/image/video workflows distinct. Reuse the current design and components, not a new marketing redesign.
- Preserve protected project attribution, deployment credentials and recovery files.
- No database restoration to roll back deployed finance facts. A compatible reader must remain available for active price snapshots.

## Phase 1: Correctness and Financial Safety

Repair and reproduce six observed conversion gaps: Responses builtin tools/strict requirements, Anthropic forced tools and mixed text/tools, Gemini tool result association and short stop sequences. Cover explicit false/zero, duplicate/orphan tool identities, parallel and repeated-name tools, supported stateless histories and unsupported server-side state. Verify no provider dispatch or retained debit for unsupported inputs; confirmed usage and retries remain idempotent and authority-bound. All source changes require red/green tests, scoped tests and whole-backend regressions.

## Phase 2: Actual Client Acceptance and Operations

Create a versioned acceptance matrix for official SDKs and representative coding, chat and workflow clients. First priorities are SDKs, Codex and Claude Code; Cursor is constrained by its BYOK product rules. Run safe simulated scenarios before quoted-provider tests. Include nonstreaming/streaming, tool round trips, cancellation, retries, authentication/limits, long-history continuity, and available multimodal workflows. Missing tools, client access or insufficient budget are explicit incomplete acceptance, not pass. Record receipts, price snapshots, actual usage, ledger entries and supplier/test-cost evidence. Exercise isolated release/rollback and read-only health/report checks before production publication.

## Phase 3: Workbench and Customer Operations

Improve model-to-client setup guidance, capability/error visibility, Key lifecycle messaging, accurate period statistics and safe diagnostic exports. Verify account-scoped chat history, per-modality draft separation, quote-backed image parameters and reference uploads, and resumable video job states. Only implement gaps proven in the actual shipped frontend path; source in another frontend is not automatically an online defect. Browser acceptance covers desktop and mobile, refresh, failure, loading and empty states. Internal diagnostics remain in logs/details rather than normal chat messages.

## Commercial Delivery

Deliver the versioned matrix (including explicit unsupported/unverified functions), automated tests, runtime/client receipts, frontend screenshots, cost evidence, deployed source/binary hashes where released, backup/rollback verification, operator documentation and a residual-risk list. Each phase has a recorded completion gate; do not mark all complete because only mocks passed.
