# Offline Claude Continuous-Work Regression

Baseline: deployed source snapshot `9d3a0a0`. These tests use synthetic requests,
in-memory test databases, and loopback-only upstreams. They do not qualify a paid
provider or a newly executed Claude Code/Anthropic SDK session.

## Commands

Run from `server` in PowerShell, with offline dependencies and bounded parallelism:

```powershell
$env:GOPROXY='off'
$env:GOSUMDB='off'
$env:GOMAXPROCS='2'
$env:GOTOOLCHAIN='local'
go test -mod=readonly -p=2 ./relay/channel/claude -run '^TestClaudeStabilityHTTP200MessageDeltaWithoutStop$' -count=1 -timeout=60s
go test -work -mod=readonly -p=2 ./relay/channel/claude -run '^TestClaudeStability(OpenAITerminalCommit|AdaptorErrorRetainsMetering)$' -count=1 -timeout=90s
go test -work -mod=readonly -p=2 ./relay/channel/claude -run '^TestClaudeStabilitySharedFinalPreservesSDKCaller$' -count=1 -timeout=90s
go test -work -mod=readonly -p=2 ./controller -run '^TestClaudeStabilityAdaptorFailurePersistsPendingMetering$' -count=1 -timeout=90s
go test -work -mod=readonly -p=2 ./relay/channel/aws ./relay/channel/vertex -run '^$' -count=1 -timeout=90s
go test -mod=readonly -p=2 ./relay/helper -run '^TestClaudeStability' -count=1 -timeout=60s
go test -mod=readonly -p=2 ./controller -run '^TestClaudeStabilityContinuousRelay$' -count=1 -timeout=120s
go test -work -mod=readonly -p=2 ./relay/channel/claude ./relay/helper ./relay/channel/openai ./relay ./controller ./service -count=1 -timeout=10m
go vet -mod=readonly -p=2 ./relay/channel/claude ./relay/helper ./controller
```

On Windows, a test can print `ok` but still exit 1 when temporary test executable
cleanup reports `unlinkat ... Access is denied`. That is not a clean command
result. Repeat with `go test -work ...` to retain its generated temporary build
directory and avoid that cleanup failure; do not disable security software or
delete another task's build directories.

From the source root, run the existing offline client harness contracts:

```powershell
node --test tests/compatibility/budget.test.mjs tests/compatibility/client-bridge.test.mjs tests/compatibility/live-fetch.test.mjs tests/compatibility/sdk-smoke.test.mjs tests/compatibility/cli-smoke.test.mjs
```

## Coverage And Evidence Boundaries

- The first regression must fail on the baseline: HTTP 200 SSE containing
  `message_delta` without `message_stop`, followed by clean EOF or
  `io.ErrUnexpectedEOF`, must return a non-nil error. Assert `NotNil` on the typed
  `*NewAPIError`; an interface-based `Error` assertion can accept a typed nil.
- Native Claude and Claude-to-Chat response handlers cover normal stop, missing
  stop, malformed/error events, scanner failure, timeout, cancellation after
  reported usage, and cancellation at stop. Failures retain reported token/cache
  evidence, skip successful finalization, and prohibit replay.
- `message_stop` terminates a still-open upstream body. A completed tool-use
  response is valid without text. No failed converted stream may emit `[DONE]`.
- OpenAI output buffers the `message_delta` terminal chunk until `message_stop`
  and successful stream/context validation. Truncated text and tool streams must
  expose no non-null finish reason, no usage final frame, and no `[DONE]`.
  Successful output must order its terminal frames as finish, usage, then DONE.
  A stop without a convertible message_delta terminal is an error, not a usage
  frame followed by DONE without a finish reason.
  Text/tool deltas remain streaming, and native Claude event order is unchanged.
- Native upstream numeric usage is observed before terminal buffering. Merge
  partial deltas with earlier input/cache counters, including cache TTL tiers,
  in `info.ZTAPIMetering`; never use the OpenAI usage observer for native Claude.
  Numeric observation does not mark billing integrity complete. The controller
  regression deliberately discards the adaptor's returned usage and exercises
  existing durable pending cleanup, proving the retained evidence is persisted
  without charging or releasing the existing hold.
- Claude stream writers preserve the valid object-valued `message_start.message`
  rather than misclassifying it as an OpenAI string-valued error message. Error
  frames must still redact private upstream content.
- The real controller regression traverses token authentication, distribution,
  request conversion, HTTP relay, response conversion and existing test-only
  settlement. It covers Messages-to-Chat and Responses-to-Chat routes with long
  history, two consecutive parallel-tool turns, four local tool executions,
  full-history replay and final text. Assertions inspect actual outbound tool
  result identities. A partial Responses stream after tools dispatches only once
  and never reports `response.completed`.
- Existing client harness tests validate CLI/SDK fixture orchestration and safety
  contracts. They are not a fresh real-client end-to-end run. Native-provider
  admission and its financial policy are not changed by this patch.

