# Telegram Support Worker

This package runs only against a dedicated private SQLite database. The `db`
and `support.Store` passed to `NewWorker` must point to the same Telegram-only
database. Never expose that store through the website support HTTP handler.

`support.Store.UserID` in this database means Telegram sender ID, NOT platform
user ID. There is no main database, JWT or customer-source credential. Optional
account reads go through the dedicated HMAC bridge; usernames never authorize
anything. Returned diagnoses are rendered deterministically, not put into model
history. Only the public-knowledge callback is available to inference.
Conversation identity is the stable tuple of chat ID,
sender ID and topic/thread ID. Inference receives only that actor's last nine
redacted user/assistant text messages plus the current message. Other actors and
historical image bytes never enter the prompt. Physical Store segments rotate
before the storage-length cap without changing the actor identity or last-nine
text window; old calls, receipts and messages remain intact.

## Integration

```go
client, err := telegram.NewClient(token, httpClient)
worker, err := telegram.NewWorker(telegram.Config{
    OwnerID: ownerID,
    BotID: botID,
    BotUsername: botUsername,
    ChannelID: providerChannelID,
    Model: providerModel,
    Supplier: providerSupplier,
    Environment: "production",
    VisionEnabled: visionAccepted,
    Bridge: bridgeClient, // Optional; Execute(ctx, bridgeapi.Request).
    BindingKey: raw32ByteBridgeSecret, // Required for private binding claims.
    AdmitWrite: privateDatabaseAdmissionGuard,
}, tgDB, tgStore, provider, publicReader.PublicKnowledge, client)
```

Owner ID, bot ID and username are mandatory configuration with no defaults.
`NewWorker` only migrates its private tables and validates the persisted bot/owner
binding. It does not verify Telegram, poll, send messages, or invoke inference.
Disabled CLI initialization must stop there. The parent owns protected credentials,
database/WAL/disk admission policy, database lease, and process lifecycle.

Before enabling, explicitly call:

```go
err = client.Verify(ctx, ownerID, botID, botUsername)
err = worker.Run(ctx)
```

Verify checks `getMe`, the owner's private chat, and an empty webhook. It never
deletes/overwrites an existing webhook or uses moderation/admin endpoints.
`Step(ctx)` performs one bounded poll/process/outbox pass for tests or supervisors.
Do not run multiple workers against the same database; enforce the parent's lease.

## Durable Behavior

- Private tables: `telegram_support_cursor`, `telegram_support_groups`,
  `telegram_support_inbox`, `telegram_support_outbox`, `telegram_support_dialogues`,
  `telegram_support_preferences`, `telegram_support_intakes`.
- Updates and directed-message fallback outbox rows commit in the same transaction
  before the cursor advances. Duplicate updates do not repeat inference or replies.
- Only the configured owner may enable/disable a group with `/support_on` and
  `/support_off`. Commands and explicit mentions/replies remain eligible even when
  queued before installation. Other bots are ignored.
- Normal-message participation is only reactive to received fresh messages: a
  deterministic SHA-256 selection modulo 40 plus a persisted 30-minute group
  cooldown. Old non-directed messages, other commands, and unsolicited images are
  not casually processed. Receiving no normal messages cannot trigger posting.
- Fresh `new_chat_members` service messages welcome human members only in groups
  already enabled by the owner. One fixed welcome covers the whole event, never
  calls a model or stores member names, and reuses durable update/outbox identity.
  Bot-only, private, unknown-group and stale joins are ignored. Pending welcome
  retries are suppressed if the owner disables the group before delivery.
- The Store enforces six admitted calls per sender/minute and sixty globally per
  minute. There is no daily usage cap. Rejections receive fixed durable replies.
- Exactly one model attempt is permitted per update. Admission is durable before
  dispatch. Interrupted/dispatched calls recover a receipt/fixed fallback, never
  an automatic model retry. Missing usage/cost remains unknown, not zero.
- `AdmitWrite` applies to new work only. Existing dispatched calls recover before
  this gate; receipts and outbox completion do not recheck it.
- After cancellation, no new tasks are fetched/admitted. A dispatched provider has
  an independent 45-second context, followed by one shared five-second receipt/
  outbox persistence budget. The caller must wait for Run to return.
- Telegram sends retry saved text only. `retry_after` delays are persisted; retries
  do not run the model. Delivery is **at least once**: Telegram has no sendMessage
  idempotency key, so a crash/ambiguous network failure after remote delivery but
  before local acknowledgement may duplicate text, never model charges.
- Images use only the current message's file ID. `getFile` and downloads use the
  fixed Telegram origin, no redirects, validated relative paths and a 3 MiB limit,
  then `support.ValidateImage` for JPEG/PNG/WebP. Image bytes are transient and are
  never stored as historical attachments. Failure produces a durable fixed reply.
