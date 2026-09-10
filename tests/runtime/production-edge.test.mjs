import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import {
  cpSync,
  mkdirSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import http from 'node:http';
import https from 'node:https';
import net from 'node:net';
import { resolve } from 'node:path';
import test from 'node:test';
import { createTestCertificate } from './create-test-certificate.mjs';

const productionCompose = 'deploy/docker/docker-compose.prod.yml';
const runtimeImages = {
  nginx: `ztapi-edge-nginx-${process.pid}:test`,
  server: `ztapi-edge-server-${process.pid}:test`,
};
const secretValues = [
  'runtime-mysql-password',
  'runtime-mysql-root-password',
  'runtime-redis-password',
  'runtime-signing-key-at-least-32-bytes',
  'runtime-crypto-secret-at-least-32-bytes',
  'runtime-upstream-master-key-32-bytes',
  'runtime-trongrid-api-key',
];
const baseEnvironment = {
  ...process.env,
  ZTAPI_RELEASE_VERSION: '0123456789abcdef0123456789abcdef01234567',
  MYSQL_DATABASE: 'ztapi',
  MYSQL_USER: 'ztapi',
  MYSQL_PASSWORD: secretValues[0],
  MYSQL_ROOT_PASSWORD: secretValues[1],
  REDIS_PASSWORD: secretValues[2],
  ZTAPI_SESSION_SIGNING_KEY: secretValues[3],
  CRYPTO_SECRET: secretValues[4],
  ZTAPI_UPSTREAM_MASTER_KEY: secretValues[5],
  TRONGRID_API_KEY: secretValues[6],
  ZTAPI_TRUSTED_PROXY_CIDRS: '172.30.0.0/24',
  ZTAPI_NGINX_IMAGE: runtimeImages.nginx,
  ZTAPI_SERVER_IMAGE: runtimeImages.server,
};

function redact(value) {
  return secretValues.reduce(
    (output, secret) => output.replaceAll(secret, '[REDACTED]'),
    value,
  );
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: process.cwd(),
    encoding: 'utf8',
    env: options.env ?? baseEnvironment,
    shell: false,
    timeout: options.timeout ?? 600_000,
  });
  const output = redact(`${result.stdout ?? ''}\n${result.stderr ?? ''}`);

  if (options.allowFailure !== true && result.status !== 0) {
    throw new Error(
      `${command} ${args.join(' ')} failed with ${result.status}\n${output}`,
    );
  }

  return { ...result, output };
}

function compose(project, overridePath, args, options = {}) {
  return run(
    'docker',
    [
      'compose',
      '--project-name',
      project,
      '-f',
      productionCompose,
      '-f',
      overridePath,
      ...args,
    ],
    options,
  );
}

async function availablePort() {
  return new Promise((resolvePort, reject) => {
    const server = net.createServer();
    server.unref();
    server.on('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      server.close(() => resolvePort(address.port));
    });
  });
}

function writePortOverride(path, httpPort, httpsPort) {
  writeFileSync(
    path,
    [
      'services:',
      '  nginx:',
      '    ports: !override',
      `      - "127.0.0.1:${httpPort}:80"`,
      `      - "127.0.0.1:${httpsPort}:443"`,
      '',
    ].join('\n'),
  );
}

async function request(protocol, port, path, options = {}) {
  const transport = protocol === 'https:' ? https : http;
  return new Promise((resolveRequest, reject) => {
    const request = transport.request(
      {
        protocol,
        hostname: '127.0.0.1',
        port,
        path,
        method: options.method ?? 'GET',
        headers: { Host: 'ztapi.vip', ...(options.headers ?? {}) },
        rejectUnauthorized: false,
        servername: 'ztapi.vip',
      },
      (response) => {
        let body = '';
        response.setEncoding('utf8');
        response.on('data', (chunk) => {
          body += chunk;
        });
        response.on('end', () => resolveRequest({
          body,
          headers: response.headers,
          statusCode: response.statusCode,
        }));
      },
    );
    request.on('error', reject);
    request.end();
  });
}