## Ownership And Release

Only Claude response code, Claude stream writers, the reproduced first-frame
parallel-tool conversion branch and regression files change.
Do not alter retry selection, `controller/relay.go`, prices, keys, balances or
settlement rules. Fixtures mutate only their isolated synthetic database.

## Upstream Variant Boundary

- The strict stop gate belongs to `ClaudeStreamHandler`. It covers the native
  Claude adaptor, Vertex's Claude mode, and AWS API-key mode, which delegate to
  that handler. Nonstream requests are unchanged.
- Claude models reached through Chat-backed Messages or Responses conversions
  use OpenAI handlers, not this native stop gate. Their upstream Chat completion
  contract is not changed to require Anthropic events.
- Existing output-only `message_delta` usage (including the Bedrock-shaped
  fixture) remains valid when followed by `message_stop`. Empty-text `tool_use`
  responses remain valid. Neither case needs invented text or repeated input
  usage in the final delta.
- AWS SDK `InvokeModelWithResponseStream` calls the shared Data/Final functions
  directly. A real-function regression exposed a missing-finish regression from
  terminal buffering. Final-chunk emission now lives in the shared Final function,
  preserving finish/usage/DONE for that call pattern. AWS and Vertex compilation
  is checked offline. The SDK caller's own EOF/error policy is not hardened or
  qualified by this change; it does not use `StreamScannerHandler`.
- Existing SDK smoke acceptance explicitly requires `message_stop`. No checked
  fixture or catalog entry authorizes a clean EOF without that event as a valid
  native Messages completion. An EOF-only bridge now fails deliberately; no
  supplier-specific bypass is added.
- The frozen `ztapi_supplier_admission_catalog_20261019.json` contains eight
  Anthropic/Claude candidates, all `admission_status=hold`; it does not prove live
  admission or terminal-event support. No production channel configuration or
  credentials were read. Unknown deployed variants therefore remain unqualified,
  rather than being reported as compatible from this static check.

## Task1 File Handoff

- `server/relay/channel/claude/relay-claude.go`
- `server/relay/channel/claude/claude_stability_test.go`
- `server/relay/channel/claude/claude_terminal_commit_test.go`
- `server/relay/helper/common.go`
- `server/relay/helper/claude_stability_test.go`
- `server/controller/relay_claude_stability_test.go`
- `server/controller/relay_claude_stability_metering_test.go`
- `server/service/convert.go`
- `server/service/claude_parallel_stream_test.go`
- `tests/compatibility/claude-continuous-work.md`

## Current Acceptance Status

- The native Claude interruption regression was observed RED for clean EOF and
  unexpected EOF, then GREEN after the terminal-state correction.
- The valid Claude `message_start` projection regression was observed RED, then
  GREEN after restricting error projection to Claude error frames.
- Follow-up review regressions were observed RED for prematurely emitted
  `finish:stop`/`finish:tool_calls` and missing native metering evidence. They are
  GREEN after buffering only the converted terminal chunk and capturing numeric
  provider observations independently of the returned usage pointer.
- The direct Data/Final SDK call pattern was observed RED for missing finish,
  then GREEN after consolidating terminal emission in the shared Final function.
- The Responses-to-Chat continuous controller journey passes, including two
  parallel-tool rounds and no redispatch after subsequent partial output.
- The Messages-to-Chat continuous controller journey exposed another RED:
  `service/convert.go:StreamResponseOpenAI2Claude` took only `ToolCalls[0]` in its
  first-frame branch. The authorized narrow correction preserves every tool's
  identity, index, argument fragments and terminal block state. The original
  controller assertions and six focused service subcases passed without being
  weakened; the full controller suite also passed (76.947 seconds).
  Reversed first-frame tools are stable-sorted in a local copy, preserving the
  provider chunk. In-memory playback with the installed Anthropic SDK 0.80.0
  and 0.73.0 passed without network calls.
- The writer's message_start exception is restricted to a valid typed and raw
  object-valued message with no error. Unknown/string-valued message payloads
  retain the previous public-error projection; leakage regressions were RED
  before this guard and GREEN afterward.
- Existing offline client contract tests pass 77/77. They do not execute a new
  real Claude Code/Anthropic SDK inference session.
- An earlier broad service run reached its 240-second package deadline. Its
  last reported hosted-tool case subsequently passed in isolation (4.414
  seconds); this does not erase the failed broad run. Frozen-source acceptance
  reruns the complete affected packages with a bounded ten-minute package
  deadline and preserves their exact command results.
  A later complete service run passed in 230.265 seconds. The earlier timeout
  remains recorded; no test timeout was weakened to hide an assertion failure.

Run the commands before release and record their actual exit status. Retain any
pre-existing failures separately from the focused regression results. Production
deployment and paid-provider qualification require the parent task's release
gates; this offline procedure performs neither.
