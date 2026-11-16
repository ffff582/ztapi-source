# ZTAPI Development and Deployment

## Local development

Create the local environment file and start the development topology:

```powershell
Copy-Item .env.example .env
docker compose -f deploy/docker/docker-compose.dev.yml up -d --build
corepack pnpm --filter @ztapi/console dev
```

The containerized console is available at `http://localhost:8080`. Running the
last command starts the console directly on the host when frontend hot reload
outside Docker is preferred.

View service logs:

```powershell
docker compose -f deploy/docker/docker-compose.dev.yml logs -f
docker compose -f deploy/docker/docker-compose.dev.yml logs -f server console
```

Reset local databases and cached state only when local data can be discarded:

```powershell
docker compose -f deploy/docker/docker-compose.dev.yml down -v
```

## Tests

Install locked dependencies and run the configuration and container tests:

```powershell
corepack pnpm install --frozen-lockfile
corepack pnpm test:contracts
node tests/runtime/create-test-certificate.mjs
corepack pnpm test:runtime
```

The runtime suite requires a running Docker Engine. It starts real containers,
uses short-lived test certificates under `test-results/tls`, and removes its
Compose projects and named volumes when each run finishes.

## Production TLS and environment

Create these DNS records before deployment:

```text
ztapi.vip      A    <production-server-public-ip>
www.ztapi.vip  A    <production-server-public-ip>
admin.ztapi.vip A    <production-server-public-ip>
```

Allow inbound TCP ports `80` and `443` in the host firewall and provider
security group. Do not expose `3000`, `3306`, or `6379`.

Prepare a host directory containing the certificate files expected by Nginx:

```text
fullchain.pem
privkey.pem
```

Set `ZTAPI_TLS_DIR` to the absolute path of that directory. Production also
requires `MYSQL_DATABASE`, `MYSQL_USER`, `MYSQL_PASSWORD`,
`MYSQL_ROOT_PASSWORD`, `REDIS_PASSWORD`, `ZTAPI_SESSION_SIGNING_KEY`,
`CRYPTO_SECRET`, and `ZTAPI_TRUSTED_PROXY_CIDRS`. No production password has a
fallback. `ZTAPI_TRUSTED_PROXY_CIDRS` must be the explicit private Nginx
application-network range, `172.30.0.0/24`, unless the Compose network is
deliberately changed at the same time.

The public site origin is `https://ztapi.vip`, the OpenAI-compatible API base
URL is `https://ztapi.vip/v1`, and the administration origin is
`https://admin.ztapi.vip`. The TLS certificate must include all three hostnames.
The administration host serves a separate static bundle, proxies only `/api/*`,
and returns `X-Robots-Tag: noindex, nofollow`.

Validate and start production:

```powershell
$env:ZTAPI_TLS_DIR = 'C:\absolute\path\to\tls'
docker compose -f deploy/docker/docker-compose.prod.yml config
docker compose -f deploy/docker/docker-compose.prod.yml up -d --build
docker compose -f deploy/docker/docker-compose.prod.yml logs -f nginx server
```

Only Nginx publishes host ports `80` and `443`. The Go server, MySQL, and Redis
ports remain private on Docker networks. The TLS directory is mounted read-only
at `/etc/nginx/tls`.

## Certificate renewal

Use the host's ACME client to renew `fullchain.pem` and `privkey.pem` in the
directory referenced by `ZTAPI_TLS_DIR`. After renewal, validate and reload
Nginx without restarting the stateful services:

```powershell
docker compose -f deploy/docker/docker-compose.prod.yml exec -T nginx nginx -t
docker compose -f deploy/docker/docker-compose.prod.yml exec -T nginx nginx -s reload
```

Schedule renewal checks at least daily and alert on a failed renewal or a
certificate with fewer than 14 days remaining.

## Backup and restore

The production state lives in the `mysql_data`, `redis_data`, and `ztapi_data`
named volumes. The database is authoritative; Redis is cache and queue state,
and `/data` contains application-owned files.

Create a consistent MySQL backup before upgrades:

```powershell
docker compose -f deploy/docker/docker-compose.prod.yml exec -T mysql `
  sh -c 'exec mysqldump --single-transaction -u root -p"$MYSQL_ROOT_PASSWORD" "$MYSQL_DATABASE"' `
  > ztapi-mysql.sql
docker compose -f deploy/docker/docker-compose.prod.yml cp server:/data ./ztapi-data
```

Restore into stopped application services, then start and verify
`https://ztapi.vip/api/status`:

```powershell
docker compose -f deploy/docker/docker-compose.prod.yml stop nginx server
Get-Content -Raw .\ztapi-mysql.sql | docker compose -f deploy/docker/docker-compose.prod.yml exec -T mysql `
  sh -c 'exec mysql -u root -p"$MYSQL_ROOT_PASSWORD" "$MYSQL_DATABASE"'
docker compose -f deploy/docker/docker-compose.prod.yml cp ./ztapi-data/. server:/data
docker compose -f deploy/docker/docker-compose.prod.yml start server nginx
```

Store encrypted backups outside the server and test a restore on a separate
Compose project before relying on the procedure.
