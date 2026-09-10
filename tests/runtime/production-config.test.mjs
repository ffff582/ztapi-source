import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const nginxConfigPath = 'deploy/nginx/ztapi.conf';

function serverBlock(source, hostname) {
  const marker = `server_name ${hostname};`;
  const start = source.indexOf(marker);

  assert.notEqual(start, -1, `${hostname} server block must exist`);
  const end = source.indexOf('\n}', start);
  assert.notEqual(end, -1, `${hostname} server block must close`);
  return source.slice(start, end + 2);
}

test('public and administration hosts use distinct static roots', () => {
  const source = readFileSync(nginxConfigPath, 'utf8');
  const publicHost = serverBlock(source, 'ztapi.vip www.ztapi.vip');
  const adminHost = serverBlock(source, 'admin.ztapi.vip');

  assert.match(publicHost, /root \/usr\/share\/nginx\/public;/);
  assert.match(adminHost, /root \/usr\/share\/nginx\/admin;/);
  assert.doesNotMatch(adminHost, /\/usr\/share\/nginx\/public/);
  assert.match(adminHost, /try_files \$uri \$uri\/ \/index\.html;/);
});

test('administration host blocks public registration and protects its login', () => {
  const source = readFileSync(nginxConfigPath, 'utf8');
  const publicHost = serverBlock(source, 'ztapi.vip www.ztapi.vip');
  const adminHost = serverBlock(source, 'admin.ztapi.vip');

  for (const host of [publicHost, adminHost]) {
    for (const endpoint of [
      '/api/user/login',
      '/api/user/register',
      '/api/user/logout',
    ]) {
      assert.match(
        host,
        new RegExp(`location = ${endpoint.replaceAll('/', '\\/')}\\s*\\{\\s*return 404;`),
      );
    }
  }

  assert.match(source, /limit_req_zone \$binary_remote_addr zone=admin_login:10m rate=5r\/m;/);
  assert.match(adminHost, /location = \/api\/auth\/login\s*\{[\s\S]*limit_req zone=admin_login burst=3 nodelay;/);
  assert.match(adminHost, /location = \/api\/auth\/register\s*\{\s*return 404;/);
  assert.match(adminHost, /location \/api\/ \{[\s\S]*proxy_pass http:\/\/server:3000;/);
  assert.doesNotMatch(adminHost, /location = \/v1\b/);
  assert.doesNotMatch(adminHost, /location \/v1\//);
  assert.doesNotMatch(adminHost, /location = \/v1beta\b/);
  assert.doesNotMatch(adminHost, /location \/v1beta\//);
});

test('administration host sends strict browser security headers', () => {
  const source = readFileSync(nginxConfigPath, 'utf8');
  const adminHost = serverBlock(source, 'admin.ztapi.vip');

  assert.match(adminHost, /X-Robots-Tag "noindex, nofollow" always;/);
  assert.match(adminHost, /Content-Security-Policy/);
  assert.match(adminHost, /Strict-Transport-Security "max-age=31536000; includeSubDomains" always;/);
  assert.match(adminHost, /X-Content-Type-Options "nosniff" always;/);
  assert.match(adminHost, /Referrer-Policy "strict-origin-when-cross-origin" always;/);
  assert.match(adminHost, /X-Frame-Options "DENY" always;/);
});

test('administration host revalidates the shell and static assets after deployment', () => {
  const source = readFileSync(nginxConfigPath, 'utf8');
  const adminHost = serverBlock(source, 'admin.ztapi.vip');

  assert.match(
    adminHost,
    /location = \/index\.html\s*\{[\s\S]*expires -1;/,
  );
  assert.match(
    adminHost,
    /location \^~ \/static\/\s*\{[\s\S]*expires -1;/,
  );
});

test('public host sends strict browser security headers and accepts relay payloads', () => {
  const source = readFileSync(nginxConfigPath, 'utf8');
  const publicHost = serverBlock(source, 'ztapi.vip www.ztapi.vip');

  assert.match(publicHost, /Content-Security-Policy/);
  assert.match(publicHost, /Strict-Transport-Security "max-age=31536000; includeSubDomains" always;/);
  assert.match(publicHost, /X-Content-Type-Options "nosniff" always;/);
  assert.match(publicHost, /Referrer-Policy "strict-origin-when-cross-origin" always;/);
  assert.match(publicHost, /X-Frame-Options "DENY" always;/);
  assert.match(publicHost, /client_max_body_size 32m;/);
});

test('public host exposes customer auth without exposing administration routes', () => {
  const source = readFileSync(nginxConfigPath, 'utf8');
  const publicHost = serverBlock(source, 'ztapi.vip www.ztapi.vip');

  for (const endpoint of ['/api/auth/login', '/api/auth/register']) {
    assert.doesNotMatch(
      publicHost,
      new RegExp(`location = ${endpoint.replaceAll('/', '\\/')}\\s*\\{\\s*return \\d{3};`),
    );
  }
  assert.match(publicHost, /location \/api\/ \{[\s\S]*proxy_pass http:\/\/server:3000;/);
  for (const prefix of ['/api/admin/', '/api/channel/', '/api/models/ztapi/']) {
    assert.match(
      publicHost,
      new RegExp(`location \\^~ ${prefix.replaceAll('/', '\\/')}\\s*\\{\\s*return 404;`),
    );
  }
});

test('public host blocks every legacy payment and callback path', () => {
  const source = readFileSync(nginxConfigPath, 'utf8');
  const publicHost = serverBlock(source, 'ztapi.vip www.ztapi.vip');
  const exactPaths = [
    '/api/stripe/webhook',
    '/api/creem/webhook',
    '/api/waffo/webhook',
    '/api/user/epay/notify',
    '/api/user/topup',
    '/api/user/pay',
    '/api/user/amount',
    '/api/user/stripe/pay',
    '/api/user/stripe/amount',
    '/api/user/creem/pay',
    '/api/user/waffo/amount',
    '/api/user/waffo/pay',
    '/api/user/waffo-pancake/amount',
    '/api/user/waffo-pancake/pay',
    '/api/subscription/balance/pay',
    '/api/subscription/epay/pay',
    '/api/subscription/stripe/pay',
    '/api/subscription/creem/pay',
    '/api/subscription/waffo-pancake/pay',
    '/api/subscription/epay/notify',
    '/api/subscription/epay/return',
  ];

  for (const endpoint of exactPaths) {
    assert.match(
      publicHost,
      new RegExp(`location = ${endpoint.replaceAll('/', '\\/')}\\s*\\{\\s*return 404;`),
    );
  }
  assert.match(
    publicHost,
    /location \^~ \/api\/waffo-pancake\/webhook\/\s*\{\s*return 404;/,
  );
});
