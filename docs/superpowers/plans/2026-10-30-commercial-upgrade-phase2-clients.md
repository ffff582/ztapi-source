# ZTAPI Phase 2 Client Qualification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task by task. Execution starts only after the recorded Phase 1 gate.

**Goal:** Run actual installed clients and official SDKs against the candidate gateway, then qualify authorized live routes and reconcile every paid attempt without calling mocked success a commercial release.

**Architecture:** Reuse the real Gin/Relay/settlement fixture for a loopback-only candidate test listener. Keep SDK/client process orchestration outside production code. First qualify clients with a scripted, local upstream; live acceptance and release are a separate gated work package using the existing private acceptance listener and real accounting evidence.

**Tech Stack:** Go 1.25.1; Node.js; OpenAI JS SDK 7.45.1 and Anthropic JS SDK 1.3.0 (versions read from the canonical npm registry); installed Codex CLI 0.165.0-alpha.1.2 and Claude Code 2.1.312.

**Spec:** `docs/superpowers/specs/2026-10-30-commercial-upgrade.md`.

## Global Constraints

- Complete Phase 1 first. Do not mutate its frozen Go source during its final full regression.
- Owner-approved paid ceilings: 10U total and 1U per model. This package's simulated runs spend 0; live runs require a separate cost reservation and receipt.
- Only quotation-authorized, currently admitted routes. No model discovery probe may authorize a resource, price, or publication.
- Do not modify user client profiles, installed client binaries, customer balances, price snapshots, channel settings, or historical bills.
- Temporary homes, synthetic projects, minimal child environments and loopback listeners only. Disable external MCP, plugins, hooks, search and nonessential telemetry in test profiles.
- Never log credentials, real customer prompts, or supplier keys. A local simulated key is still written only to a restricted temporary readiness file, not stdout.
- SDK automatic inference retries are disabled in paid qualification. A request with unknown cost retains its budget reservation until resolved.
- Distinguish scripted-provider actual-client qualification, live-provider qualification, and deployed edge postchecks in every receipt.

## Review Focus

- A client can parse text while failing to execute tools or receive terminal SSE; require actual tool output and local artifacts.
- Client startup, retries or background requests can multiply calls; record every dispatch and hard-stop unexpected inference paths.
- A fresh client home must not import the operator's real projects, hooks, MCP credentials or model-provider configuration.
- Test credit deductions are not supplier cash costs; Xcloud's 100-for-200 recharge factor is 0.5, applied once after the billed group multiplier.
- Unsupported server-side state, hosted tools, tool-result media or closed-product features must not be advertised as verified.

## Task 1: Real HTTP Candidate Bridge

**Files:** Create `server/controller/ztapi_client_acceptance_bridge_test.go`.
**Interfaces:** An opt-in `TestZTAPIClientAcceptanceBridge` consumes a restricted JSON configuration path from `ZTAPI_CLIENT_BRIDGE_CONFIG`; emits `{base_url, api_key, model, pid}` only to its configured readiness file; writes redacted dispatch and final settlement receipts to its output directory.

- [ ] Add ordinary offline tests for configuration/path/model validation and fail-closed endpoint limits; observe RED before implementation.
- [ ] Start the existing real Relay fixture behind `httptest.NewServer` on loopback only, for one selected fixture model per run.
- [ ] Add a deterministic scripted local upstream that chooses only tools declared by the actual request and validates returned IDs/results; never execute model-selected tools inside the gateway.
- [ ] Bound request bytes, inference dispatch count and total run duration. Use a stop-file condition or canceled context for shutdown, with an explicit timeout.
- [ ] Prove the bridge rejects non-loopback/live destinations in simulated mode, preserves real billing assertions, and shuts down cleanly.

## Task 2: Pinned SDK and Installed CLI Runners

**Files:** Create `tests/compatibility/sdk-smoke.mjs`, `tests/compatibility/cli-smoke.mjs`, matching `.test.mjs` files, and `tests/compatibility/README.md`. Dependency installation and generated evidence live in the external delivery directory, not the root lockfile.
**Interfaces:** Runners accept a readiness file, output directory and explicit case/client profile; emit a versioned JSON receipt with endpoint, terminal events, tool IDs, artifact hashes, dispatch counts and `provider_kind: simulated|live`.

- [ ] TDD validation of missing paths, endpoint escape, secret redaction, unexpected client binaries and child-environment isolation.
- [ ] Run actual official SDK requests: nonstreaming, streaming terminal completion, forced function call, matching result and final answer; validate supported complete-history replay and explicit unsupported-state errors.
- [ ] Run Codex with an isolated `CODEX_HOME`, synthetic project, no inherited rules/configuration or hosted search; verify a real local file/tool round trip and terminal success.
- [ ] Run Claude Code with an isolated `CLAUDE_CONFIG_DIR`, bare profile and a minimal tool list; verify actual file read/write and a final response.
- [ ] Capture failure paths with scripted authentication, rate-limit, provider failure and interruption responses; no real paid request is used for negative cases.
- [ ] Keep environment/client restrictions separate from relay failures. Cursor/Dify/OpenWebUI UI support remains unverified unless the actual product is run; request-shaped fixtures alone are not product certification.

## Task 3: Matrix, Financial Guard and Live/Release Gate

**Files:** `tests/compatibility/README.md`; external phase receipts and immutable source manifest. The live-budget module and operational release plan are specified after inspecting current routes and the existing receipt/import workflow, before executing any paid request.

- [ ] Record actual SDK and CLI evidence, plus unsupported/unverified features, in a versioned support matrix.
- [ ] Inspect current admission, quotations, route IDs, private acceptance listener, settlement/test-cost interfaces and release/source-integrity controls without altering them.
- [ ] Write and test the separate live/operations work package: monetary ceilings, every-attempt accounting, supplier statement matching, immutable finance annotation, isolated MySQL/runtime/rollback verification and guarded release.
- [ ] Never reuse an old paid test as fresh evidence, fabricate a settlement/verification to satisfy finance foreign keys, or bypass a source/release guard.
- [ ] Phase 2 cannot be marked complete based on Tasks 1-2 alone. Actual provider receipts, test expenses and release/postcheck evidence remain required before Phase 3 implementation.

## Official References

- OpenAI gateway qualification: https://learn.chatgpt.com/docs/enterprise/gateway-compatibility
- Custom providers: https://learn.chatgpt.com/docs/config-file/config-advanced#custom-model-providers
- Official SDKs: https://developers.openai.com/api/docs/libraries
- Claude Code gateway protocol: https://code.claude.com/docs/en/llm-gateway-protocol
- Anthropic TypeScript SDK: https://platform.claude.com/docs/en/cli-sdks-libraries/sdks/typescript

Stateful continuation, native grammar enforcement, physical mid-scan disconnects, tool-result images and full application-specific features are separate capabilities. Do not infer them from the Phase 1 fixes or basic SDK replies.
