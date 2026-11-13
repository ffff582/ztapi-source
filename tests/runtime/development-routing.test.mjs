import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import http from 'node:http';
import net from 'node:net';
import { resolve } from 'node:path';
import test from 'node:test';

const developmentCompose = 'deploy/docker/docker-compose.dev.yml';

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: process.cwd(),
    encoding: 'utf8',
    env: process.env,
    shell: false,
    timeout: options.timeout ?? 600_000,
  });
  if (options.allowFailure !== true && result.status !== 0) {
    throw new Error(
      `${command} ${args.join(' ')} failed with ${result.status}\n${result.stdout}\n${result.stderr}`,
    );
  }
  return result;
}

function compose(project, overridePath, args, options = {}) {
  return run(
    'docker',
    [
      'compose',
      '--project-name',
      project,
      '-f',
      developmentCompose,
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

async function request(port, path) {
  return new Promise((resolveRequest, reject) => {
    const outgoing = http.request(
      { hostname: '127.0.0.1', port, path, method: 'GET' },
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
    outgoing.on('error', reject);
    outgoing.end();
  });
}

async function waitForStatus(port) {
  let lastError;
  for (let attempt = 0; attempt < 90; attempt += 1) {
    try {
      const response = await request(port, '/api/status');
      if (response.statusCode === 200) {
        return response;
      }
    } catch (error) {
      lastError = error;
    }
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 1_000));
  }
  throw lastError ?? new Error('development status route did not become ready');
}

test('development console proxies API routes to the Go server', { timeout: 900_000 }, async () => {
  const port = await availablePort();
  const root = resolve('test-results', 'development-routing');
  const overridePath = resolve(root, 'override.yml');
  const project = `ztapi-dev-routing-${process.pid}`.toLowerCase();
  rmSync(root, { recursive: true, force: true });
  mkdirSync(root, { recursive: true });
  writeFileSync(
    overridePath,
    [
      'services:',
      '  console:',
      '    ports: !override',
      `      - "127.0.0.1:${port}:5173"`,
      '',
    ].join('\n'),
  );

  try {
    compose(project, overridePath, ['up', '-d', '--build'], {
      timeout: 900_000,
    });
    const status = await waitForStatus(port);
    assert.match(status.headers['content-type'] ?? '', /application\/json/);
    assert.doesNotMatch(status.body, /<!doctype html/i);
    const parsed = JSON.parse(status.body);
    assert.equal(parsed.success, true);
    assert.ok(parsed.data, 'status response must come from the Go API');
  } finally {
    compose(project, overridePath, ['down', '-v', '--remove-orphans'], {
      allowFailure: true,
    });
  }
});
