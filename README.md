# ZTAPI

ZTAPI is an API gateway and billing platform for supported AI model providers.
This repository is the corresponding source for the production release recorded
in [SOURCE-OFFER.md](SOURCE-OFFER.md).

ZTAPI is based on [New API](https://github.com/QuantumNous/new-api). Frontend
design and development by New API contributors. ZTAPI contains modifications;
see [MODIFICATIONS.md](MODIFICATIONS.md).

## Build

Requirements:

- Go 1.25.1
- Node.js 24
- pnpm 10.13.1
- Bun 1.3.14
- Docker with Compose v2 for the production container build

```sh
corepack pnpm install --frozen-lockfile
corepack pnpm --filter @ztapi/console build

(cd server/web && bun install --frozen-lockfile --filter react-template)
(cd server/web/classic && bun run build)

(cd server && \
  mkdir -p web/default/dist web/classic/dist && \
  printf '<!doctype html><title>ZTAPI</title>' > web/default/dist/index.html && \
  cp web/default/dist/index.html web/classic/dist/index.html && \
  go test ./... && \
  go build ./...)
```

The production images use the same build steps and do not require generated
frontend files to be committed:

```sh
RELEASE_COMMIT="$(node -p "require('./PUBLIC-SOURCE-MANIFEST.json').release_commit")"
docker build -f server/Dockerfile.ztapi -t ztapi-server:source server
docker build \
  --build-arg "ZTAPI_RELEASE_VERSION=$RELEASE_COMMIT" \
  -f deploy/nginx/Dockerfile \
  -t ztapi-nginx:source \
  .
```

The production container definitions and non-secret configuration template are
under `deploy/`. No production credentials or customer data are included.

## License

This source is provided under GNU AGPLv3. Preserve the notices in `NOTICE` and
the third-party terms in `THIRD-PARTY-LICENSES.md`.
