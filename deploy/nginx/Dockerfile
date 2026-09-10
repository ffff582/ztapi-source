FROM node:24-alpine AS public-build

RUN corepack enable
WORKDIR /workspace

COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY web/console/package.json web/console/package.json
RUN pnpm install --frozen-lockfile --filter @ztapi/console...

COPY web/console web/console
RUN pnpm --filter @ztapi/console build

FROM oven/bun:1.3.14-alpine AS admin-build

WORKDIR /workspace/server/web

COPY server/web/package.json server/web/bun.lock ./
COPY server/web/classic/package.json classic/package.json
COPY server/web/default/package.json default/package.json
RUN bun install --frozen-lockfile --filter react-template

COPY server/web/classic classic
WORKDIR /workspace/server/web/classic
ARG VITE_REACT_APP_VERSION
RUN VITE_ZTAPI_ADMIN_APP=true \
    VITE_REACT_APP_VERSION="${VITE_REACT_APP_VERSION:-ztapi}" \
    bun run build \
    && sed -i 's|<title>New API</title>|<title>ZTAPI 管理后台</title>|' dist/index.html \
    && grep -Fq 'ZTAPI 管理后台' dist/index.html

FROM nginx:1.29-alpine
ARG ZTAPI_RELEASE_VERSION
COPY --from=public-build /workspace/web/console/dist/ /usr/share/nginx/public/
COPY --from=admin-build /workspace/server/web/classic/dist/ /usr/share/nginx/admin/
COPY deploy/nginx/ztapi.conf /etc/nginx/conf.d/default.conf
COPY server/LICENSE server/NOTICE server/THIRD-PARTY-LICENSES.md /licenses/
RUN test "${#ZTAPI_RELEASE_VERSION}" -eq 40 \
    && case "$ZTAPI_RELEASE_VERSION" in *[!0-9a-f]*) exit 1 ;; esac \
    && sed -i "s|__ZTAPI_RELEASE_COMMIT__|${ZTAPI_RELEASE_VERSION}|g" /etc/nginx/conf.d/default.conf \
    && ! grep -Fq '__ZTAPI_RELEASE_COMMIT__' /etc/nginx/conf.d/default.conf
