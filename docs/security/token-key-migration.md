# ZTAPI User API-Key Migration

ZTAPI user API keys are one-time credentials. Plaintext is returned only by
the successful token-creation response and is never persisted, cached, or
returned by later API calls.

## Persistence Contract

- Public keys use `sk-zt-` followed by 32 random bytes encoded with unpadded
  base64url.
- `tokens.key_hash` stores lowercase SHA-256 hex of the complete plaintext key.
- `tokens.key_prefix` stores the first 12 plaintext characters for display.
- The token model has no persistent plaintext `key` field.
- `key_hash` is excluded from JSON serialization. List, search, detail, and
  update responses expose only `key_prefix`, which may be rendered as
  `<prefix>...`.

The token-creation response is the only response containing the full key:

```json
{
  "success": true,
  "data": {
    "id": 123,
    "key": "sk-zt-...",
    "key_prefix": "sk-zt-xxxxx"
  }
}
```

Registration never creates an API key. Users create credentials explicitly
after registration so the plaintext can be shown in the successful creation
flow. The legacy `GENERATE_DEFAULT_TOKEN` setting does not issue a token.

## Pre-Launch Brand Cutover

Credentials with the temporary pre-launch `sk-gan-` prefix are invalid. ZTAPI
does not generate, parse, authenticate, migrate, or otherwise accept them.
Users must create a new canonical `sk-zt-` credential; there is no legacy
compatibility mode.

## Breaking Legacy Migration

ZTAPI has no production users, so imported pre-ZTAPI plaintext tokens are
invalidated instead of converted into usable ZTAPI credentials.

Before `Token` AutoMigrate runs, both normal and fast migration paths:

1. Add `key_hash` and `key_prefix` when the legacy `tokens.key` column exists.
2. Disable every row with a non-empty legacy plaintext key.
3. Set `key_hash` to SHA-256 of
   `ztapi-invalid-legacy-token:<row-id>`. This deterministic,
   domain-separated placeholder is unique per row and does not depend on the
   legacy plaintext.
4. Set `key_prefix` to an empty string.
5. Keep the legacy `key` column by default. Drop it only during an explicitly
   approved second-phase deployment.

Legacy-row discovery uses GORM column expressions so the reserved `key`
identifier is quoted by the active SQLite, MySQL, or PostgreSQL dialector.
The invalidation runs in bounded batch updates and excludes
`ztapi-invalid-legacy-token:%` placeholders on later starts. The operation is
idempotent and supports SQLite, MySQL 5.7.8+, and PostgreSQL 9.6+. All
pre-ZTAPI plaintext tokens are disabled and must be reissued.

## Two-Phase Production Procedure

### Phase 1: Invalidate and verify

1. Take and verify a database backup.
2. Deploy with `ZTAPI_DROP_LEGACY_TOKEN_KEY=false` (the workflow default).
3. Verify every migrated legacy token is disabled, `key_hash` is populated,
   `key_prefix` is empty, and `key` contains only the
   `ztapi-invalid-legacy-token:<id>` placeholder.
4. Confirm newly created credentials authenticate through `key_hash` and that
   no application query still reads plaintext `tokens.key`.

Do not continue when any row retains an original plaintext credential or when
the backup cannot be restored in a staging environment.

### Phase 2: Remove the legacy column

1. Start a maintenance deployment from the production workflow.
2. Select `drop_legacy_token_key=true` only after Phase 1 evidence is approved.
3. Verify the migration drops `tokens.key`, then run token creation,
   authentication, quota settlement, and refund smoke tests.
4. Return the workflow input to its default `false` for later deployments. The
   setting is harmless after the column is gone, but keeping it false prevents
   accidental destructive behavior in restored older databases.

The deployment workflow writes the selected value to `/opt/ztapi/.env`; manual
server edits are intentionally not required and would be overwritten by the
next workflow run.

## Migrated Call Sites

- **Key generation:** explicit token creation uses `GenerateZTAPIKey` and
  persists only its hash and display prefix. Registration does not create a
  default credential.
- **Lookup:** `ValidateUserToken` hashes the complete presented credential once;
  internal reads use `GetTokenByHash` explicitly.
- **Cache:** Redis identifiers are HMACs derived from `KeyHash`; cached token
  payloads clear `KeyHash` and never reconstruct plaintext.
- **Quota and billing:** relay state, websocket pre-consume, settlement,
  refunds, task billing, and quota adjustments pass `TokenKeyHash`.
- **Middleware:** authentication keeps the complete `sk-zt-` credential for
  validation and writes only `token_key_hash` to Gin context.
- **Serialization:** token list, search, detail, and update responses contain a
  display prefix or masked prefix and cannot serialize `key_hash`.
- **Cache invalidation:** single delete, batch delete, user invalidation, and
  quota cache updates identify tokens by `KeyHash`.
- **Routes:** `POST /api/token/:id/key` and
  `POST /api/token/batch/keys` are no longer registered.
- **Classic console:** successful creation captures `data.key` and displays it
  immediately in a one-time save dialog that ignores mask clicks and Escape.
  Token rows show only `key_prefix`; reveal, copy-existing-key, and batch-key
  retrieval controls are removed.
- **Tests and fixtures:** controller, middleware, service, cache, and migration
  fixtures store hash/prefix values instead of plaintext token keys.

Restriction enforcement and trusted-proxy policy are handled by Task 3.2 and
are not part of this migration.
