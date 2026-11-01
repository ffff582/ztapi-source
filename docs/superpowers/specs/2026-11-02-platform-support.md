# Platform Support

## Approved Requirements

Deliver authenticated platform AI support using the existing authorized DeepSeek
Flash upstream, own-request diagnosis, human handoff and automatic finance costs.
No main API restart, automated refunds, wallet updates or model-route changes.
The owner explicitly removed daily caps on 2026-11-02; retain
short-window abuse limits, bounded messages and concurrency. Paid acceptance is
bounded at 0.5U and recorded in finance. No paid calls before zero-cost tests pass.

The owner subsequently authorized reuse of @ztapi_bot for the public group
https://t.me/ztapivip (verified group -1004401851162). Channel/operational alerts
must remain private to the existing owner recipient; group answers must never
expose them. The owner verified bot administrator status and sent /support_on.
Publish the public group link on the website and platform support entry.

## Architecture

Use a separate Go support service and private SQLite database; a SELECT-only
MySQL reader supplies identity and owner-scoped request evidence. Existing JWT
sessions are validated, including current account status. Frontend static releases
and a graceful Nginx route change publish it without restarting the model API.
The financial sidecar reads the support database read-only; no fake customer
settlement and no customer wallet funding are needed for support calls.

An authenticated chat message has exactly one idempotent provider call. Its
admission persists before dispatch; an append-only receipt persists before the
reply. Missing receipts are unconfirmed, never zero-cost. No automatic retries.
Client disconnect does not discard an already-dispatched call's usage receipt.
Images are optional, validated, private and enabled only after real vision
acceptance. Uploaded screenshots must be available to the owning user and assigned
platform staff, not a public object URL. Private image storage is bounded at 50MB
per user and 512MB in total; text/human support remains usable when storage is full.
No arbitrary URL fetching or model-controlled admin tools.

Stable public business rules are curated with versioned documentation links;
current models/prices are retrieved from the platform's existing public pricing
projection with the caller's session. Never expose purchase prices, provider keys,
customer prompts/code, other users' requests, or internal raw failure bodies.
No invented promotions, refund decisions, timelines, model availability or prices.

## HTTP Contract

All JSON responses use `{success:true,data:...}` or a stable error `message` and
appropriate non-2xx status. Dates are ISO-8601 strings. Unauthorized object access
and unknown IDs both return 404. Mutations require exact same Origin and JSON.

- GET `/api/support/config`: `{enabled,model_label,vision_enabled,max_message_chars,max_image_bytes,knowledge_version}`.
- GET `/api/support/conversations`: `{items:Conversation[]}` (latest 30).
- POST `/api/support/conversations`: `{idempotency_key}` -> Conversation.
- GET `/api/support/conversations/:id`: `{conversation,messages:Message[],ticket?:Ticket}`.
- POST `/api/support/conversations/:id/messages`: `{content,request_id?,image?:{mime_type,data_base64},idempotency_key}` -> `{message,reply,call_id}`. Pending replay may return 202 `{pending:true,call_id}`; poll the conversation, never resend with another key automatically.
- GET `/api/support/requests`: `{items:Diagnosis[]}` (latest 20, 30 days).
- GET `/api/support/diagnostics/:request_id`: Diagnosis.
- GET `/api/support/attachments/:id`: owner-only `{mime_type,data_base64}`.
- POST `/api/support/tickets`: `{conversation_id,request_id?,subject,content,contact?,idempotency_key}` -> Ticket.
- GET `/api/admin/support/tickets?status=open|in_progress|resolved|all&after_id=0&limit=20`: `{items:Ticket[],next_after_id:number}`.
- GET `/api/admin/support/tickets/:id`: `{ticket,conversation,messages,diagnosis?:Diagnosis}`.
- GET `/api/admin/support/attachments/:id`: staff-only `{mime_type,data_base64}`.
- POST `/api/admin/support/tickets/:id/replies`: `{content,idempotency_key}` -> `{ticket,message}`.
- POST `/api/admin/support/tickets/:id/status`: `{status,version,idempotency_key}` -> Ticket; optimistic version conflicts return 409.

