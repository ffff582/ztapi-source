# Support Experience Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development for disjoint bot/UI tasks and parent implementation of binding security, with TDD and one final whole-change review.

**Goal:** Ship a personable, useful support workflow with verified private diagnosis and real human follow-up, without monetary mutations.

**Architecture:** Existing website support sidecar owns identity/bindings/tickets. Telegram retains isolated storage and reaches a narrowly authenticated bridge. Existing finance reads unchanged immutable call receipts. Frontend extends existing support page.

**Tech Stack:** Go/GORM/SQLite/existing JWT, HMAC-SHA256, React/TypeScript/Vitest, existing Nginx and protected SSH release tooling.

**Spec:** docs/superpowers/specs/2026-11-02-support-experience.md

## Global Constraints

- No automatic refund/balance/price/channel/key creation; main API and finance service not restarted.
- Deployed baseline a8f24edad96e42328b1ce2963d94c455e42d2ac6; dirty unrelated work preserved.
- General configuration help requires no binding. Private identity is confirmed by website, never a claimed username.
- Additional paid acceptance0.5U, existing unknown holds retained; total15U and Opus3U unchanged. Support costs automatically appear in finance.
- Old receipts, backups and protected credentials are retained. No credentials in source, logs or prompts.

## Review Focus

- A stolen bind link cannot activate itself or expose account existence/history.
- Replaying bridge/nonces/Telegram updates cannot duplicate model calls or tickets.
- Private diagnostics/preferences and unbound claims cannot leak into group prompts.
- Unbind/disabled user prevents new account reads and ticket delivery.
- Serious-mode fallback cannot invent a refund, repair, operator handoff or zero cost.
- Support/bridge/notification outages cannot affect the model API or hide finance receipts.

### Task1: Contracts, Binding And Secure Bridge (Parent)

Files: support/bridgeapi/*; support/telegram_binding*.go; support/telegram_bridge*.go; support/http*.go; cmd/ztapi-support/main*.go.

- [x] Write failing binding TTL/replay/ownership and HMAC tests; run RED.
- [x] Implement five-minute challenge, claim/explicit-confirm/revoke, active-identity checks and fixed bridge actions.
- [x] Add persistent ticket event discovery/ack with generic private notifications; preserve existing ticket API.
- [x] Wire protected bridge secret configuration; run support and command suites/vet.

### Task2: Telegram Personality, Preferences And Private Workflow (Agent)

Files owned: support/telegram/* and cmd/ztapi-telegram-support/* only. Consume support/bridgeapi.Request/Result and Signature helper; Config.Bridge uses Execute(context.Context,bridgeapi.Request)(bridgeapi.Result,error).

- [x] RED tests for Xiao Z normal/serious policy, no secret echo, opt-in preference/forget/context isolation.
- [x] Implement private /start bind_TOKEN, binding status/owned recent request/diagnosis/handoff through Bridge only; no platform DB/JWT.
- [x] Add durable generic notifications with authorize-before-send/ack-after-send; owner-private unverified intake/reply/resolve with no group leakage.
- [x] Keep existing inbox/outbox/replay/financial/resource invariants; run package and CLI tests/vet.

### Task3: Binding UI And Setup Experience (Agent)

Files owned: web/console/src/api/support* and features/support/*; cmd/ztapi-support-fixture/*; tests/compatibility/support-experience-browser* only.

- [x] RED API/component tests for no-binding general help; pending link/claim, explicit confirm, revoke, expiry/replay/error states.
- [x] Implement compact BindingView controls in support page, actual external bot link and progress refresh; no auto-confirm or sensitive account display in group links.
- [x] Browser cases at1440/390/320 with local fixture; no real calls or credentials.
- [x] Record production UI file hashes and test evidence, stop owned fixture/browser processes.

### Task4: Integration, Security Review And Release (Parent)

- [ ] Pin deployed container/config/static baselines, private database backup and rolling sidecar-only rollback.
- [ ] Run all affected Go/Python/UI suites; fresh whole-change reviewer; fix important findings RED/GREEN.
- [ ] Freeze only authorized overlay on actual deployed baseline, build exact source, publish AGPL refs and verify payload hashes.
- [ ] Reserve five0.1U calls in existing global immutable journal. Real personality/config/serious/image tests and binding/ticket flows; preserve unknown holds and no automatic inference retries.
- [ ] Deploy tested UI/support/bot only; verify original main API/finance identities, private alert destination, group receiver, support finance continuity and health. Deliver operational record.
