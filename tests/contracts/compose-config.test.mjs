import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import YAML from 'yaml';

const productionComposePath = 'deploy/docker/docker-compose.prod.yml';
const productionEnvExamplePath = 'deploy/docker/.env.example';
const developmentComposePath = 'deploy/docker/docker-compose.dev.yml';
const nginxConfigPath = 'deploy/nginx/ztapi.conf';
const nginxDockerfilePath = 'deploy/nginx/Dockerfile';
const dockerignorePath = '.dockerignore';
const viteConfigPath = 'web/console/vite.config.ts';

function readProductionCompose() {
  return YAML.parse(readFileSync(productionComposePath, 'utf8'));
}

test('production exposes only public TLS and a loopback acceptance listener', () => {
  const compose = readProductionCompose();

  assert.deepEqual(compose.services.nginx.ports, [
    '80:80',
    '443:443',
    '127.0.0.1:18081:8081',
  ]);
  assert.equal(compose.services.server.ports, undefined);
  assert.equal(compose.services.mysql.ports, undefined);
  assert.equal(compose.services.redis.ports, undefined);
});

test('production server has outbound access without exposing stateful services', () => {
  const compose = readProductionCompose();

  assert.equal(compose.networks.egress?.internal, undefined);
  assert.ok(compose.services.server.networks.includes('egress'));
  assert.ok(!compose.services.nginx.networks.includes('egress'));
  assert.ok(!compose.services.mysql.networks.includes('egress'));
  assert.ok(!compose.services.redis.networks.includes('egress'));
});

test('development console proxies API and relay routes to the server', () => {
  const compose = YAML.parse(readFileSync(developmentComposePath, 'utf8'));
  const vite = readFileSync(viteConfigPath, 'utf8');

  assert.equal(compose.services.console.environment.ZTAPI_API_ORIGIN, 'http://server:3000');
  assert.match(vite, /loadEnv/);
  assert.match(vite, /ZTAPI_API_ORIGIN/);
  for (const route of ['/api', '/v1', '/v1beta']) {
    assert.match(vite, new RegExp(`['"]${route}['"]`));
  }
});

test('stateful production services use named volumes', () => {
  const compose = readProductionCompose();

  assert.ok(compose.volumes.mysql_data);
  assert.ok(compose.volumes.redis_data);
  assert.ok(compose.volumes.ztapi_data);
  assert.ok(compose.services.mysql.volumes.some((value) => value.startsWith('mysql_data:')));
  assert.ok(compose.services.redis.volumes.some((value) => value.startsWith('redis_data:')));
  assert.ok(compose.services.server.volumes.some((value) => value.startsWith('ztapi_data:')));
});

test('production mounts required TLS material read-only', () => {
  const source = readFileSync(productionComposePath, 'utf8');
  const compose = readProductionCompose();

  assert.match(source, /\$\{ZTAPI_TLS_DIR:\?set ZTAPI_TLS_DIR\}/);
  assert.ok(
    compose.services.nginx.volumes.some(
      (value) => value.endsWith(':/etc/nginx/tls:ro'),
    ),
  );
});

test('nginx terminates TLS with exact certificate paths and redirects HTTP', () => {
  const nginx = readFileSync(nginxConfigPath, 'utf8');

  assert.match(nginx, /listen 443 ssl;/);
  assert.match(nginx, /ssl_certificate\s+\/etc\/nginx\/tls\/fullchain\.pem;/);
  assert.match(nginx, /ssl_certificate_key\s+\/etc\/nginx\/tls\/privkey\.pem;/);
  assert.match(nginx, /return 308 https:\/\/\$host\$request_uri;/);
});

