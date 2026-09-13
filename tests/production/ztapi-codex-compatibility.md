# ZTAPI Codex Compatibility Acceptance

This report is completed against the deployed production commit. Local checks do not count as production acceptance.

| Check | Required evidence | Status |
|---|---|---|
| Official model alias | Codex uses the official model name without an unknown-model warning | Pending production |
| Reasoning effort | Admin audit shows received client effort and final forwarded effort; `ultra` is recorded as client alias and forwarded as `max` only when declared | Pending production |
| Reasoning usage | Upstream `reasoning_tokens` is recorded when supplied, without treating it as proof of effort | Pending production |
| Tool round trip | Responses tool call, tool output, and final answer retain their schema | Pending production |
| File edit | `apply_patch` creates and modifies a temporary file through ZTAPI | Pending production |
| Command execution | A shell command available in the selected environment runs successfully | Pending production |
| Long conversation | State and tool-call continuity survive context compaction without duplicate assistant output | Pending production |

Environment limitations, such as a missing Python executable, are recorded separately and do not count as a relay failure. If context compaction cannot be triggered deterministically, it remains pending.