Conversation: `{id:string,title:string,created_at:string,updated_at:string}`.
Message: `{id:number,role:'user'|'assistant'|'human'|'system',content:string,created_at:string,has_image:boolean,attachment_id?:number}`.
Diagnosis: `{request_id,model,created_at,outcome,code,summary,http_status:number,stream:boolean,dispatched:boolean,usage_received:boolean,charged_u:string,reserved_u:string,needs_human:boolean}`.
Ticket: `{id:number,conversation_id:string,subject:string,content:string,contact:string,request_id:string,status:string,version:number,created_at:string,updated_at:string}`. User ID may be present only in the admin projection.

Human tickets are durably stored and visibly acknowledged, not falsely presented
as live human chat. Admin roles 2/10/100 (support/admin/root) can manage tickets;
finance-only roles cannot. Replies appear in the owning customer's conversation.
While an open/in_progress human ticket exists, subsequent customer messages are
persisted for staff and acknowledged without an AI/upstream call. Resolved tickets
permit AI support again. No business action is implied by closing a ticket.

## Finance Contract

`support.FinancialUsage` is the shared read-only call/receipt projection. A call
without a complete upstream usage receipt is `unconfirmed`; a quotation-derived
cash amount is `estimate`, not an actual upstream debit. Input is inclusive of
cache-read and is normalized once for pricing. Xcloud group-adjusted credit rates
and the confirmed 100-for-200 recharge conversion are separately frozen.

Finance consumes `support.ReadFinancialUsage(ctx, db, start, end)`. Support DB
unavailability must mark costs/profit incomplete without making all finance
endpoints return 503. Automated support costs are not legacy management-test facts.
Monthly supplier invoices covering the same channel include support costs once;
otherwise estimates remain explicit and reduce estimated profit, not confirmed
profit. No previously confirmed facts or balances are rewritten.

Both site support and Telegram support use separate private stores. Telegram
sender IDs are never platform user identities, never authorize account reads and
never become customer wallet settlements. Finance reads both stores read-only,
once each; either unavailable store makes support cost/profit incomplete.

## Telegram Contract

Use one independent long-poll worker for the existing bot, with persistent inbox,
offset, owner-enabled group whitelist and delivery outbox. Never overwrite an
existing webhook or compete with another receiver. No main DB or session signing
credential is mounted into the Telegram worker. Its knowledge uses anonymous
public pricing, never the owner's account context. Preserve existing private
platform alerts without changing the main API runtime.

An explicit human @mention or reply in an enabled group must produce a useful
answer or a clear refusal/failure/busy response, not intentional silence. Images
are bounded, validated and fetched only through Telegram's fixed API/file origin;
no arbitrary URLs. Friendly small talk is allowed. Unmentioned participation is
low-frequency, limited to fresh human messages when the bot can read them, and
must not cause chatter loops or replay old conversations.

Owner commands enable/disable groups; ordinary users cannot change that policy.
Ignore bot messages. Conversations are isolated by group, sender and topic.
Sensitive credentials, another person's records and internal operational details
are refused with safe alternatives. Group identity does not grant access to
personal diagnosis; direct the user to authenticated platform support.

Persist updates before acknowledging offsets and persist replies before delivery.
Provider inference is never automatically retried after dispatch. Telegram retries
only the saved outgoing reply and respects 429 retry_after. Delivery may be delayed
by Telegram/network outages; do not promise impossible instantaneous delivery.

## Acceptance

Cover cross-user access, revoked/disabled login, CSRF, secret redaction, prompt
injection, duplicate/replayed calls, crash/disconnect, concurrency/rate limits,
empty replies, absent/conflicting usage, cache non-overlap, no auto retries, ticket
reply/status conflicts, finance outage/de-duplication and browser mobile/desktop.
Mock tests cannot prove vision; real paid text/vision acceptance must be recorded
with cost reservations and post-deploy checks. Main API container ID, start time,
wallet and route fingerprints must remain unchanged through deployment.
