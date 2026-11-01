# Support Experience Upgrade

Approved in conversation: technical group companion Xiao Z can respond to memes and occasionally tease situations, never customers. Errors, charges and account matters use a serious evidence-led tone. Add opt-in clearable client preferences, stepwise setup help, private account verification/diagnosis, handoff and ticket progress. No key creation, refund, wallet, price or routing changes.

## Scope And Decisions

- Preserve deployed a8f24edad96e42328b1ce2963d94c455e42d2ac6 application baseline. Reuse the existing isolated dirty worktree; publish only explicit task files on that baseline. Main model API and finance service must not restart.
- The existing Xcloud channel8/DeepSeek Flash remains the only customer-service inference provider. Original official-search credentials are never used for inference.
- No daily support cap is added. Existing short-window/concurrency/disk limits remain. This upgrade has an additional administrative test allowance0.5U, whole commercial budget15U and Opus3U unchanged; retain previous unknown holds.
- General help needs no binding. Website JWT plus a five-minute, single-use random challenge proves platform control. Telegram privately claims it, then the logged-in website explicitly confirms the Telegram numeric ID before activation. Claim is not binding. Usernames never authorize access. One Telegram identity cannot take over another user's binding. Unbind/rebind requires the website.
- Bot never receives main DB/JWT/provider-master credentials. A dedicated HMAC-authenticated, timestamp/nonce-protected internal endpoint exposes only fixed support actions. Every account read rechecks active platform identity and binding. The server chooses the platform user, never the bot/AI.
- Private diagnostics show only that bound user's bounded recent requests or a requested owned ID. Group replies contain no account evidence; private context never enters a group prompt. Missing fee/usage remains unknown. Models cannot execute bridge actions.
- Bound handoff creates existing site tickets; staff use the existing inbox. Private unbound users can submit an explicitly unverified intake to the owner, without private account disclosure. Owner-only private reply/resolve commands can close that intake. No claim of a created/escalated case without a durable record.
- Ticket updates are deterministic private notifications, not model calls. Poll/ack and Telegram outbox are durable/idempotent. Delivery rechecks active bindings; notifications contain a ticket number/status/site link, not balance, prompt text, purchase price or human reply body. No new public operational announcements; internal alerts remain owner-private.
- Preferences are explicit opt-in, bounded client/OS/model fields only. No raw arbitrary facts/keys. Scope by actor/chat/topic; private preferences do not enter group prompts. Forget deletes preferences and excludes preceding text from future model context, but does not destroy immutable cost receipts.
- Credential leakage receives a fixed instruction to withdraw/rotate, never an echo or automatic key mutation. An @mention always gets an answer/refusal/fallback, absent delivery/API failure. Ordinary chatter remains low-frequency; service failures/cost disputes have no jokes or mockery.

## Interfaces

Shared package support/bridgeapi defines Request/Result/Diagnosis/Ticket/Notification plus HMAC canonicalization. POST /internal/telegram-support accepts actions claim_binding, binding_status, recent_requests, diagnose, create_ticket, poll_notifications, ack_notification, authorize_notification. Fixed headers X-ZTAPI-Telegram-Timestamp, X-ZTAPI-Telegram-Nonce, X-ZTAPI-Telegram-Signature sign timestamp + LF + nonce + LF + POST + LF + path + LF + raw body with HMAC-SHA256. Window90seconds; nonce consumed once; maximum body16KiB; no Origin/session credential substitutes.

Website GET/POST /api/support/telegram-binding uses existing JWT/origin rules. POST has action create|confirm|cancel|unbind, idempotency_key and optional challenge_id. Response data is BindingView: enabled,bound,telegram_id,username,display_name,bound_at,pending{id,claimed,telegram_id,username,display_name,expires_at},binding_url (only fresh create). Bind UI requires explicit account confirmation and notification consent. No privileged user_id accepted.

## Acceptance

Security tests: expired/replayed/stolen/foreign challenge; claim before confirmation; cross-user/banned/deleted identity; tampered/replayed signatures; no group private read; unbind prevents further delivery; blocked/nonexistent request parity; forged owner commands; dispatch/sending restart without duplicate inference; forget and actor isolation; credential no-echo; finance receipts continue from both stores.

User tests: desktop/mobile binding create/claim/confirm/unbind, config guidance without binding, serious billing tone, private handoff and staff progress without model calls. Five real inference samples maximum0.1U reserved each after offline acceptance, separately accounted as support acceptance. No customer/real provider key in browser fixtures.

Rollback retains all call/cost evidence and new backward-compatible tables. Disable bridge access and receiver inference on failure; restore prior UI/services without resetting database snapshots. No blind main API restart or finance downgrade.
