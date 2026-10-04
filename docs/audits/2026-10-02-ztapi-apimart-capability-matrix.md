# ZTAPI APIMart-style capability audit

Date: 2026-10-02

Scope: the local Native implementation after Tasks 1-4. This is a capability
and contract audit, not a claim that ZTAPI has copied APIMart's backend. The
website may borrow information architecture and interaction patterns; model
availability, billing, provider routing and balances remain ZTAPI-owned.

## Decision summary

- The public site and authenticated console can use the existing contracts.
- No backend endpoint, automatic fallback, provider selection, balance
  mutation, model publication rule or pricing rule is required for the UI
  upgrade.
- The documentation manifest was incomplete relative to the routes registered
  by the server. It now lists the verified native, OpenAI-compatible, media,
  audio, rerank and Gemini entry points. This is a documentation correction,
  not a relay behavior change.
- Model availability must still be read from the live pricing/catalog response;
  the endpoint list is not a promise that every model supports every route.
- Health-worker and provider controls remain admin-only. They must not be
  exposed as public marketing claims or used by the frontend to auto-publish,
  auto-unpublish or silently switch a customer's model.

## Capability matrix

| APIMart-style capability | ZTAPI evidence | Contract / permission boundary | Status and risk |
| --- | --- | --- | --- |
| Public model directory and pricing | `GET /api/pricing`, `GET /api/status`; `web/console/src/features/models/ModelsPage.tsx` | Header-nav pricing gate; prices come from runtime `PricingEnvelope` and its billing dimensions | **Verified.** Do not hard-code counts or treat the catalog as a provider inventory. |
| Authenticated model catalog | `GET /api/user/models`; `SupportedModelsPage.tsx` | ZTAPI user session; `UserModelCatalogEnvelope` | **Verified.** Route, modality and pricing are model-specific. |
| OpenAI-compatible model discovery | `GET /v1/models`, `GET /v1/models/:model` in `relay-router.go` | API token authentication; protocol-sensitive output for OpenAI, Anthropic and Gemini credentials | **Verified.** Conflicting credentials are rejected by existing router tests. |
| API key lifecycle | `/api/token/` GET/POST/PUT/DELETE; `KeysPage.tsx` | Authenticated user; plaintext key is returned only at creation and is not persisted in browser state | **Verified.** No new key or secret flow introduced. |
| Usage visibility | `/api/log/self`, `/api/log/self/stat`; `LogsPage.tsx`, `DashboardPage.tsx` | Authenticated ZTAPI user; response includes model, billed amount, token dimensions, status, latency and request ID | **Verified.** No invented cumulative spend metric; `rpm/tpm` is not a lifetime total. |
| Balance and recharge | `/api/user/self`, `/api/status`, `/api/user/topup/info`, `/api/user/topup/usdt-trc20/orders*` | Authenticated user for balance/order; settlement stays server-side | **Verified.** Website changes do not alter accounting or settlement. |
| Public API documentation | `web/console/src/features/docs/docs-manifest.json`, generated `public/llms.txt` | Static public pages; examples use `https://ztapi.vip/v1` and bearer auth | **Verified after correction.** Endpoint presence does not imply universal model support. |
| OpenAI chat/completions and Responses | `POST /v1/chat/completions`, `POST /v1/completions`, `POST /v1/responses`, `POST /v1/responses/compact` | Token auth, system performance check, model request rate limit, distribution and health pin | **Verified.** Responses-only models must remain model-catalog driven. |
| Anthropic native messages | `POST /v1/messages` | Token auth and relay distribution; Anthropic protocol selected by route | **Verified.** The docs now expose the route; no claim that all Claude models are available. |
| Gemini native / compatible calls | `POST /v1beta/models/*path`, `POST /v1/models/*path`, `GET /v1beta/models` | Token auth; Gemini route is selected by path/credential protocol | **Verified.** Exact model/action support remains upstream/model metadata dependent. |
| Images, embeddings, audio and rerank | `POST /v1/images/generations`, `/v1/images/edits`, `/v1/embeddings`, `/v1/audio/transcriptions`, `/v1/audio/translations`, `/v1/audio/speech`, `/v1/rerank` | Token auth and endpoint-specific relay format | **Verified as routes.** Billing and supported input/output dimensions remain catalog-specific. |
| Video task lifecycle | `POST/GET /v1/video/generations`, `POST/GET /v1/videos`, `GET /v1/videos/:task_id/content` | Token auth; content proxy additionally accepts a user session | **Verified as routes.** A task route is not evidence that a given video model is currently published. |
| Admin model publication and health | `/api/models/ztapi/*`, `/api/channel/*` | Model/channel permissions; health worker and recovery are admin-only | **Verified.** Must never be called by public pages or triggered by a customer's malformed request. |
| Admin finance and reconciliation | `/api/admin/request-logs*`, `/api/admin/request-settlements*`, `/api/admin/attempt-billing/*`, `/api/admin/supplier-reconciliation/*` | Finance/log/audit permissions | **Verified.** Website redesign must not change financial state or infer supplier cost from a public price. |
| Auto fallback / silent model replacement | No public or console contract added | No approved contract; existing distribution is server-owned | **Intentionally absent.** Adding it would change billing and user-visible model semantics. |

## Verified endpoint index

The public docs index is generated from the same manifest used by the visible
docs page. The current list is:

```text
GET  /v1/models
POST /v1/chat/completions
POST /v1/completions
POST /v1/responses
POST /v1/responses/compact
POST /v1/messages
POST /v1/embeddings
POST /v1/images/generations
POST /v1/images/edits
POST /v1/audio/transcriptions
POST /v1/audio/translations
POST /v1/audio/speech
POST /v1/rerank
POST /v1/video/generations
GET  /v1/video/generations/{task_id}
POST /v1/videos
GET  /v1/videos/{task_id}
GET  /v1/videos/{task_id}/content
POST /v1beta/models/{model}:generateContent
```

These are route-level facts. Before a customer integrates, the model support
page remains the source of truth for model ID, protocol, modality, endpoint,
input/output dimensions and live price.

## Not copied from APIMart

The following would require a separate backend product decision and are out of
scope for this release: provider account abstraction, supplier-specific
settlement APIs, automatic cross-provider failover, customer-facing provider
selection, plan/subscription billing, or any APIMart private implementation.
Adding any of these only because they appear in a reference site would be an
unsupported assumption and could alter billing or routing behavior.
