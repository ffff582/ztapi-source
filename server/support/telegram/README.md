# Telegram Support Worker

This package runs only against a dedicated private SQLite database. The `db`
and `support.Store` passed to `NewWorker` must point to the same Telegram-only
database. Never expose that store through the website support HTTP handler.

`support.Store.UserID` in this database means Telegram sender ID, NOT platform
user ID. There is no platform identity lookup, JWT, customer source, balance
lookup, or platform-request diagnosis. Only the public-knowledge callback is
available to inference. Conversation identity is the stable tuple of chat ID,
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
  `telegram_support_inbox`, `telegram_support_outbox`, `telegram_support_dialogues`.
- Updates and directed-message fallback outbox rows commit in the same transaction
  before the cursor advances. Duplicate updates do not repeat inference or replies.
- Only the configured owner may enable/disable a group with `/support_on` and
  `/support_off`. Commands and explicit mentions/replies remain eligible even when
  queued before installation. Other bots are ignored.
- Normal-message participation is only reactive to received fresh messages: a
  deterministic SHA-256 selection modulo 40 plus a persisted 30-minute group
  cooldown. Old non-directed messages, other commands, and unsolicited images are
  not casually processed. Receiving no normal messages cannot trigger posting.
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
- Requests for specific private account records, real credentials or privileged
  backend operations are refused with a website-login link. Public billing rules,
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
