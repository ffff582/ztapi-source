# Codex catalog sidecar

Independent standard-library-only Go module. It does not import, modify, or
restart the main API. No SQL, request logging, credential persistence, model
inference, hosted tools, generation endpoints, or client-supplied upstream URLs.

## Contract

- `GET /codex/models?client_version=0.165.0` requires one Bearer API key.
- Only exact version `0.165.0` is supported; alpha suffixes, missing/duplicate
  versions, additional query parameters, and bodies are rejected with 400.
- Every valid request forwards that key to the configured internal
  `GET /v1/models`. Existing main-API TokenAuth, group, model, and IP restrictions
  remain authoritative. No authentication or visibility results are cached.
- Only the exact ID `zt-claude-opus-5` can reveal the supplied ModelInfo entry.
  Success is exactly `{"models":[entry]}` or `{"models":[]}`. No other models
  are inferred or described.
- Upstream 401/403 remain sanitized 401/403. All other upstream errors,
  redirects, invalid JSON/envelopes, read errors, and oversize bodies yield 503.
  Error JSON contains a fixed error code, never upstream diagnostics.
- Unknown routes return 404; unsupported methods return 405 with `Allow: GET`.
  Routes are not redirected or path-normalized.
- Internal-only `GET /healthz` returns plain `healthy\n`, without authentication
  or any upstream call. It reports process health, not upstream readiness.
  Do not publish a public nginx health location.
- All handler responses have `Cache-Control: no-store` and `nosniff`.

## Environment

| Variable | Requirement |
| --- | --- |
| `UPSTREAM_MODELS_URL` | Required fixed internal HTTP(S) URL, exact `/v1/models`; no userinfo, query, fragment, encoded path, or redirects. |
| `MODEL_INFO_PATH` | Required read-only UTF-8 file containing one parent-validated ModelInfo JSON object. |
| `LISTEN_ADDR` | Default `:8080`; internal container listener, no host port publishing. |
| `TRUSTED_PROXY_CIDRS` | Comma-separated exact trusted proxy peers; empty trusts no forwarding headers. IPv4 `/32` or IPv6 `/128` only; broader and mapped prefixes are rejected. |

The service loads the metadata once before listening. It checks the exact slug
and nonempty `base_instructions`, but deliberately does not fabricate or validate
context windows, instructions, tools, or Codex schema completeness. The parent
owns CLI schema validation and the full official instructions. Unknown JSON
fields and supplied instructions are preserved verbatim. No production metadata
or shortened instructions are bundled; test fixtures are not deployment inputs.
Replacing the file requires restarting only this sidecar, never the main API.

## IP trust boundary

The socket peer is authoritative unless it matches `TRUSTED_PROXY_CIDRS`.
Untrusted peers' `X-Forwarded-For`, `X-Real-IP`, and `Forwarded` cannot override
that peer. For trusted peers, XFF takes precedence and is parsed right-to-left,
skipping only explicitly trusted proxy hops. Malformed chains fail closed rather
than falling back to another header. `X-Real-IP` is accepted only without XFF;
without either header the socket peer is used. IPv4-mapped addresses are unmapped
and all accepted addresses are canonicalized. Scoped, unspecified, multicast,
port-bearing, and overly long forwarded chains are rejected.

Outbound requests contain only the canonical single-IP XFF and X-Real-IP, Bearer
authorization, and JSON Accept header. Inbound cookies, proxy credentials, URL
overrides, forwarding chains, and other client headers are never forwarded.
Environment HTTP(S) proxies and compressed upstream responses are disabled.

Deployment must ensure nginx overwrites forwarding headers from the actual
client (or has its own correctly pinned trusted proxy configuration). The main
API must recognize the sidecar's canonical forwarding headers under its existing
trusted-proxy policy. Forwarding correct headers alone does not prove its IP
allowlist is enforced; the parent must verify the real allowed/denied IP path.
Do not add a broadly trusted subnet or a new main-API authentication bypass.

## Bounds

Metadata and authorization JSON reject duplicate or case-aliased field names,
trailing documents, and nesting deeper than 128. Authority fields use exact
canonical names (`data`, `id`, `slug`, `base_instructions`) rather than Go's
case-insensitive struct-field matching.

32 concurrent catalog handlers/upstream reads, no waiting queue; overflow is
503 with `Retry-After: 1`. Health is local and independent of upstream slots.
Upstream total timeout is 5 seconds, response body and metadata are each at most
1 MiB, upstream response headers are at most 16 KiB. Inbound header limit is
16 KiB, header/read/write/idle timeouts are 3/5/7/30 seconds. Bearer header and
query lengths are bounded. Response bodies are closed on every upstream path.
SIGINT/SIGTERM drains for up to 5 seconds; the process performs no runtime writes.

## Offline verification

From this module with Go 1.25 available locally:

```sh
GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off go test -count=1 ./...
GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off go vet ./...
```

Tests use `httptest.NewRecorder` and an in-memory RoundTripper. They do not bind
listeners, perform DNS lookups, send network requests, or use real API keys.

## Container handoff

The Dockerfile produces a static Linux binary in a scratch image with a CA
bundle and numeric nonroot user `65532:65532`. The parent builds and deploys it.
Run with a read-only root filesystem, no host ports, internal networking only,
all capabilities dropped, and `no-new-privileges`. Mount the externally validated
ModelInfo file read-only and readable by UID 65532. No writable volume or tmpfs
is required. Runtime read-only networking and mount policies belong to the
parent's orchestrator, not to the Dockerfile.

Only route the new catalog location to this sidecar, retaining Bearer auth and
canonical client IP forwarding. Keep main/finance services running unchanged.
Codex configuration uses `model_catalog_url`; this module neither disables
features nor installs `model_catalog_json` or edits client profiles.
