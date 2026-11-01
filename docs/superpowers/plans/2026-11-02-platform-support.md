# Platform Support Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development for independent slices, with TDD and a final whole-change security review.

**Goal:** Ship platform support, own-request diagnosis, human handoff and automatic support finance without changing the model API runtime or customer money.

**Architecture:** Independent Go service, owner-scoped SELECT-only source, private SQLite call receipts/tickets and static frontend routes. Finance reads support receipts read-only.

**Tech Stack:** Existing Go/common JSON/GORM/JWT, React/TypeScript, existing private finance sidecar and Nginx.

**Spec:** `docs/superpowers/specs/2026-11-02-platform-support.md`.

## Global Constraints

- Main API, balances, refund records, channels and prices are not modified.
- No daily caps; short-window anti-abuse controls remain.
- Paid acceptance <=0.5U, no automatic retries, all support costs enter finance.
- No official search credential is used for model inference.
- All cross-user reads fail closed; no raw keys, request bodies or purchase prices in customer AI context.
- Preserve dirty worktree changes and use the real R23 backend / R3 frontend baselines.

## Review Focus

- Caller cannot choose another user, cost purpose, backend model, or upstream URL.
- Dispatch crash and missing receipts cannot turn cost into zero or trigger replay.
- AI cannot call financial/admin mutation APIs or invent business commitments.
- Human handoff has a usable admin inbox and customer-visible replies.
- Support database/read failures cannot take down finance or model serving.

### Task 1: Shared Contracts And Safe Source

- [x] Define support DTOs and financial receipt projection.
- [x] TDD SELECT-only owner diagnostics, real identity and curated/live knowledge.
- [x] Verify disabled/deleted identities and cross-user not-found parity.

### Task 2: Support Runtime

- [x] TDD private conversations/tickets, immutable call receipts, idempotency and secret redaction.
- [x] TDD fixed-upstream Flash adapter, strict vision, usage accounting and failure evidence.
- [x] TDD same-origin JWT HTTP endpoints, bounded concurrency and human replies.
- [x] Add standalone entrypoint with protected configuration and SELECT-only DB.

### Task 3: User And Staff UI

- [x] User support route, navigation, persisted chat, optional screenshots, own requests, diagnostic evidence and ticket submission.
- [x] Admin ticket inbox, full conversation, reply and explicit status management.
- [ ] Tests, responsive browser acceptance and no global layout regression.

### Task 4: Automatic Finance

- [x] Read immutable support receipts without writing customer settlements.
- [x] Show support token/cost rows, pending evidence and estimates separately.
- [x] Deduct cost once; unavailable support source makes profit incomplete, not all finance 503.
- [x] Validate old finance payloads and rollback read-only behavior.

### Task 5: Acceptance And Publication

- [ ] Fresh security/code review, all affected suites and browser QA.
- [ ] <=0.5U real text/vision tests with recorded receipts and budget controls.
- [ ] Publish source/artifacts, backup private stores and stage candidate services.
- [ ] Gracefully activate routes/static assets without main API restart.
- [ ] Verify main API identities and financial/route invariants, then deliver rollback and operational docs.

### Task 6: Telegram Extension Authorized During Implementation

- [ ] Reuse the verified existing bot and private alert recipient; do not change or restart the main API.
- [ ] Owner-enabled group whitelist, durable polling/inbox/outbox and cross-sender isolation.
- [ ] Explicit mentions/replies always acknowledged, safe refusals, images and low-frequency small talk.
- [x] Anonymous public knowledge and combined read-only site/Telegram finance readers.
- [ ] Private runtime configuration, store lease/resource limits and disabled candidate staging.
- [ ] Offline fault/permission/replay tests, actual group enable receipt and private owner verification.
- [ ] Public website group links and fresh responsive UI acceptance before publication.