async function waitForHttps(port) {
  let lastError;
  for (let attempt = 0; attempt < 60; attempt += 1) {
    try {
      return await request('https:', port, '/');
    } catch (error) {
      lastError = error;
      await new Promise((resolveDelay) => setTimeout(resolveDelay, 1_000));
    }
  }
  throw lastError;
}

function assertNoPublishedPort(
  project,
  overridePath,
  environment,
  service,
  port,
) {
  const container = compose(project, overridePath, ['ps', '-q', service], {
    env: environment,
  });
  assert.ok(container.stdout.trim(), `${service} container must be running`);

  const result = run(
    'docker',
    [
      'inspect',
      '--format',
      '{{json .NetworkSettings.Ports}}',
      container.stdout.trim(),
    ],
    { env: environment },
  );
  const ports = JSON.parse(result.stdout.trim());
  const bindings = ports[`${port}/tcp`];
  assert.ok(
    bindings == null,
    `${service} port ${port} is unexpectedly published: ${JSON.stringify(bindings)}`,
  );
}

async function assertInvalidTlsFails(caseName, prepareDirectory) {
  const caseRoot = resolve('test-results', 'production-edge', caseName);
  const tlsDirectory = resolve(caseRoot, 'tls');
  const overridePath = resolve(caseRoot, 'override.yml');
  const project = `ztapi-edge-${caseName}-${process.pid}`.toLowerCase();
  const httpPort = await availablePort();
  const httpsPort = await availablePort();
  rmSync(caseRoot, { recursive: true, force: true });
  mkdirSync(tlsDirectory, { recursive: true });
  prepareDirectory(tlsDirectory);
  writePortOverride(overridePath, httpPort, httpsPort);

  try {
    const result = compose(
      project,
      overridePath,
      ['run', '--rm', '--no-deps', 'nginx', 'nginx', '-t'],
      {
        allowFailure: true,
        env: { ...baseEnvironment, ZTAPI_TLS_DIR: tlsDirectory },
      },
    );
    assert.notEqual(
      result.status,
      0,
      `${caseName} TLS unexpectedly passed nginx -t`,
    );
  } finally {
    compose(project, overridePath, ['down', '-v', '--remove-orphans'], {
      allowFailure: true,
      env: { ...baseEnvironment, ZTAPI_TLS_DIR: tlsDirectory },
    });
  }
}

function assertUnreadableTlsFails(
  project,
  overridePath,
  environment,
  sourceTlsDirectory,
) {
  const nginxContainer = compose(
    project,
    overridePath,
    ['ps', '-q', 'nginx'],
    { env: environment },
  ).stdout.trim();
  const image = run(
    'docker',
    ['inspect', '--format', '{{.Image}}', nginxContainer],
    { env: environment },
  ).stdout.trim();
  const appNetwork = run(
    'docker',
    [
      'network',
      'ls',
      '--filter',
      `label=com.docker.compose.project=${project}`,
      '--filter',
      'label=com.docker.compose.network=app',
      '--format',
      '{{.ID}}',
    ],
    { env: environment },
  ).stdout.trim();
  const volume = `${project}-unreadable-tls`;

  assert.ok(image, 'nginx image ID must be available');
  assert.ok(appNetwork, 'Compose app network must be available');

  try {
    run('docker', ['volume', 'create', volume], { env: environment });
    run(
      'docker',
      [
        'run',
        '--rm',
        '--user',
        '0',
        '-v',
        `${sourceTlsDirectory}:/source:ro`,
        '-v',
        `${volume}:/tls`,
        '--entrypoint',
        'sh',
        image,
        '-c',
        [
          'cp /source/fullchain.pem /tls/fullchain.pem',
          'cp /source/privkey.pem /tls/privkey.pem',
          'chmod 000 /tls/fullchain.pem',
          'chmod 644 /tls/privkey.pem',
        ].join(' && '),
      ],
      { env: environment },
    );
    const result = run(
      'docker',
      [
        'run',
        '--rm',
        '--network',
        appNetwork,
        '--user',
        '101:101',
        '--cap-drop',
        'ALL',
        '-v',
        `${volume}:/etc/nginx/tls:ro`,
        '--entrypoint',
        'nginx',
        image,
        '-t',
      ],
      { allowFailure: true, env: environment },
    );
    assert.notEqual(result.status, 0, 'unreadable TLS unexpectedly passed');
    assert.match(result.output, /permission denied/i);
    assert.match(result.output, /fullchain\.pem/);
  } finally {
    run('docker', ['volume', 'rm', '-f', volume], {
      allowFailure: true,
      env: environment,
    });
  }
}

