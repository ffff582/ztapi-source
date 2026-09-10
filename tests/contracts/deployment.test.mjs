import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import {
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
} from 'node:fs';
import { resolve } from 'node:path';
import test from 'node:test';

const dockerfilePath = 'deploy/nginx/Dockerfile';
const dockerignorePath = '.dockerignore';
const workflowPath = '.github/workflows/ztapi-deploy.yml';
const webWorkspacePath = 'server/web';
const nginxConfigPath = 'deploy/nginx/ztapi.conf';
const productionComposePath = 'deploy/docker/docker-compose.prod.yml';

test('production edge advertises the corresponding source on both hosts', () => {
  const nginx = readFileSync(nginxConfigPath, 'utf8');
  const dockerfile = readFileSync(dockerfilePath, 'utf8');
  const compose = readFileSync(productionComposePath, 'utf8');
  const publicServerStart = nginx.indexOf('server_name ztapi.vip www.ztapi.vip;');
  const acceptanceServerStart = nginx.indexOf('server_name acceptance.ztapi.internal;');
  const adminServerStart = nginx.indexOf('server_name admin.ztapi.vip;');
  const publicServer = nginx.slice(publicServerStart, acceptanceServerStart);
  const adminServer = nginx.slice(adminServerStart);

  for (const [name, block] of [
    ['public', publicServer],
    ['administration', adminServer],
  ]) {
    assert.match(
      block,
      /add_header X-ZTAPI-Source "https:\/\/github\.com\/ffff582\/ztapi-source\/tree\/production-__ZTAPI_RELEASE_COMMIT__" always;/,
      `${name} host must advertise its exact corresponding source tag`,
    );
    assert.match(
      block,
      /add_header Link "<https:\/\/ztapi\.vip\/\.well-known\/source>; rel=\\\"source\\\"" always;/,
      `${name} host must advertise the anonymous source metadata endpoint`,
    );
  }

  assert.match(
    publicServer,
    /location = \/\.well-known\/source \{[\s\S]*proxy_pass http:\/\/server:3000;/,
    'the source metadata endpoint must be reachable without authentication',
  );
  assert.match(dockerfile, /ARG ZTAPI_RELEASE_VERSION/);
  assert.match(
    dockerfile,
    /sed[^\n]+__ZTAPI_RELEASE_COMMIT__[^\n]+ZTAPI_RELEASE_VERSION/,
  );
  assert.match(
    compose,
    /ZTAPI_RELEASE_VERSION:\s*\$\{ZTAPI_RELEASE_VERSION:\?[^}]+\}/,
  );
});

test('clean Bun workspace resolves classic catalog dependencies', { timeout: 120_000 }, () => {
  const workspaceManifest = resolve(webWorkspacePath, 'package.json');

  assert.ok(
    existsSync(workspaceManifest),
    'server/web/package.json must define the Bun workspace and catalog',
  );

  mkdirSync('test-results', { recursive: true });
  const cleanWorkspace = mkdtempSync(
    resolve('test-results', 'task11-bun-workspace-'),
  );
  mkdirSync(resolve(cleanWorkspace, 'classic'));
  mkdirSync(resolve(cleanWorkspace, 'default'));

  try {
    for (const [source, destination] of [
      [workspaceManifest, resolve(cleanWorkspace, 'package.json')],
      [resolve(webWorkspacePath, 'bun.lock'), resolve(cleanWorkspace, 'bun.lock')],
      [
        resolve(webWorkspacePath, 'classic/package.json'),
        resolve(cleanWorkspace, 'classic/package.json'),
      ],
      [
        resolve(webWorkspacePath, 'default/package.json'),
        resolve(cleanWorkspace, 'default/package.json'),
      ],
    ]) {
      copyFileSync(source, destination);
    }

    const bunArguments = [
      'install',
      '--frozen-lockfile',
      '--lockfile-only',
      '--filter',
      'react-template',
    ];
    const command = process.platform === 'win32'
      ? (process.env.ComSpec ?? 'cmd.exe')
      : 'bun';
    const commandArguments = process.platform === 'win32'
      ? ['/d', '/s', '/c', `bun ${bunArguments.join(' ')}`]
      : bunArguments;
    const result = spawnSync(
      command,
      commandArguments,
      {
        cwd: cleanWorkspace,
        encoding: 'utf8',
        timeout: 120_000,
      },
    );
    const output = [
      result.stdout ?? '',
      result.stderr ?? '',
      result.error?.message ?? '',
    ].join('\n');

    assert.equal(result.status, 0, output);
  } finally {
    rmSync(cleanWorkspace, { recursive: true, force: true });
  }
});

