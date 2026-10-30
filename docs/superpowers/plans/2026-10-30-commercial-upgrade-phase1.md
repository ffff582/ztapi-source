# ZTAPI Commercial Upgrade Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task. Steps use checkboxes for evidence-backed progress.

**Goal:** Repair shared conversion semantics and prove that failures cannot bypass billing or routing safety.

**Architecture:** Keep the current gateway and adapters. Repair pure conversion functions with explicit unsupported-input errors, then validate the HTTP/admission/settlement boundary using existing synthetic upstream fixtures. No pricing or catalog mutations.

**Tech Stack:** Go 1.25, existing DTO/common JSON wrappers, GORM isolated test databases, httptest, existing settlement and publication contracts.

**Spec:** `docs/superpowers/specs/2026-10-30-commercial-upgrade.md` (owner-approved direction and paid-test limits).

## Global Constraints

- Current working tree is the baseline; preserve all unrelated dirty source.
- No paid calls in Phase 1; no production writes, price/channel/publication or balance changes.
- TDD for semantic changes; optional forwarded scalar fields preserve explicit false and zero.
- Keep existing stateless tool support and reject unsupported requirements without silent degradation.
- Use common JSON wrappers; preserve all protected project attribution.

## Review Focus

- Strict false/true, required/forced tools and unsupported builtins cannot disappear silently.
- Mixed text/tool messages and results preserve content and call identity, including repeated names.
- Orphan, duplicate and ambiguous results fail safely rather than attach to another call.
- Cancellation, trailing failure and retries cannot create a second debit or report an incomplete reply as completed.
- Old prices, permitted resource channels, immutable evidence and unrelated clients retain their existing behavior.

## Task 1: Preserve the Baseline

**Files:** delivery `source-baseline.json`; this spec/plan; external progress ledger.
- [x] Verify source repository/branch and preserve existing dirty state.
- [x] Create a detached isolated worktree and mechanically copy all 2184 current source files; verify every copied hash.
- [x] Reuse existing ignored dependencies without regenerating lockfiles.
- [x] Run a fresh focused baseline and retain red diagnostics for the six known cases.

## Task 2: Responses Conversion Contract

**Files:** `server/service/openaicompat/responses_to_chat_request.go`, its tests; `server/dto/openai_request.go` only if strict representation requires it; `server/controller/ztapi_responses_via_chat_e2e_test.go`.
**Interface:** `ResponsesRequestToChatCompletionsRequest(*dto.OpenAIResponsesRequest) (*ResponsesChatConversion, error)` remains unchanged.
- [x] Add failing tests for unsupported builtin requirements and strict function preservation, including explicit false.
- [x] Observe meaningful red assertions before implementation.
- [x] Preserve supported function/custom/namespace behavior. Reject unsupported tools/constraints explicitly rather than discard them.
- [x] Update the test that previously expected builtin deletion and prove rejection precedes dispatch/retained debit.
- [x] Run `go test ./service/openaicompat ./controller -run 'Responses|Codex' -count=1 -p 1` and inspect output.

## Task 3: Anthropic and Gemini Tool Semantics

**Files:** `server/service/convert.go`; new `server/service/ztapi_conversion_semantics_test.go`; Gemini DTO only by explicit coordination.
**Interfaces:** existing `ClaudeToOpenAIRequest` and `GeminiToOpenAIRequest` signatures remain unchanged.
- [x] Add red tests for Anthropic forced/any/none/auto tool selection and parallel=false, and mixed text/tool messages.
- [x] Add red tests for Gemini separated tool results, parallel/repeated names, missing/orphan results, and stop-sequence lengths 0,1,3,4,5.
- [x] Implement bounded, deterministic mapping; reject unrepresentable or ambiguous identities instead of inventing associations.
- [x] Preserve explicit false/zero and existing cache/usage behavior.
- [x] Run `go test ./service -run 'ConversionSemantics|ClaudeCache|ClaudeClientCache|Gemini' -count=1`.

## Task 4: Financial and Admission Boundary

**Files:** new `server/controller/ztapi_conversion_admission_e2e_test.go`; only scoped implementation fixes if a new red test proves one.
**Consumes:** converter errors, existing httptest upstream fixture and immutable settlement/ledger API.
- [x] Prove rejected inputs dispatch zero provider calls and retain zero debit in the isolated database.
- [x] Recheck trailing stream failure with confirmed usage: exactly one settlement and one log.
- [x] Recheck cancellation/retry price snapshots and permitted channel authority; no enterprise-resource expansion.
- [x] Run controller, service and model settlement/retry regression selections; record exact commands/results.

## Task 5: Review and Phase Gate

**Files:** delivery source manifest, red/green logs and phase receipt; plan/ledger.
- [x] Rerun six diagnosis scenarios as positive regression tests or explicit pre-dispatch rejections.
- [x] Run `go test -short ./... -p 1 -timeout 15m` from an independent Windows temp directory.
- [x] Run `git diff --check`; compare unrelated baseline files against the preserved source manifest.
- [x] Obtain independent scope/security/financial review and address important findings with red/green tests.
- [x] Record Phase 1 complete and report its summary before starting Phase 2 implementation.

## Execution Rulings

The owner explicitly requested execution of the accepted three-stage plan; no second generic design-approval loop is required. The desktop worktree tool rejected the external sourceRoot, so a standard Git worktree was created and the exact current dirty state copied in. No original source was changed by setup. Existing unrelated work is not committed or reverted as part of this plan.

Independent reviews added required cases before the final gate: unknown custom-format rejection, verbatim grammar preservation, valid Chat tool-round ordering, missing Anthropic tool results, Gemini HTTP admission, and deterministic downstream write failure/cancellation with exact wallet, token, ledger and replay assertions. The initial whole-backend run failed because ignored embedded frontend builds were absent; the original generated build directories were copied mechanically and the final frozen run must succeed. No live client or provider acceptance is inferred from Phase 1.

Final gate passed on 2026-10-30 at 10:15:22 UTC: `go test -short ./... -count=1 -p 1 -timeout 15m`, command exit 0, unchanged identity for all 1069 Go/mod/sum files (`7bc67c0dca9f0eb1fad9d66aadc5493bc77cc8f0839de8c87fbd6d341a79a74b`). Exact machine receipt is in the external delivery directory as `phase1-full-latest.json`. Phase 1 paid calls and production application mutations are zero.