test('production edge enforces TLS and keeps stateful ports private', { timeout: 900_000 }, async () => {
  run('docker', ['info']);

  const root = resolve('test-results', 'production-edge', 'valid');
  const tls = resolve(root, 'tls');
  const overridePath = resolve(root, 'override.yml');
  const project = `ztapi-edge-valid-${process.pid}`.toLowerCase();
  const httpPort = await availablePort();
  const httpsPort = await availablePort();
  rmSync(root, { recursive: true, force: true });
  createTestCertificate(tls);
  writePortOverride(overridePath, httpPort, httpsPort);
  const environment = { ...baseEnvironment, ZTAPI_TLS_DIR: tls };

  try {
    compose(project, overridePath, ['up', '-d', '--build'], {
      env: environment,
      timeout: 900_000,
    });
    compose(project, overridePath, ['exec', '-T', 'nginx', 'nginx', '-t'], {
      env: environment,
    });
    await waitForHttps(httpsPort);

    const redirect = await request('http:', httpPort, '/v1/models?limit=1');
    assert.equal(redirect.statusCode, 308);
    assert.equal(
      redirect.headers.location,
      'https://ztapi.vip/v1/models?limit=1',
    );

    const secure = await request('https:', httpsPort, '/');
    assert.match(
      secure.headers['strict-transport-security'] ?? '',
      /max-age=/,
    );
    assert.match(
      secure.headers['x-ztapi-source'] ?? '',
      /^https:\/\/github\.com\/ffff582\/ztapi-source\/tree\/production-[0-9a-f]{40}$/,
    );
    assert.equal(
      secure.headers.link,
      '<https://ztapi.vip/.well-known/source>; rel="source"',
    );

    const sourceMetadata = await request(
      'https:',
      httpsPort,
      '/.well-known/source',
    );
    assert.equal(sourceMetadata.statusCode, 200);
    const sourcePayload = JSON.parse(sourceMetadata.body);
    assert.equal(sourcePayload.success, true);
    assert.equal(
      sourcePayload.data.source_repository,
      'https://github.com/ffff582/ztapi-source',
    );
    assert.match(sourcePayload.data.production_commit, /^[0-9a-f]{40}$/);
    assert.equal(
      sourcePayload.data.source_tag,
      `production-${sourcePayload.data.production_commit}`,
    );

    const adminSourceMetadata = await request(
      'https:',
      httpsPort,
      '/.well-known/source',
      { headers: { Host: 'admin.ztapi.vip' } },
    );
    assert.equal(adminSourceMetadata.statusCode, 200);
    assert.equal(
      adminSourceMetadata.headers['x-ztapi-source'],
      secure.headers['x-ztapi-source'],
    );
    assert.equal(
      adminSourceMetadata.headers.link,
      '<https://ztapi.vip/.well-known/source>; rel="source"',
    );

    const blockedLegacyPayments = [
      ['POST', '/api/stripe/webhook'],
      ['POST', '/api/creem/webhook'],
      ['POST', '/api/waffo/webhook'],
      ['POST', '/api/waffo-pancake/webhook/prod'],
      ['POST', '/api/user/epay/notify'],
      ['GET', '/api/user/epay/notify'],
      ['POST', '/api/user/topup'],
      ['POST', '/api/user/pay'],
      ['POST', '/api/user/amount'],
      ['POST', '/api/user/stripe/pay'],
      ['POST', '/api/user/stripe/amount'],
      ['POST', '/api/user/creem/pay'],
      ['POST', '/api/user/waffo/amount'],
      ['POST', '/api/user/waffo/pay'],
      ['POST', '/api/user/waffo-pancake/amount'],
      ['POST', '/api/user/waffo-pancake/pay'],
      ['POST', '/api/subscription/balance/pay'],
      ['POST', '/api/subscription/epay/pay'],
      ['POST', '/api/subscription/stripe/pay'],
      ['POST', '/api/subscription/creem/pay'],
      ['POST', '/api/subscription/waffo-pancake/pay'],
      ['POST', '/api/subscription/epay/notify'],
      ['GET', '/api/subscription/epay/notify'],
      ['GET', '/api/subscription/epay/return'],
      ['POST', '/api/subscription/epay/return'],
    ];
    for (const [method, path] of blockedLegacyPayments) {
      const blocked = await request('https:', httpsPort, path, { method });
      assert.equal(blocked.statusCode, 404, `${method} ${path} must be blocked`);
    }

    const publicRelay = await request('https:', httpsPort, '/v1/models');
    assert.equal(publicRelay.statusCode, 401);
    assert.doesNotMatch(publicRelay.body, /<!doctype html/i);
    assert.equal(
      publicRelay.headers['x-request-id'],
      publicRelay.headers['x-oneapi-request-id'],
      'edge response request IDs must be identical',
    );
    assert.notEqual(publicRelay.headers['x-request-id'], 'client-controlled-id');

    for (const exactPath of ['/v1', '/v1beta']) {
      const exact = await request('https:', httpsPort, exactPath);
      assert.notEqual(exact.statusCode, 301, `${exactPath} must not redirect`);
      assert.notEqual(exact.statusCode, 308, `${exactPath} must not redirect`);
      assert.doesNotMatch(exact.body, /<!doctype html/i);
    }

    const spoofed = await request('https:', httpsPort, '/v1/models', {
      headers: { 'X-Request-ID': 'client-controlled-id' },
    });
    assert.notEqual(spoofed.headers['x-request-id'], 'client-controlled-id');
    assert.equal(
      spoofed.headers['x-request-id'],
      spoofed.headers['x-oneapi-request-id'],
    );

    const serverContainer = compose(
      project,
      overridePath,
      ['ps', '-q', 'server'],
      { env: environment },
    ).stdout.trim();
    const egress = run(
      'docker',
      [
        'exec',
        serverContainer,
        'wget',
        '-q',
        '-O',
        '-',
        'https://example.com/',
      ],
      { env: environment },
    );
    assert.match(egress.stdout, /Example Domain/);

    const licenses = compose(
      project,
      overridePath,
      [
        'exec',
        '-T',
        'nginx',
        'sh',
        '-c',
        'test -f /licenses/LICENSE && test -f /licenses/NOTICE && test -f /licenses/THIRD-PARTY-LICENSES.md',
      ],
      { env: environment },
    );
    assert.equal(licenses.status, 0);

    assertUnreadableTlsFails(
      project,
      overridePath,
      environment,
      tls,
    );

    assertNoPublishedPort(project, overridePath, environment, 'server', 3000);
    assertNoPublishedPort(project, overridePath, environment, 'mysql', 3306);
    assertNoPublishedPort(project, overridePath, environment, 'redis', 6379);
  } finally {
    compose(project, overridePath, ['down', '-v', '--remove-orphans'], {
      allowFailure: true,
      env: environment,
    });
    run('docker', ['image', 'rm', runtimeImages.nginx, runtimeImages.server], {
      allowFailure: true,
      env: environment,
    });
  }

  const validPair = createTestCertificate(
    resolve('test-results', 'production-edge', 'source-pair'),
  );
  const otherPair = createTestCertificate(
    resolve('test-results', 'production-edge', 'other-pair'),
  );

  await assertInvalidTlsFails('missing', () => {});
  await assertInvalidTlsFails('mismatched', (directory) => {
    cpSync(validPair.certificatePath, resolve(directory, 'fullchain.pem'));
    cpSync(otherPair.privateKeyPath, resolve(directory, 'privkey.pem'));
  });
});