test('nginx proxies exact relay roots and returns the server billing request ID', () => {
  const nginx = readFileSync(nginxConfigPath, 'utf8');

  assert.match(nginx, /location = \/v1\s*\{/);
  assert.match(nginx, /location = \/v1beta\s*\{/);
  assert.doesNotMatch(nginx, /map \$http_x_request_id/);
  assert.match(
    nginx,
    /map \$upstream_http_x_request_id \$ztapi_response_request_id/,
  );
  assert.match(nginx, /default \$upstream_http_x_request_id;/);
  assert.match(nginx, /"" \$request_id;/);
  assert.match(
    nginx,
    /add_header X-Request-ID \$ztapi_response_request_id always;/,
  );
  assert.match(
    nginx,
    /add_header X-Oneapi-Request-Id \$ztapi_response_request_id always;/,
  );
  assert.match(nginx, /proxy_set_header X-Request-ID \$request_id;/);
  assert.match(nginx, /proxy_set_header X-Oneapi-Request-Id \$request_id;/);
});

test('public nginx proxies customer APIs while preserving isolated acceptance access', () => {
  const nginx = readFileSync(nginxConfigPath, 'utf8');
  const publicStart = nginx.indexOf('server_name ztapi.vip www.ztapi.vip;');
  const publicEnd = nginx.indexOf('server_name admin.ztapi.vip;', publicStart);
  const publicServer = nginx.slice(publicStart, publicEnd);

  assert.ok(publicStart >= 0 && publicEnd > publicStart);
  assert.match(publicServer, /location = \/api\/status\s*\{[\s\S]*?proxy_pass http:\/\/server:3000;/);
  assert.doesNotMatch(publicServer, /return 503;/);
  assert.match(publicServer, /location \/api\/\s*\{[\s\S]*?proxy_pass http:\/\/server:3000;/);
  for (const route of ['v1', 'v1beta']) {
    assert.match(
      publicServer,
      new RegExp(`location = \\/${route}\\s*\\{[\\s\\S]*?proxy_pass http:\\/\\/server:3000;`),
    );
    assert.match(
      publicServer,
      new RegExp(`location \\/${route}\\/\\s*\\{[\\s\\S]*?proxy_pass http:\\/\\/server:3000;`),
    );
  }

  assert.match(nginx, /listen 8081;/);
  assert.match(nginx, /server_name acceptance\.ztapi\.internal;/);
  assert.match(nginx, /location \/api\/\s*\{[\s\S]*?proxy_pass http:\/\/server:3000;/);
  assert.match(nginx, /location \/v1\/\s*\{[\s\S]*?proxy_pass http:\/\/server:3000;/);
});

test('only the loopback acceptance listener marks internal validation traffic', () => {
  const nginx = readFileSync(nginxConfigPath, 'utf8');
  const publicStart = nginx.indexOf('server_name ztapi.vip www.ztapi.vip;');
  const acceptanceStart = nginx.indexOf('server_name acceptance.ztapi.internal;');
  const publicEnd = acceptanceStart;
  const acceptanceEnd = nginx.indexOf('\nserver {', acceptanceStart);
  const publicServer = nginx.slice(publicStart, publicEnd);
  const acceptanceServer = nginx.slice(acceptanceStart, acceptanceEnd);

  assert.doesNotMatch(publicServer, /X-ZTAPI-Internal-Acceptance\s+"1"/);
  for (const route of ['/api/', '= /v1', '/v1/', '= /v1beta', '/v1beta/']) {
    const escaped = route.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    assert.match(
      publicServer,
      new RegExp(`location ${escaped}\\s*\\{[\\s\\S]*?proxy_set_header X-ZTAPI-Internal-Acceptance "";`),
    );
    assert.match(
      acceptanceServer,
      new RegExp(`location ${escaped}\\s*\\{[\\s\\S]*?proxy_set_header X-ZTAPI-Internal-Acceptance "1";`),
    );
  }
});

test('public image retains license and notice files', () => {
  const dockerfile = readFileSync(nginxDockerfilePath, 'utf8');
  const dockerignore = readFileSync(dockerignorePath, 'utf8');

  assert.match(dockerfile, /COPY server\/LICENSE server\/NOTICE server\/THIRD-PARTY-LICENSES\.md \/licenses\//);
  assert.doesNotMatch(dockerignore, /^server$/m);
  assert.match(dockerignore, /!server\/LICENSE/);
  assert.match(dockerignore, /!server\/NOTICE/);
  assert.match(dockerignore, /!server\/THIRD-PARTY-LICENSES\.md/);
});

test('production secrets and trusted proxy CIDRs have no defaults', () => {
  const source = readFileSync(productionComposePath, 'utf8');
  const requiredVariables = [
    'MYSQL_PASSWORD',
    'MYSQL_ROOT_PASSWORD',
    'REDIS_PASSWORD',
    'ZTAPI_SESSION_SIGNING_KEY',
    'ZTAPI_UPSTREAM_MASTER_KEY',
    'TRONGRID_API_KEY',
    'ZTAPI_SOURCE_COMMIT',
    'ZTAPI_USDT_RECEIVING_ADDRESS',
    'CRYPTO_SECRET',
    'ZTAPI_TRUSTED_PROXY_CIDRS',
  ];

  for (const variable of requiredVariables) {
    assert.match(source, new RegExp(`\\$\\{${variable}:\\?[^}]+\\}`));
    assert.doesNotMatch(source, new RegExp(`\\$\\{${variable}:-`));
  }
});

test('legacy payment handlers are disabled by default in production', () => {
  const compose = readFileSync(productionComposePath, 'utf8');
  const example = readFileSync('server/.env.example', 'utf8');

  assert.match(
    compose,
    /ZTAPI_LEGACY_PAYMENT_ENABLED:\s*\$\{ZTAPI_LEGACY_PAYMENT_ENABLED:-false\}/,
  );
  assert.match(example, /ZTAPI_LEGACY_PAYMENT_ENABLED=false/);
});

test('production enables only the fixed USDT TRC-20 top-up contract', () => {
  const source = readFileSync(productionComposePath, 'utf8');
  const compose = readProductionCompose();
  const environment = compose.services.server.environment;

  assert.equal(environment.USDT_TRC20_TOPUP_ENABLED, 'true');
  assert.equal(
    environment.USDT_TRC20_RECEIVING_ADDRESS,
    '${ZTAPI_USDT_RECEIVING_ADDRESS:?set ZTAPI_USDT_RECEIVING_ADDRESS}',
  );
  assert.equal(environment.USDT_TRC20_MIN_TOPUP, '10');
  assert.equal(environment.USDT_TRC20_ORDER_TTL_SECONDS, '600');
  assert.equal(environment.USDT_TRC20_POLL_INTERVAL_SECONDS, '5');
  assert.equal(environment.USDT_TRC20_SUFFIX_COOLDOWN_SECONDS, '86400');
  assert.match(source, /TRONGRID_API_KEY:\s*\$\{TRONGRID_API_KEY:\?[^}]+\}/);
  assert.match(source, /USDT_TRC20_RECEIVING_ADDRESS:\s*\$\{ZTAPI_USDT_RECEIVING_ADDRESS:\?[^}]+\}/);
  assert.doesNotMatch(source, /TRONGRID_API_KEY:\s*[A-Za-z0-9_-]{20,}/);
  assert.equal(environment.ZTAPI_LEGACY_PAYMENT_ENABLED, '${ZTAPI_LEGACY_PAYMENT_ENABLED:-false}');
});

test('production compose accepts a synthetic TronGrid key without exposing defaults', () => {
  const syntheticKey = 'contract-test-trongrid-key-000000000000';
  const env = {
    ...process.env,
    MYSQL_DATABASE: 'ztapi',
    MYSQL_USER: 'ztapi',
    MYSQL_PASSWORD: 'contract-test-only',
    MYSQL_ROOT_PASSWORD: 'contract-test-only',
    REDIS_PASSWORD: 'contract-test-only',
    ZTAPI_SESSION_SIGNING_KEY: 'contract-test-only-signing-key-32-bytes',
    ZTAPI_UPSTREAM_MASTER_KEY: 'contract-test-only-upstream-key-32-bytes',
    CRYPTO_SECRET: 'contract-test-only-crypto-key-32-bytes',
    ZTAPI_TRUSTED_PROXY_CIDRS: '172.30.0.0/24',
    ZTAPI_TLS_DIR: 'C:/contract-test/tls',
    ZTAPI_SERVER_IMAGE: `sha256:${'a'.repeat(64)}`,
    ZTAPI_NGINX_IMAGE: `sha256:${'b'.repeat(64)}`,
    ZTAPI_RELEASE_VERSION: '0123456789abcdef0123456789abcdef01234567',
    ZTAPI_SOURCE_COMMIT: 'abcdef0123456789abcdef0123456789abcdef01',
    ZTAPI_USDT_RECEIVING_ADDRESS: 'T111111111111111111111111111111111',
    TRONGRID_API_KEY: syntheticKey,
  };
  const result = spawnSync(
    'docker',
    ['compose', '-f', productionComposePath, 'config', '--format', 'json'],
    { encoding: 'utf8', env },
  );

  assert.equal(result.status, 0, 'compose config should accept the complete synthetic environment');
  const rendered = JSON.parse(result.stdout);
  const serverEnvironment = rendered.services.server.environment;
  assert.equal(serverEnvironment.TRONGRID_API_KEY, syntheticKey);
  assert.equal(serverEnvironment.USDT_TRC20_RECEIVING_ADDRESS, env.ZTAPI_USDT_RECEIVING_ADDRESS);
  assert.equal(serverEnvironment.USDT_TRC20_TOPUP_ENABLED, 'true');
  assert.equal(serverEnvironment.ZTAPI_LEGACY_PAYMENT_ENABLED, 'false');
});

test('production environment example contains placeholders only', () => {
  const example = readFileSync(productionEnvExamplePath, 'utf8');

  assert.match(example, /^ZTAPI_SERVER_IMAGE=sha256:<server-image-id>$/m);
  assert.match(example, /^ZTAPI_NGINX_IMAGE=sha256:<nginx-image-id>$/m);
  assert.match(example, /^TRONGRID_API_KEY=replace_with_your_trongrid_api_key$/m);
  assert.match(example, /^ZTAPI_USDT_RECEIVING_ADDRESS=replace_with_your_tron_receiving_address$/m);
  assert.match(example, /^ZTAPI_SOURCE_COMMIT=$/m);
  assert.doesNotMatch(example, /^TRONGRID_API_KEY=[0-9a-f]{8}-[0-9a-f-]{27}$/im);
  assert.match(example, /^ZTAPI_LEGACY_PAYMENT_ENABLED=false$/m);
});

test('production compose interpolation fails without ZTAPI_TLS_DIR', () => {
  const env = {
    ...process.env,
    MYSQL_DATABASE: 'ztapi',
    MYSQL_USER: 'ztapi',
    MYSQL_PASSWORD: 'contract-test-only',
    MYSQL_ROOT_PASSWORD: 'contract-test-only',
    REDIS_PASSWORD: 'contract-test-only',
    ZTAPI_SESSION_SIGNING_KEY: 'contract-test-only-signing-key-32-bytes',
    ZTAPI_UPSTREAM_MASTER_KEY: 'contract-test-only-upstream-key-32-bytes',
    CRYPTO_SECRET: 'contract-test-only-crypto-key-32-bytes',
    ZTAPI_TRUSTED_PROXY_CIDRS: '172.30.0.0/24',
    ZTAPI_SERVER_IMAGE: `sha256:${'a'.repeat(64)}`,
    ZTAPI_NGINX_IMAGE: `sha256:${'b'.repeat(64)}`,
    ZTAPI_RELEASE_VERSION: '0123456789abcdef0123456789abcdef01234567',
    ZTAPI_SOURCE_COMMIT: 'abcdef0123456789abcdef0123456789abcdef01',
    ZTAPI_USDT_RECEIVING_ADDRESS: 'T111111111111111111111111111111111',
    TRONGRID_API_KEY: 'contract-test-trongrid-key-000000000000',
  };
  delete env.ZTAPI_TLS_DIR;

  const result = spawnSync(
    'docker',
    ['compose', '-f', productionComposePath, 'config'],
    { encoding: 'utf8', env },
  );
  const output = `${result.stdout}\n${result.stderr}`;

  assert.notEqual(result.status, 0);
  assert.match(output, /ZTAPI_TLS_DIR|set ZTAPI_TLS_DIR/);
});
