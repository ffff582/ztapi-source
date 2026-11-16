# ZTAPI Compatibility Qualification

This matrix is versioned evidence, not a promise to support every AI product.
Status as of 2026-10-30; a later upstream or client change requires requalification.

## Current Evidence

| Client / Surface | Version | Candidate With Scripted Provider | Live Provider | Released Candidate |
| --- | --- | --- | --- | --- |
| OpenAI JavaScript SDK: Chat text and SSE | 7.45.1 | Passed | Nonstream Chat passed on all 14 published text routes; live Chat SSE not qualified | Not released |
| OpenAI JavaScript SDK: Responses text and SSE | 7.45.1 | Passed | Fable 5, Opus 5 and Sonnet 5 SSE passed; GPT 6.1 Sol vendor metadata defect reproduced and fixed locally | Not released |
| OpenAI JavaScript SDK: Chat and Responses function round trips | 7.45.1 | Passed; actual local function executed | Responses forced function and full-history streamed result passed on the three Claude routes | Not released |
| Anthropic JavaScript SDK: Messages text, SSE and tool round trip | 1.3.0 | Passed; actual local function executed | Not yet qualified | Not released |
| Claude Code: file read, file write and final result | 2.1.312 | Passed; physical artifact hash and three settled requests | Not yet qualified | Not released |
| Codex CLI: patch, shell command and final result | 0.165.0-alpha.1.2 | Passed in owner-approved offline Docker boundary; physical patch, shell exit0, final result and three settled requests | Not yet qualified | Not released |
| Cursor, Open WebUI, Dify, Cherry Studio, other closed products | Not pinned | Not run | Not run | Not certified |

The SDK result is 12/12 cases with three actual local tool executions. It uses
the candidate's real HTTP auth, admission, conversion and settlement path, but
the provider is scripted and has no paid upstream connection. The Claude CLI
result similarly uses a scripted provider. Client-reported list-price estimates
in those logs are synthetic estimates, not actual cash expenditure.

The deployed-baseline paid runs contain 26 actual upstream attempts: 14 Chat
checks, nine successful Claude Responses cases, and three GPT stream failures
including two diagnostics. HTTP200 alone was not a compatibility pass. Every
attempt has one matched station settlement and a separately recorded test-cost
fact. Eight Xcloud attempts have supplier-confirmed bills and token matches;
Yunxin same-day costs remain quotation estimates pending next-day statements.
These are baseline provider facts, not certification of the unreleased binary.

The GPT native Responses fix filters only the observed `response.metadata`
extension, captures raw numeric usage evidence, preserves explicit zero and
leaves missing/conflicting usage unresolved. Synthetic Go tests preserve all
other event payloads, IDs and sequence gaps. Offline replay of the captured
synthetic wire passes the pinned SDK after that exact filter with unchanged
terminal usage; a live released-candidate postcheck is still required.

Initial nested-sandbox attempts failed: bubblewrap cannot create a user namespace
under the unchanged host/container policy, and legacy Landlock still requires
bubblewrap. These failed attempts are retained and are not counted as passes.
The owner then explicitly approved Docker as the sole execution boundary for
the offline synthetic qualification. R6 passed using the actual pinned Codex:
new file artifact and SHA-256, completed shell command with exit0, final result,
terminal turn, and three independently checked simulated settlements. Docker
remained non-root, read-only, network-none, cap-drop-all and no-new-privileges,
with no host directory/socket mounts or live credentials. The temporary container
and private environment were removed; production identity was unchanged. This
does not certify the nested client sandbox or any live-provider Codex route.
Evidence was read inside the live container because the Docker archive API
cannot see this host's tmpfs files; fixed paths, regular/non-symlink checks,
64KiB bounds, artifact identity and settlement invariants remain required.

The candidate runtime and compatible baseline reader both passed startup and
pricing-catalog reads on the same isolated MySQL copy. All 16 published models
matched and protected table snapshots were unchanged across startup/shutdown.
Temporary database, user, container and private environment were cleaned up;
production identity remained unchanged. This is reader-only rollback evidence,
not a migration rehearsal, live client qualification or production deployment.

## Protocol Boundaries

- Responses-to-Chat supports complete-history replay, function tools, supported
  custom tool wrappers, and terminal streaming events. It does not implement
  `previous_response_id`, provider-hosted conversation state, hosted web/file
  tools, or native grammar enforcement.
- Tool declaration forwarding is not proof that an upstream honors strict
  structured output. Qualify that semantic requirement on the actual route.
- Anthropic-to-Chat retains supported forced tool choices, mixed content and
  matching tool results. Result media and client-specific beta features require
  separate evidence; a basic text pass does not grant those capabilities.
- Gemini-to-Chat rejects ambiguous orphaned/parallel same-name tool histories
  rather than guessing. Stop sequences support zero through four entries.
- Actual terminal frames, physical tool artifacts, matching tool identities,
  settlement and supplier billing are independent acceptance checks.
- A deterministic failed downstream writer after confirmed usage proves that
  tested timing only. It is not evidence for every possible physical disconnect.

## Reproducible Local Checks

```powershell
node --test tests/compatibility/*.test.mjs
go test ./service/openaicompat ./service ./controller -run 'Responses|Codex|Downstream|ChatToolHistory|ConversionSemantics|Gemini' -count=1 -p 1
```

Actual SDKs are pinned in a separate external runtime; root dependency files
are not changed. Actual CLI runners use fresh homes, a synthetic project and a
verified nonce/PID loopback bridge. See `cli-smoke.md` and the Phase 2 plan.
Do not pass an SSH tunnel or a live key to a simulated runner.

## Paid Acceptance And Accounting

Only the existing server-only acceptance listener reached through strict-host-key
SSH is permitted. Use temporary model-scoped finite-quota keys, explicit output
bounds and no SDK automatic retry. Pre-reserve both possible attempts for any
existing managed two-channel fallback; one client request is not one upstream
request. `live-fetch.mjs` requires a durable reservation
before dispatch; `budget.mjs` enforces the approved 10U total and cumulative 1U
per-model limits using decimal strings. An unknown supplier cash charge retains
the full reservation. Stage-specific attempt IDs and exclusive run journals
prevent restarting the same paid run under a fresh budget.

Supplier billed credit and cash are different quantities. Xcloud's confirmed
recharge factor is 0.5, applied once after the supplier's group-billed amount.
Keep Yunxin quote-based estimates distinct from next-day supplier statements.
Each actual attempt must be reconciled to its immutable main settlement and
registered separately through the existing finance `test_cost` interface.
Missing usage or supplier evidence is not zero expenditure or confirmed profit.

## Release Gate

Fresh full backend regression, source freeze, scope preservation, isolated
MySQL/runtime/compatible rollback checks, exact image/binary identity, verified
gzip/SHA-256 backups and private-listener postchecks are required before marking
the candidate released. Do not reprice, change channel eligibility or alter
historic customer balances to make compatibility tests pass.
