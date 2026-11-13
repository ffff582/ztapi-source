# Phase 2 Installed CLI Runner

This runner qualifies installed clients against the **simulated** Go bridge only.
Unit tests are not actual-client qualification, and simulated qualification is
not live-provider, accounting, deployment, or product certification.

## Offline Validation

```powershell
node --test tests/compatibility/cli-smoke.test.mjs
```

Tests mock client inference and the shared identity request. They do not start
Codex/Claude inference, contact existing listeners, install packages, or modify
user profiles. Process tests launch only small local Node fixtures.

## Parent-Owned Actual Runs

After the corresponding Go bridge profile is running, use absolute paths:

```powershell
node tests/compatibility/cli-smoke.mjs --client codex --ready D:/ztapi-acceptance/codex/ready.json --output-dir D:/ztapi-acceptance/codex/cli-evidence
node tests/compatibility/cli-smoke.mjs --client claude-code --ready D:/ztapi-acceptance/claude/ready.json --output-dir D:/ztapi-acceptance/claude/cli-evidence
```

The bridge owns `ready.json` and its sibling `project/fixture.txt`. Each actual
run needs a fresh project containing only that fixture; a preexisting
`client-result.txt` is refused rather than overwritten. Go configuration and
listener lifecycle remain parent-owned. This runner never kills the bridge PID.

Readiness must contain `base_url`, `api_key`, `model`, `pid`, `run_nonce`, and
`provider_kind`. The shared `client-bridge.mjs` validates simulated mode, model
`zt-claude-sonnet-5`, positive PID, a 32-character lowercase hex nonce, and numeric
loopback HTTP with an explicit nondefault port. The runner additionally rejects
alternate IP spellings and endpoint decoration. Before **any client spawn**, it
awaits shared `verifySimulatedBridge(ready)`: the nonbillable identity GET must
match the instance nonce and PID. Loopback alone does not authorize inference.

## Installed Entrypoints And Isolation

- Codex is pinned to `0.165.0-alpha.1.2` at the supplied OpenAI installation
  path. `exec --help` confirms invocation flags; ignore flags belong after `exec`.
- Claude Code is pinned to `2.1.312`. The runner resolves the supplied npm shim
  using only its matching package identity and `bin.claude` entry. This machine's
  package points to `bin/claude.exe`, not `cli.js`. Both verified package layouts
  are supported; JS uses the current absolute Node executable, never a shell shim.
- `-V` and help are checked before inference. Version mismatch, missing flags,
  missing binaries, Windows sandbox failures, and failed bridge identity are
  environment failures, not gateway compatibility failures. No automatic install,
  update, sandbox bypass, or global configuration change is attempted.
- Child environment starts from only PATH, SystemRoot, WINDIR, ComSpec, PATHEXT,
  and SystemDrive, case-insensitively. HOME, USERPROFILE, CODEX_HOME,
  CLAUDE_CONFIG_DIR, APPDATA, LOCALAPPDATA, TEMP, and TMP are fresh directories
  under the owned evidence run. Provider/OAuth, proxy, Node injection, MCP,
  plugin, application, and other inherited variables do not survive.
- Only the selected fixture API key is added for inference, never for version
  or help. It is not passed in arguments. Codex uses a custom Responses provider;
  Claude uses bare mode, Read/Write only, empty strict MCP, no settings sources,
  no session persistence, and a 0.8 USD client ceiling. That ceiling does **not**
  authorize paid traffic.
- Codex disables hosted search, plugins, remote plugin catalog, hooks and apps;
  ancestor project configuration is rejected. No real user configuration is edited.

## Evidence And Failure Semantics

Each invocation creates `output-dir/cli-CLIENT-RANDOM/receipt.json`, redacted
`client.stdout.jsonl`, and redacted `client.stderr.log`. Files request mode 0600;
on Windows the newly owned run directory also gets a current-user-only DACL.
Only that new directory's permissions are changed. Controlled temporary homes
are removed afterward; the project artifact remains for inspection.

Passing requires exit code zero, terminal client success, exact final
`ZTAPI_CLIENT_OK`, a newly created file containing `ZTAPI_CLIENT_FILE_OK`, its
SHA-256 over original bytes, and actual successful tool events. Codex additionally
requires the shell-output marker `ZTAPI_CLIENT_EXEC_OK`; Claude requires matching
Read and Write tool-use/result IDs. Text or an artifact alone cannot pass.
Unknown client event shapes remain incomplete evidence, not invented success.
Codex execution proof must come from a parsed, completed `command_execution`
event with numeric `exit_code: 0` and a string `aggregated_output` containing the
execution marker, never from global stdout or model text. Claude results may
omit `is_error`; when present, it must be the boolean `false`.

Receipts record actual version, exit code, runtime, event summary, artifact hash,
redacted error classification, `provider_kind: simulated`, and `paid_calls: 0`.
Dispatch count stays null until joined to the parent-owned bridge receipt; one
client process is not assumed to equal one model request. Generic model-metadata
fallback is recorded as an unverified warning, not a capability claim.

Child-process work shares a 180-second deadline, with bounded output capture.
Timeout or output overflow terminates only the spawned child tree (Windows
`taskkill /PID ... /T /F`, POSIX dedicated process group); termination can add a
short OS cleanup interval. Raw output is buffered and redacted before persistence;
the readiness file and child environment are never dumped.

Flag availability was inspected using the installed versions' help. Configuration
keys were checked against the [official OpenAI configuration reference](https://developers.openai.com/codex/config-reference).