- Groups never read private account records. All private user messages are handled;
  ordinary configuration help needs no binding. Real credentials receive a fixed
  withdraw/rotate instruction without echo, inference or automatic key mutation.
  Public billing rules,
  top-up instructions, bill locations and request-ID guidance remain answerable;
  normal business nouns in answers are not treated as sensitive. Operational
  details/alerts go only to the configured owner's private chat. Bot tokens are
  absent from returned errors and Client formatting; no package request logging.

Finance reads immutable call/receipt projections through
`support.ReadFinancialUsage` on this separate database. The parent combines the
website and Telegram databases without mapping sender IDs to platform users.

Tests use an in-memory SQLite database and an intercepting HTTP transport. They
perform no real Telegram/network/model requests. Mock vision tests do not prove
the live provider's vision capability; that remains a separate acceptance gate.

## Private Workflows

`NewBridgeClient(endpoint, secret, client)` accepts only
the original `http://ztapi-support-20261102:3012/internal/telegram-support` or
the exact owned `ztapi-support-experience-20261102` / `-candidate` host on the
same port and path, with a raw 32-byte secret. It disables redirects/cookies (and environment proxies on its default
transport), caps request/response sizes and signs each POST with a fresh 32-character
lowercase-hex nonce and Unix-seconds timestamp via `bridgeapi.Signature`.

The CLI's protected JSON may contain optional
`bridge: {endpoint, secret}`. `secret` is 64 hex characters, decoded to the bridge
secret and AES-256-GCM binding key; neither is taken from a plaintext key env var.
No bridge configuration means no verified account reads or binding claims.

Private `/start bind_<43-character-base64url-token>` only claims a website
challenge. It explicitly asks the user to return to the logged-in website and
confirm the numeric Telegram identity. The durable inbox contains redacted text
and AES-GCM ciphertext, with AAD binding bot/update/actor/chat. Restart can recover
an unprocessed claim; completion clears its ciphertext. No secure key means no
claim. A claimed challenge is never described as an active binding.

`/binding`, `/recent`, `/diagnose request_id` and private account questions use
only active-bound bridge results. Missing costs/usage remain unknown. Account
outbox deliveries recheck the exact original binding generation, including after
transport failure or rebind; missing/mismatched generations reveal no old record. All fallback
paths lead to `https://ztapi.vip/console/support`. Model output cannot invoke
bridge actions, create keys or invent refund/repair/handoff success.

`/handoff` or an explicit human-support request creates a site ticket when bound,
with the fixed `tg-action:<botID>:<updateID>` idempotency key. Otherwise it records
an explicitly unverified local intake and owner-private outbox notice. Only the
configured owner, in their own private chat, can use `/case_reply ID text` or
`/case_resolve ID`. Those commands never reveal case information to a group.

Ticket events are polled at most once per 15 seconds, deduplicated by event ID,
and rendered only as ticket number, fixed status text and the support link. Each
send first calls `authorize_notification`. A successful send is persisted before
`ack_notification`; failed ACKs retry ACK only, never send or inference. Telegram's
remote-send/local-save ambiguity remains the at-least-once limitation above.

`/memory` or explicit "remember" language saves only bounded client/OS/model
preferences for the current actor/chat/topic. `/forget` clears these and rotates
the context pointer so all earlier text is excluded from future prompts, while
immutable financial evidence and original messages remain intact.

## Optional Live Acceptance

`TestLiveSupportExperienceAcceptance` is SKIPPED by default. Only an operator may
enable it with all of these absolute paths:

- `SUPPORT_EXPERIENCE_LIVE_CONFIG`: protected regular JSON containing a complete
  `support.ProviderConfig` for authorized Xcloud/channel8/Flash, vision enabled.
- `SUPPORT_EXPERIENCE_LIVE_OUTPUT`: nonexistent output file, exclusively created
  with mode0600; an existing file is never overwritten.
- `SUPPORT_EXPERIENCE_LIVE_IMAGE`: synthetic HTTP503 screenshot, no real secrets.

It uses fake Telegram/Bridge transports and embedded approved public knowledge
without fetching a live catalog. Five distinct actors exercise meme, setup,
billing, fault and vision, with at most one real model attempt each and no retry.
The operator must reserve the five0.1U allowances before execution; this helper
does not manage funding or treat quote estimates as actual deductions.

Output schema1 contains `environment: "telegram-acceptance-v2"`, `cases` with
reply/checks/call_id/attempts, and top-level `usages: []support.FinancialUsage`.
A defer exports all available admitted/completed immutable facts even after a
partial failure. Missing actual costs stay null. Parent tooling owns artifact
hash/purpose evidence and append-only import to the stopped receiver's existing
Telegram store; never fabricate platform request lineage or mutate customer funds.
