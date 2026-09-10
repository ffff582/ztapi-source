FROM node:24-alpine AS build

RUN corepack enable
WORKDIR /workspace

COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY web/console/package.json web/console/package.json
RUN pnpm install --frozen-lockfile --filter @ztapi/console...

COPY web/console web/console
RUN pnpm --filter @ztapi/console build

FROM nginx:1.29-alpine
COPY --from=build /workspace/web/console/dist/ /usr/share/nginx/html/
