# Local Support Browser Acceptance

Never deploy `server/cmd/ztapi-support-fixture`. It binds only an explicit
loopback address, creates fresh private SQLite databases in a new state directory,
and has no production configuration, MySQL source, provider URL or paid provider.
It refuses relay routes and financial mutations. All users, request evidence,
provider replies, token usage and quote amounts are synthetic.

The fixture uses the real support HTTP handler, real session JWT issue/parse
functions and the real finance HTTP handler. Login/refresh cookies are a local
fixture mechanism, not a test of the production refresh-session database.
Finance reads real support receipts through a separate SQLite `mode=ro` and
`query_only` connection. Synthetic quote costs are estimates, never actual debits.

## Commands

Run from the repository root using existing installed Go, Node, Chrome and cached
dependencies. No package installation or external requests are needed.

```powershell
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOTOOLCHAIN = 'local'
go -C server test ./cmd/ztapi-support-fixture -count=1 -v
node tests/compatibility/support-vision-fixture.mjs
node tests/compatibility/support-browser.mjs
# After a scroll-only UI fix, avoid repeating finance and unrelated pages:
node tests/compatibility/support-browser.mjs --scroll-only
# After a community-entry-only UI change:
node tests/compatibility/support-browser.mjs --community-only
```

The browser command builds the fixture, selects unused loopback ports, starts
public and classic-admin Vite servers with fixture-only proxies, uses fresh
browser contexts, then stops the browser, servers and fixture. Existing browser
profiles, users and running services are not used. Output is under:

`C:/Users/Administrator/Documents/翻译软件/outputs/ztapi-commercial-upgrade-20261030/support-20261102/`

`SUPPORT_E2E_OUTPUT` can select another result directory.
`SUPPORT_VISION_FIXTURE` can select another PNG destination.

Each successful run contains `results.json`, screenshots and accessibility
snapshots at widths 1440/390/320, `fixture.log`, the local fixture executable and
its disposable private SQLite state. The report contains the exact build/start
commands and counters, but no JWTs or cookies. Synthetic logins use
`fixture-only-password`; available synthetic account names are in fixture source.

## Checks

- Draft input does not create a conversation. Explicit sending persists chat,
  updates the title and survives a browser refresh without another POST.
- Owner diagnostics work; another user sees the same 404 as an unknown ID for
  diagnostics, conversations and private screenshots.
- Screenshots are not prefetched. User and staff explicitly open them with JWT
  headers; decoded bytes match the synthetic vision PNG hash.
- Human tickets accept follow-up messages without increasing the provider count;
  staff replies appear with the `human` role. Explicit status changes work and
  a real stale-version 409 reloads without automatically resubmitting.
- Support role 2 reads tickets; finance role 3 is denied both tickets and private
  support attachments, while real finance reports automatically read receipts.
- Text is rendered literally, not as model HTML. No page exceptions, unexpected
  missing assets, external page requests or document-level horizontal overflow.
  Existing navigation and financial tables may scroll within their containers.
- Fault injection discards the HTTP response only after the real handler has
  persisted a failed-provider `user` + terminal `system` pair. Refresh clears the
  persisted pending marker, unlocks input/navigation and sends no new POST.

The `--scroll-only` run uses the same real fixture but checks only public support
at 1440/390/320: initial latest-message position, preserving a reader's historical
position across actual incoming staff replies, near-bottom following, own-message
following and switching conversations. It asserts actual scroll distances and
human-reply viewport intersection, and records screenshots at each step.

The `--community-only` run checks the real unauthenticated public homepage and
authenticated support page at 1440/390/320. It verifies the community link in the
desktop navigation and mobile menu, its Lucide icon and exact safe external-link
attributes, and document-level overflow. It never follows the Telegram link.

The vision PNG is made by screenshotting a fresh white HTML page containing only
`HTTP 503` and `UPSTREAM_UNAVAILABLE`. It contains no customer material and does
not prove the real Flash provider can understand images.

## Frozen UI Evidence

```powershell
node tests/compatibility/support-freeze-manifest.mjs '<successful run directory>'
node tests/compatibility/support-freeze-manifest.mjs --check '<successful run directory>/ui-freeze-manifest.json'
```

This fingerprints only the authorized support API/folders, router/navigation and
central public header overlay, including related tests. The fixture and acceptance scripts are not part
of the production UI overlay. Release builds and post-release smoke tests remain
separate acceptance stages.
