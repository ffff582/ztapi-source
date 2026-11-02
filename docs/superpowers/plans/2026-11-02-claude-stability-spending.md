# Claude Stability And Spending Alerts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Follow TDD and preserve disjoint ownership.

**Goal:** Make continuous Claude client failures detectable/recoverable without unsafe replays, and provide opt-in, owner-only spending alerts without altering money or routes.

**Architecture:** Narrow relay fixes and fixture regressions; independent SELECT-only spending sidecar with private SQLite state; shared console alert UI. Reuse existing JWT, ledger evidence and verified Telegram bindings, never a new model/provider integration.

**Tech Stack:** Go/Gin and net/http, GORM with existing SQLite/MySQL/PostgreSQL-compatible queries, glebarez SQLite, React/TypeScript/Vitest/Playwright, existing Docker/Nginx release machinery.

**Spec:** docs/superpowers/specs/2026-11-02-claude-stability-spending.md

## Global Constraints

- No new paid model calls without a numeric budget; fixtures and live read-only checks are separate evidence.
- No key rotation, balance/refund edits, price changes or channel changes; no primary-database migration for alerts.
- Start from deployed9d3a0a0; do not include unrelated dirty worktree files.
- New alerts default OFF; no hard cap or suspension; pending/unknown charges are never zero confirmed usage.
- Protected identity/source attribution, common JSON wrappers, parametrized SQL and all3 database dialects remain intact.

## Review Focus

- A stream returning200 and visible text can still be truncated; never convert it to a successful terminal event or retry after tool execution.
- Billing facts can be final, reserved, released, late or independently charged; no duplication or inferred refunds.
- Account switch, disabled owner, unbind or stale browser response cannot expose the previous owner's data.
- Clock boundaries, exact decimals, duplicate workers and unknown delivery cannot create incorrect or repeated notifications.
- A sidecar/store/notification failure must leave the main model API and established support/finance/bot unchanged.

### Task1: Reproduce And Fix Continuous Claude Defects

**Files:** Existing server/relay/channel/claude, server/relay/helper and conversion handlers only where a reproduction requires it; new targeted controller/protocol regression tests and tests/compatibility fixture coverage. No broad refactor.

- [ ] Find a concrete transport/protocol defect with a failing real-function/httptest regression.
- [ ] Add multi-turn text, parallel tool/results, large history, partial output, clean terminal, error event, EOF and cancellation assertions.
- [ ] Implement the smallest correction preserving original billing evidence and non-replay boundary.
- [ ] Run affected relay/controller/service tests, existing Codex/Claude fixture contracts and vet. Document actual client/fixture/version coverage and residual limits.

### Task2: Read-Only Charge Evidence And Private Alert Engine

**Files:** server/spending/{types,source,store,engine,telegram}.go and tests; server/cmd/ztapi-spending runtime. Use the spec's DTO and financial semantics.

- [ ] RED tests for authoritative net charges, pending/unknown evidence, exact U conversion, hosted tools, legacy overlap and Beijing windows.
- [ ] Implement bounded SELECT-only source and separate preference/event/outbox state; test no primary writes.
- [ ] RED then GREEN rule evaluation, disabled defaults, optimistic CAS, dedup/cooldown, restart and stale binding checks.
- [ ] Add strict owner JWT HTTP endpoints with body/origin/rate bounds,403/404 isolation and fixed safe errors.
- [ ] Add resource-bounded runtime and Telegram template sender with no polling/model access and ambiguous-send safety.

### Task3: Customer Controls And Inbox

**Files:** web/console/src/api/spending.ts and tests; features/spending components/styles/tests; app/router.tsx and tests; components/layout/{ConsoleHeader,ConsoleShell} and tests; narrow en/zh translations.

- [ ] RED tests for settings validation, explicit save, OFF defaults, summary unknown/pending and acknowledgement.
- [ ] Implement /console/spending and shared icon bell polling no more than60seconds, owner-aware stale-result guards and independent failure state.
- [ ] Test account changes, logout, unavailable service, mobile layout, keyboard/focus and existing navigation.
- [ ] Run complete console unit/types/build regression and browser fixtures at320/390/1440px.

### Task4: Independent Review, Artifact Freeze And Deployment

**Files:** Dedicated release helper and failure-injection tests under tools/release; execution evidence outside the source tree.

- [ ] Independently review charge provenance, privacy, rule/delivery dedup and stream retry/billing boundaries; resolve findings with regressions.
- [ ] Freeze only authorized changes on9d3a0a0, publish exact source, build Linux main/spending binaries and immutable UI artifacts, verify hashes.
- [ ] Preflight live identity, free capacity, readonly grants, current money/keys/routes digests, runtime support and rollback material without emitting secrets.
- [ ] Install isolated OFF-by-default sidecar, verify auth/health, atomically add narrow Nginx route; never send a real customer/test message automatically.
- [ ] Roll backend with non-forcing drain and statics last. Stop on drift or failed acceptance, preserving serving instances and transactions.
- [ ] Independently verify live commit/binary/assets, unchanged protected configuration/services, browser behavior, zero paid-call test cost, and collect completion receipts.

### Execution Status

- [x] User accepted the recommended two priorities and safe deployment boundary.
- [x] Exact deployed source and protected/service baseline reverified read-only.
- [x] Fresh source snapshot isolated from the dirty commercial worktree; existing dependencies linked without upgrades.
- [ ] Task1
- [ ] Task2
- [ ] Task3
- [ ] Task4
