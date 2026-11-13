import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const nginx = readFileSync('deploy/nginx/ztapi.conf', 'utf8');
const servers = nginx.split(/\nserver \{/);
const publicServer = servers.find((block) => /listen 443 ssl;[\s\S]*server_name ztapi\.vip www\.ztapi\.vip;/.test(block));
const adminServer = servers.find((block) => /server_name admin\.ztapi\.vip;/.test(block) && /listen 443/.test(block));

test('public site serves API calls made without the /v1 prefix', () => {
  assert.ok(publicServer, 'public server block');
  const location = publicServer.match(/location ~ \^\/\(([^)]*)\)\$ \{([\s\S]*?)\n    \}/);
  assert.ok(location, 'prefix-less API location');
  const paths = location[1].split('|');
  for (const path of ['chat/completions', 'responses', 'messages', 'messages/count_tokens', 'embeddings', 'dashboard/billing/subscription']) {
    assert.ok(paths.includes(path), `${path} must be served without /v1`);
  }
  assert.match(location[2], /rewrite \^\/\(\.\*\)\$ \/v1\/\$1 break;/);
  assert.match(location[2], /proxy_pass http:\/\/server:3000;/);
  assert.match(location[2], /proxy_set_header X-ZTAPI-Internal-Acceptance "";/);
});

test('/models stays the catalog page unless an API key is sent', () => {
  assert.match(nginx, /map "\$http_authorization\$http_x_api_key\$http_x_goog_api_key" \$ztapi_api_client \{\s*"" 0;\s*default 1;\s*\}/);
  assert.match(publicServer, /location = \/models \{\s*if \(\$ztapi_api_client\) \{\s*rewrite \^ \/v1\/models last;\s*\}\s*try_files \/index\.html =404;\s*\}/);
});

test('the admin host does not expose relay paths', () => {
  assert.ok(adminServer, 'admin server block');
  assert.doesNotMatch(adminServer, /chat\/completions|\/v1\/models|ztapi_api_client/);
});