test('production image builds isolated public and administration bundles', () => {
  const dockerfile = readFileSync(dockerfilePath, 'utf8');
  const dockerignore = readFileSync(dockerignorePath, 'utf8');

  assert.match(dockerfile, /AS public-build/);
  assert.match(dockerfile, /AS admin-build/);
  assert.match(
    dockerfile,
    /COPY server\/web\/package\.json server\/web\/bun\.lock \.\//,
  );
  assert.match(
    dockerfile,
    /COPY server\/web\/classic\/package\.json classic\/package\.json/,
  );
  assert.match(
    dockerfile,
    /COPY server\/web\/default\/package\.json default\/package\.json/,
  );
  assert.match(
    dockerfile,
    /RUN bun install --frozen-lockfile --filter react-template/,
  );
  assert.match(
    dockerfile,
    /VITE_ZTAPI_ADMIN_APP=true[\s\\]+VITE_REACT_APP_VERSION=/,
  );
  assert.match(
    dockerfile,
    /COPY --from=public-build .*\/usr\/share\/nginx\/public\//,
  );
  assert.match(
    dockerfile,
    /COPY --from=admin-build .*\/usr\/share\/nginx\/admin\//,
  );
  assert.match(dockerignore, /!server\/web\//);
  assert.match(dockerignore, /!server\/web\/package\.json/);
  assert.match(dockerignore, /!server\/web\/default\/package\.json/);
});

test('deployment packages and verifies the administration hostname', () => {
  const workflow = readFileSync(workflowPath, 'utf8');

  assert.doesNotMatch(workflow, /--exclude='server\/web'/);
  assert.match(workflow, /-d admin\.ztapi\.vip/);
  assert.match(workflow, /for domain in ztapi\.vip www\.ztapi\.vip admin\.ztapi\.vip/);
  assert.match(workflow, /--resolve admin\.ztapi\.vip:443:127\.0\.0\.1/);
  assert.match(workflow, /--resolve admin\.ztapi\.vip:443:123\.254\.104\.157/);
  assert.match(workflow, /openssl x509 -checkend 604800 -noout/);
});

test('certificate reuse and copy hook verify exact required hostnames', () => {
  const workflow = readFileSync(workflowPath, 'utf8');
  const reuseStart = workflow.indexOf(
    'certificate_path=/etc/letsencrypt/live/ztapi.vip/fullchain.pem',
  );
  const reuseEnd = workflow.indexOf('\n          else', reuseStart);
  const copyHookStart = workflow.indexOf(
    "cat > /usr/local/sbin/ztapi-copy-certificate <<'SCRIPT'",
  );
  const copyHookEnd = workflow.indexOf('\n          SCRIPT', copyHookStart);

  assert.ok(reuseStart >= 0 && reuseEnd > reuseStart);
  assert.ok(copyHookStart >= 0 && copyHookEnd > copyHookStart);
  const certificatePaths = [
    workflow.slice(reuseStart, reuseEnd),
    workflow.slice(copyHookStart, copyHookEnd),
  ];

  for (const certificatePath of certificatePaths) {
    assert.doesNotMatch(
      certificatePath,
      /openssl x509 -text[\s\S]*grep -Fq ['"]DNS:/,
    );
    for (const hostname of [
      'ztapi.vip',
      'www.ztapi.vip',
      'admin.ztapi.vip',
    ]) {
      const escapedHostname = hostname.replaceAll('.', '\\.');
      const checks = certificatePath.match(
        new RegExp(
          `openssl x509 -checkhost ${escapedHostname} -noout -in "\\$certificate_path"`,
          'g',
        ),
      );

      assert.equal(checks?.length ?? 0, 1, `${hostname} must be checked once`);

      // `openssl x509 -checkhost` exits 0 even when the hostname does NOT
      // match; it only reports the verdict on stdout. Asserting the command
      // exists is therefore not enough — a mismatch would silently pass. Each
      // check must pipe into a grep on the printed verdict.
      const gatedCheck = new RegExp(
        `openssl x509 -checkhost ${escapedHostname} -noout -in "\\$certificate_path"\\s*\\|\\s*\\n?\\s*grep -q 'does match certificate'`,
      );
      assert.match(
        certificatePath,
        gatedCheck,
        `${hostname} check must gate on the printed verdict, not the exit code`,
      );
    }
  }
});
