# Spending Frontend Acceptance

Scope: frontend Task 3 of
`docs/superpowers/plans/2026-11-02-claude-stability-spending.md`.
The approved spec and its refund/ordinary-input clarification are authoritative.
This is not backend, production, delivery, or paid-model acceptance.

## Contracts And Decisions

- GET config uses `data.preferences` plus timezone and Telegram capability flags.
- PUT config sends exactly seven editable fields and returns preferences only.
- Read acknowledgement sends JSON `{}` and requires `data.read === true`.
- Money stays decimal strings. Gross spending and refunds are separate.
  Null evidence stays unavailable; incomplete summaries remain visibly incomplete.
- Ordinary input Tokens exclude cache; missing confirmed usage does not imply zero.
- Optional requests reuse the existing client's in-memory token, owner header,
  credentials and session lifecycle. They deliberately do not call the generic
  destructive 401 refresh/replay path. A sidecar 401 cannot invalidate a healthy
  main session. A sidecar 401 has a distinct credentials-expired/unaccepted notice,
  not the 503 unavailable state. Only the explicit reload action calls
  `window.location.reload()` after a native confirmation warning about unsaved
  changes and unfinished operations. Cancel retains the current form and session;
  confirm reloads the same URL so the existing AuthProvider rechecks the main login.
  No refresh/logout is initiated by an auxiliary 401 and no write is replayed.
- Only `spending_version_conflict` produces the other-session conflict message.
  `spending_telegram_binding_required` and `spending_telegram_unavailable` retain
  edits and offer a support link in a separate tab. Explicit retry/master disable
  remains possible, without silently changing the Telegram preference.
- Negative measured_value is accepted only for low_balance. Other kinds and all
  thresholds remain nonnegative; negative balances never invalidate a whole inbox.
- The shared in-memory inbox allows one visible-page GET per 60 seconds without
  overlapping reads. A started read survives StrictMode subscription replay and
  has a 15-second transport deadline. Session changes clear memory, abort old
  operations and fence stale results, including same-owner login changes.
- Saving is explicit, never replayed. Conflicts and uncertain results retain edits
  and require an explicit reload. Overview refresh never discards settings edits.
- No binding, Telegram send, model invocation, money mutation or persistent
  browser storage of spending records is implemented.

## Commands

Run from `web/console`, using the already linked dependencies:

```powershell
node node_modules/vitest/vitest.mjs run src/api/spending.test.ts src/features/spending/SpendingPage.test.tsx src/features/spending/useSpendingInbox.test.tsx src/components/layout/ConsoleHeader.test.tsx src/components/layout/ConsoleShell.test.tsx src/components/layout/WorkbenchShell.test.tsx src/app/router.test.tsx --maxWorkers=2 --reporter=dot
node node_modules/typescript/bin/tsc --noEmit -p tsconfig.json
node src/features/spending/SpendingPage.browser.mjs
```

Observed RED cases preceded their fixes: missing route/navigation/modules,
missing refund parsing/display, refresh losing edits, actual sidecar wire shapes,
StrictMode losing the first inbox response, unconfirmed read acknowledgement,
ordinary-input notification wording, and a 320x568 panel extending below screen.

The browser harness uses a temporary loopback-only Vite instance, fixture auth/API
responses and headless Chrome. All unmatched API/model and external requests are
blocked and reported. Its `finally` closes Chrome and Vite. It writes screenshots
and `result.json` to `../../../spending-ui-evidence` by default; an output-directory
argument overrides that path. `SPENDING_BROWSER_WIDTH` optionally narrows a debug
run. The default covers 320x568, 390x844 and 1440x1000, Chinese/English, light/dark,
layout overflow/overlap, explicit save, read acknowledgement, conflict reload,
late old-owner results, and an optional-service outage with workbench navigation.

## Compiled And Published Static Modes

The harness supports `--static-dir` for an isolated local compiled build and
`--base-url` (alias `--origin`, or `SPENDING_BROWSER_BASE_URL`) for the published
origin AFTER release. Do not run the published command before parent authorization.

```powershell
node node_modules/vite/bin/vite.js build --outDir ../../../spending-static-local-build --emptyOutDir false
node src/features/spending/SpendingPage.browser.mjs --static-dir ../../../spending-static-local-build --evidence ../../../spending-static-local-evidence
# After release only, substitute the actual HTTPS origin:
node src/features/spending/SpendingPage.browser.mjs --base-url https://YOUR_PUBLISHED_ORIGIN --evidence ../../../spending-published-evidence
```

Published mode starts no dev server, requires a compiled `/assets/*.js` bundle,
and never imports dev `/src` modules. Account switching uses real login/logout UI
with fully intercepted synthetic API responses. Browser contexts are fresh, with
no customer profile or storage state; cookies are asserted empty. Only allowlisted
same-origin static GET/HEAD resources can reach the asset server through Node fetch
without browser cookies, auth or referrers. Redirects are not followed; Set-Cookie
is stripped. All API/model calls are fixtures or blocked, all WebSockets are closed,
and unknown/external requests fail the acceptance. Static asset SHA-256 values are
recorded in result.json. Local static mode deliberately injects a synthetic
Set-Cookie header and verifies zero cookies/auth/API requests at the static server.

The desktop interaction additionally advances 15 minutes of virtual time, injects
an auxiliary 401, verifies no automatic main refresh/logout, cancels a warned reload
without losing edits, then accepts one same-URL reload and observes one main login
recheck. This is fixture evidence, not a live-customer token or production API test.

## Remaining Parent Gates

Run the final frozen console regression and release artifact build, independent review,
real sidecar integration and deployment acceptance. Targeted tests and synthetic
browser screenshots are not proof of a live release or Telegram delivery.
