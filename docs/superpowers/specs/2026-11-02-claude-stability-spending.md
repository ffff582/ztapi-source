# Claude Continuous Work And Customer Spending Alerts

The owner approved these two upgrades and deployment after acceptance. Keys, customer balances, prices and channels must not be automatically changed. Preserve existing customer requests, support, finance and Telegram services. Work from deployed source9d3a0a068836e0dfc4dc28a706923bd3ae0fd4ae; preserve the unrelated dirty commercial worktree.

## Chosen Approach

Extend the existing relay's actual continuous-work regressions and fix only reproduced protocol/transport defects. A reply-to-OK probe is not full coding-client acceptance. Preserve usage evidence, internal billing and cancellation semantics. Never retry after partial output or tool execution merely to obtain a successful response; never silently change models or channels.

Run customer alerts as a separate, resource-bounded sidecar. Its main-database connection is SELECT-only; preferences, notification records and delivery state live in its own SQLite store. An unavailable alert service must not affect model admission, charging or API availability. This is preferred to putting a notification polling loop into the billing transaction or existing Telegram bot. Existing billing statistics without a rule engine would not provide timely opt-in alerts.

## User Experience

- Add a Consumer Spending page at /console/spending and a shared header bell accessible in both console and workbench. Match the current quiet console layout, with icon controls, explicit labels, light/dark themes, English/Chinese and responsive320/390/1440px views.
- Alerting defaults OFF. The user may set daily confirmed spending (Beijing calendar day), rolling60-minute spending, low available balance and an optional per-request input-token warning. Empty/zero individual thresholds are disabled; enabling requires at least one positive valid threshold.
- Explain financial state using separate confirmed gross spending, confirmed refunds, available balance and pending/incomplete counts. Unknown money is null/unavailable, never fabricated zero. A reservation is not a final charge. Include all supported customer charge evidence, including independent hosted-tool charges; do not add a final charge and its ledger/log receipt twice. Thresholds use confirmed gross spending; refunds are shown separately, not silently treated as an unspend operation.
- Inbox notifications are visible only to the authenticated owner. The bell polls at most once per60seconds while visible, with no overlapping calls. Account changes/logout immediately clear old-owner data and ignore stale responses. Failure of this optional fetch never logs out a healthy model-API session or blocks the page.
- Telegram delivery is separately opt-in, only to an already verified binding for that same owner. No client-selected chat IDs, group messages, admin fallback or new bot/key. Unbound users can use the website inbox. Revalidate account/binding generation before delivery; use only a generic alert and a fixed ZTAPI link in Telegram, not account identifiers, amounts, request IDs or secrets.
- No hard spending cap, auto-suspension, auto-refund, balance mutation or channel switching in this release.

## HTTP Contract

All /api/spending endpoints require a valid existing ZTAPI access token, matching New-Api-User and a currently enabled, nondeleted owner. Use strict JSON, bounded bodies, fixed public errors, no-store responses, same-origin checks on writes and owner scoping on every record. No endpoint accepts user_id or arbitrary callback URLs.

- GET /api/spending/config: preferences plus telegram_bound and telegram_available. Preferences: enabled:boolean; daily_spend_u, hourly_spend_u, low_balance_u:decimal strings (empty disables); input_tokens:integer (0 disables); telegram:boolean; version:integer. Return timezone=Asia/Shanghai. Initial version0. PUT the same editable fields plus version; optimistic version conflicts return409. Maximum threshold1000000U, up to6 decimal places; input_tokens0..1000000000. Never accept negative, nonfinite, exponent, unknown or duplicate fields.
- GET /api/spending/overview: as_of, timezone, day_start, complete, confirmed_daily_u:string|null, confirmed_hourly_u:string|null, confirmed_refunds_daily_u:string|null, available_balance_u:string|null, pending_requests:integer, incomplete_requests:integer. Complete means the queried evidence is complete, not that every upstream bill has arrived. No pagination may silently turn a partial aggregate into a complete total. Daily/hourly windows use the durable final-charge outbox posting timestamp, not request start, cache-sync updated_at or log delivery time.
- GET /api/spending/alerts: {items:Alert[], unread_count:integer}; limit defaults20, maximum100, optional positive before cursor. Alert: id, kind (daily_spend/hourly_spend/low_balance/large_input), created_at RFC3339, measured_value, threshold, unit (U/tokens), request_id/model optional, read_at optional, delivery_status. Messages are fixed templates, not raw upstream content.
- POST /api/spending/alerts/{id}/read: owner-only idempotent acknowledgement; cross-owner/missing ID404. Read state changes only the private notification store.
- GET /healthz: minimal unauthenticated readiness, no customer or configuration evidence.

