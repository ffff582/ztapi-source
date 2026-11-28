# Platform Support

The independent support service never starts the main API resources, runs wallet
settlements or changes model routes. Its main-database identity has column-level
SELECT privileges only. Private conversations, screenshots, tickets, admitted
calls and immutable completion receipts live in `/opt/ztapi-support/data/support.db`.

## Operations

- Customer entry: `/console/support`. Human inbox: admin `/support/tickets`.
- Human tickets are durable, not a promise that an operator is online. An open
  or in-progress ticket pauses AI inference for that conversation. Replies and
  explicit status changes are visible to the customer after refresh/polling.
- Knowledge is versioned in `knowledge.md`; public model capabilities/prices
  come from the fixed main API pricing projection. Update the knowledge when
  public business rules change. Never insert internal procurement or credentials.
- There is no daily usage/fee limit. Short-window throttles, two inference slots,
  16 HTTP slots, image quotas, a 960MiB private-store write threshold and 1GiB
  free-disk guard protect serving capacity. Existing reads remain available if
  storage admission fails; in-flight receipts retain reserved headroom.
- DeepSeek Flash uses the authorized Xcloud domestic group. The protected quote
  freezes base prices, group multiplier and prepaid cash conversion separately.
  A key/quote change requires a controlled support-only configuration rollout.
  Do not substitute the official hosted-search credential.
- Finance reads the store read-only. Quote amounts are cash estimates, not proof
  of supplier debit. Inclusive input excludes cache hits exactly once. A complete
  channel invoice covers its support subset instead of adding it twice.
- Missing receipts or disputed usage stay unknown and do not trigger automatic
  retries. A local record proving no dispatch has confirmed zero supplier cost.
- Daily consistent SQLite backups validate SHA256 and `gunzip -t`. Failure alerts
  contain no chat or screenshot content. Keep backups private and recoverable.
  A 2GiB backup-directory budget and extra disk headroom stop new backups before
  they can fill the host; failure alerts require an operator to archive capacity.
  Existing verified backups are never automatically deleted.

## Release And Rollback

`deploy/release.py` prepares isolated candidate services, then activates tested
routes and static entrypoints through a graceful Nginx reload. It checks the
main API and old finance container identities before and after. Existing static
assets and old finance remain available; never recreate the Nginx container.

If customer support must be disabled, remove its new routing/UI entrypoints and
gracefully stop only the support sidecar after draining in-flight calls. Preserve
the private store and the new finance reader so already-incurred support costs
remain visible. Do not restore an older finance implementation that ignores these
costs merely to hide an incident. No customer balance or historical invoice is
rewritten as part of rollback.

## Telegram

The existing @ztapi_bot also serves the owner-enabled public group
https://t.me/ztapivip. Operational alerts remain private to the existing owner.
The Telegram worker has a separate store, no main DB connection and no platform
session-signing credential. Its store's actor IDs represent Telegram users only,
never platform identities. Finance aggregates its immutable usage read-only.

Explicit mentions and replies receive answers or clear failure/refusal messages.
Validated current-message pictures are supported. Unmentioned small talk is
low-frequency, only when readable fresh group messages are available; bot-to-bot
loops and old-message chatter are excluded. Personal billing and diagnosis move
to authenticated site support. Owner commands control group enablement. The worker
persists delivery separately from inference and retries saved Telegram replies,
not paid model calls. Do not start a second getUpdates receiver or overwrite an
existing webhook. Keep the existing main API private alert destination unchanged.