## Processing And Financial Safety

Use bounded read queries and exact decimal/quota conversion from evidence rather than JavaScript floats or a guessed exchange rate. Validate authoritative charge coverage against existing settlement/outbox/ledger contracts before acceptance. Do not infer spend from the balance changing, since funding, holds and refunds differ. Do not access prompts, tool arguments, plaintext tokens or other users' records to build alerts.

Only enabled preferences are evaluated, in bounded batches, with a small read-connection pool and query deadline. No primary-schema migration is needed. Missing tables, evidence conflicts, unavailable reads or overflow fail closed for totals/notifications and are surfaced as unavailable, not as a zero charge.

Notifications have durable deduplication. Daily notices are once per owner/rule/day; rolling-hour notices have at least60-minute cooldown; low-balance notices have a24-hour cooldown with rearming on recovery; per-request input alerts deduplicate by owner/request. Updates must not let duplicate concurrent workers or simple repeated saves flood a user. Setting changes can change future thresholds, not historical money.

Only confirmed normalized ordinary-input dimensions may drive input-token notices; name this ordinary billed input rather than total context. Do not add cache reads/writes again or treat missing usage as zero. Preserve the actual evidence/unknown state.

Telegram never calls getUpdates or an AI model. Explicit rate-limit failures may retry with bounded delay after revalidation; an ambiguous send must not be blindly retried as if unsent. Website evidence remains available even if delivery fails. Disable/unbind prevents subsequent sends. No notification is sent before the user enables it.

The new service uses its own minimally granted SELECT-only database account if existing read roles do not cover the required evidence. Creating this internal read role must not reset credentials or expand privileges of any existing account, user API Key or channel. It must never receive root DB credentials or model provider keys. The existing shared HS256 session key has signing capability; do not describe it as cryptographically verification-only.

## Acceptance And Release

Zero-paid-call acceptance first: real relay httptest/fault fixtures, existing client-compatible multi-turn/tool flows, strict timeout/EOF/cancellation cases, immutable fee evidence and retry safety. Do not claim real paid upstream verification from fixtures. This turn grants no new numeric paid-test budget, so do not spend one without separate authorization.

Alert acceptance covers owner isolation, revoked account, CSRF, precision, Beijing midnight/UTC boundaries, duplicate/missing receipts, pending/released settlements, hosted-tool double-counting, unknown dimensions, restart/dedup, late finalization, preference CAS, disabled rules, binding changes, blocked bot, timeout/unknown delivery and read-store failure. Use deterministic clocks and synthetic evidence, not live customer writes or outbound Telegram messages.

Frontend: full unit/type/build regression plus browser screenshots and interaction tests across320/390/1440px, account switching, optional-service failure and existing text/media/support workflows. Keep long-text/security fixes from the deployed base.

Release the independent alert sidecar without touching existing service identities; route only /api/spending/ after its readiness and authorization probes pass. Publish exact AGPL source and immutable artifacts. Use the existing proven rolling backend handoff for relay changes: healthy candidate, hot Nginx reload, drain all old streams/settlements/statistics before stopping, preserve old image/container/config and verified database backup. Never restore a database over new customer transactions. Static assets first, index atomically last. Verify protected environment, channels/prices/publication digests and unchanged finance/support/bot services before and after.
