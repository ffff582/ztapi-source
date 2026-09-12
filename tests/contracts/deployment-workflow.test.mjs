import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import test from 'node:test';
import YAML from 'yaml';

const workflowPath = '.github/workflows/ztapi-deploy.yml';
const releaseRunbookPath = 'docs/operations/ztapi-media-release-runbook.md';
const isPublicSnapshot = existsSync('PUBLIC-SOURCE-MANIFEST.json');

function readText(path) {
  return readFileSync(path, 'utf8').replaceAll('\r\n', '\n');
}

test('ZTAPI verifies exact anonymous corresponding source before SSH or mutation', () => {
  const source = readText(workflowPath);
  const gateStart = source.indexOf('- name: Verify corresponding public source');
  const sshStart = source.indexOf('- name: Install SSH tooling');
  const deployStart = source.indexOf('- name: Deploy ZTAPI');

  assert.ok(gateStart >= 0, 'public source verification step must exist');
  assert.ok(gateStart < sshStart && sshStart < deployStart);
  const gate = source.slice(gateStart, sshStart);
  assert.match(gate, /https:\/\/api\.github\.com\/repos\/ffff582\/ztapi-source\/git\/ref\/tags\/production-\$ZTAPI_RELEASE_VERSION/);
  assert.match(gate, /https:\/\/codeload\.github\.com\/ffff582\/ztapi-source\/tar\.gz\/\$public_source_commit/);
  assert.match(gate, /\.release_commit == \$release_commit/);
  assert.match(gate, /\.source_tag == \$source_tag/);
  assert.match(gate, /tools\/public-source\/export\.mjs/);
  assert.match(gate, /--commit "\$ZTAPI_RELEASE_VERSION"/);
  assert.match(gate, /cmp -s "\$source_export\/PUBLIC-SOURCE-MANIFEST\.json" "\$public_manifest"/);
  for (const required of [
    'LICENSE',
    'NOTICE',
    'THIRD-PARTY-LICENSES.md',
    'MODIFICATIONS.md',
    'SOURCE-OFFER.md',
  ]) {
    assert.match(gate, new RegExp(required.replaceAll('.', '\\.')));
  }
  assert.doesNotMatch(gate, /secrets\./);
  assert.doesNotMatch(gate, /Authorization:/i);
});

test('ZTAPI deployment is isolated and secret-backed', () => {
  const source = readText(workflowPath);
  const workflow = YAML.parse(source);

  assert.ok(workflow.on.workflow_dispatch);
  assert.match(source, /release_commit/);
  assert.match(source, /\/opt\/ztapi/);
  assert.doesNotMatch(source, /\/opt\/kuaiyi/);
  assert.equal(workflow.jobs.deploy.environment, 'production');
  assert.equal(workflow.on.workflow_dispatch.inputs.release_commit.type, 'string');
  assert.equal(workflow.on.workflow_dispatch.inputs.release_commit.required, true);
  assert.doesNotMatch(source, /inputs\.source_ref/);
  assert.equal(
    workflow.on.workflow_dispatch.inputs.drop_legacy_token_key.type,
    'boolean',
  );
  assert.equal(
    workflow.on.workflow_dispatch.inputs.drop_legacy_token_key.default,
    false,
  );
  assert.match(source, /ZTAPI_DROP_LEGACY_TOKEN_KEY/);
  assert.match(source, /ZTAPI_DEPLOY_SSH_KEY/);
  assert.match(source, /ztapi-deploy\.env/);
  assert.match(source, /ztapi-deploy@123\.254\.104\.157/);
  assert.match(source, /sudo -n bash -s/);
  assert.doesNotMatch(source, /sshpass/);
  assert.doesNotMatch(source, /root@123\.254\.104\.157/);
  assert.match(source, /validate_secret/);
  assert.match(
    source,
    /validate_secret ZTAPI_ADMIN_USERNAME "\$ZTAPI_ADMIN_USERNAME" 3 12/,
  );
  for (const secret of [
    'ZTAPI_MYSQL_PASSWORD',
    'ZTAPI_MYSQL_ROOT_PASSWORD',
    'ZTAPI_REDIS_PASSWORD',
    'ZTAPI_SESSION_SIGNING_KEY',
    'ZTAPI_CRYPTO_SECRET',
    'ZTAPI_UPSTREAM_MASTER_KEY',
    'ZTAPI_ADMIN_USERNAME',
    'ZTAPI_ADMIN_PASSWORD',
    'ZTAPI_DEPLOY_SSH_KEY',
    'TRONGRID_API_KEY',
  ]) {
    assert.match(source, new RegExp(`secrets\\.${secret}`));
  }
  assert.match(
    source,
    /validate_secret ZTAPI_UPSTREAM_MASTER_KEY "\$ZTAPI_UPSTREAM_MASTER_KEY" 32 128/,
  );
  assert.match(
    source,
    /printf 'ZTAPI_UPSTREAM_MASTER_KEY=%q\\n' "\$ZTAPI_UPSTREAM_MASTER_KEY"/,
  );
  assert.match(source, /^\s*ZTAPI_UPSTREAM_MASTER_KEY=\$ZTAPI_UPSTREAM_MASTER_KEY\s*$/m);
  assert.match(
    source,
    /validate_secret TRONGRID_API_KEY "\$TRONGRID_API_KEY" 32 128/,
  );
  assert.match(
    source,
    /printf 'TRONGRID_API_KEY=%q\\n' "\$TRONGRID_API_KEY"/,
  );
  assert.match(source, /^\s*TRONGRID_API_KEY=\$TRONGRID_API_KEY\s*$/m);
  assert.doesNotMatch(source, /echo[^\n]*\$TRONGRID_API_KEY/);
  assert.doesNotMatch(source, /set -x/);
});

test('ZTAPI release runbook orders every guarded phase and receipt', {
  skip: isPublicSnapshot
    ? 'the public snapshot intentionally excludes private operations runbooks'
    : false,
}, () => {
  const source = readText(releaseRunbookPath);
  const phases = [
    ['PRE-01', '生产只读预检'],
    ['BACKUP-01', '备份'],
    ['BUILD-01', '构建'],
    ['MIGRATE-01', '迁移'],
    ['PRIVATE-01', '私网启动'],
    ['SMOKE-01', '内部冒烟'],
    ['CUTOVER-01', '公开切换'],
    ['POST-01', '切换后复核'],
    ['ROLLBACK-01', '回滚'],
  ];
  let previous = -1;
  for (const [receipt, title] of phases) {
    const heading = new RegExp(`^## \\d+\\. ${receipt} `, 'm').exec(source);
    const position = heading?.index ?? -1;
    assert.ok(position > previous, `${receipt} ${title} must appear in order`);
    previous = position;
  }
  assert.match(source, /每一阶段.*前序回执/);
  assert.match(source, /提交 SHA-256|提交 SHA/);
  assert.match(source, /镜像 ID/);
  assert.match(source, /gzip -t/);
  assert.match(source, /CHECK TABLE/);
  assert.match(source, /不得删除.*\.ztapi-deploy-secrets/);
  assert.match(source, /不得删除.*pre-wipe-env\.txt/);
});

test('ZTAPI deployment pins the requested commit and immutable image IDs', () => {
  const source = readText(workflowPath);
  const compose = readText('deploy/docker/docker-compose.prod.yml');

  assert.match(source, /\^\[0-9a-f\]\{40\}\$/);
  assert.match(source, /ZTAPI_RELEASE_COMMIT:\s*\$\{\{\s*inputs\.release_commit\s*\}\}/);
  assert.match(source, /ref:\s*\$\{\{\s*inputs\.release_commit\s*\}\}/);
  assert.match(source, /git rev-parse HEAD/);
  assert.match(source, /docker image inspect/);
  assert.match(source, /ZTAPI_SERVER_IMAGE=\$server_image_id/);
  assert.match(source, /ZTAPI_NGINX_IMAGE=\$nginx_image_id/);
  assert.match(source, /server_image_id.*\^sha256:\[0-9a-f\]\{64\}\$/);
  assert.match(source, /nginx_image_id.*\^sha256:\[0-9a-f\]\{64\}\$/);
  assert.match(compose, /image:\s*"?\$\{ZTAPI_SERVER_IMAGE:\?set ZTAPI_SERVER_IMAGE\}"?/);
  assert.match(compose, /image:\s*"?\$\{ZTAPI_NGINX_IMAGE:\?set ZTAPI_NGINX_IMAGE\}"?/);
  assert.doesNotMatch(compose, /image:\s*[^\n]*:latest/);
});

test('ZTAPI deployment gates public mutation with durable phase receipts', () => {
  const source = readFileSync(workflowPath, 'utf8');

  for (const phase of [
    'preflight',
    'backup',
    'build',
    'migration',
    'private',
    'smoke',
    'cutover',
    'postcheck',
  ]) {
    assert.match(source, new RegExp(`write_receipt ${phase}`));
  }
  for (const phase of [
    'preflight',
    'backup',
    'build',
    'migration',
    'private',
    'smoke',
  ]) {
    assert.match(source, new RegExp(`require_receipt ${phase}`));
  }

  const backupGate = source.indexOf('require_receipt preflight');
  const databaseBackup = source.indexOf('mysqldump');
  const privateGate = source.indexOf('require_receipt migration');
  const privateStart = source.indexOf('up -d mysql redis server', privateGate);
  const cutoverGate = source.indexOf('require_receipt smoke');
  const publicStart = source.indexOf('up -d nginx --wait', cutoverGate);

  assert.ok(backupGate >= 0 && databaseBackup > backupGate);
  assert.ok(privateGate >= 0 && privateStart > privateGate);
  assert.ok(cutoverGate >= 0 && publicStart > cutoverGate);
  assert.match(source, /gzip -t "\$database_backup"/);
  assert.match(source, /sha256sum "\$database_backup"/);
  assert.match(source, /chmod 0600 "\$release_backup"/);
  assert.match(source, /umask 077/);
  assert.match(source, /release_backup/);
  assert.match(source, /env_backup/);
  assert.match(source, /restore_previous_release/);
  assert.match(source, /write_receipt rollback/);
  const postcheckWrite = source.indexOf('write_receipt postcheck');
  const postcheckGate = source.indexOf('require_receipt postcheck', postcheckWrite);
  const completed = source.indexOf('- name: Finalize verified deployment', postcheckWrite);
  assert.ok(postcheckWrite >= 0 && postcheckGate > postcheckWrite);
  assert.ok(completed > postcheckGate);
});

test('release and rollback copies normalize uploaded file ownership', () => {
  const source = readFileSync(workflowPath, 'utf8');
  const normalizedCopies = source.match(/rsync -a --chown=root:root --delete/g) ?? [];

  assert.equal(
    normalizedCopies.length,
    2,
    'both release installation and rollback restoration must force root ownership',
  );
});

test('release build environment is regenerated from approved inputs', () => {
  const source = readFileSync(workflowPath, 'utf8');

  assert.match(source, /build_env=\/tmp\/ztapi-release\/\.ztapi-build\.env/);
  assert.match(source, /ZTAPI_SOURCE_COMMIT=\$ZTAPI_SOURCE_COMMIT/);
  assert.match(source, /ZTAPI_USDT_RECEIVING_ADDRESS=\$ZTAPI_USDT_RECEIVING_ADDRESS/);
  assert.doesNotMatch(
    source,
    /install -m 0600 \/opt\/ztapi\/\.env "\$build_env"/,
  );
  assert.doesNotMatch(
    source,
    /\/opt\/ztapi\/\.env > "\$build_env"/,
  );
});

test('ZTAPI rollback reuses the exact active Compose layers and protects unrelated containers', () => {
  const source = readText(workflowPath);

  assert.match(source, /com\.docker\.compose\.project\.config_files/);
  assert.match(source, /com\.docker\.compose\.project\.environment_file/);
  assert.match(source, /com\.docker\.compose\.project\.working_dir/);
  assert.match(source, /IFS=',' read -r -a old_compose_paths/);
  assert.match(source, /old_compose_args\+=\(-f "\$compose_file"\)/);
  assert.match(source, /"\$\{old_compose_args\[@\]\}" up -d mysql redis server/);
  assert.match(source, /"\$\{old_compose_args\[@\]\}" up -d nginx/);
  assert.match(source, /snapshot_unrelated_containers/);
  assert.match(source, /unrelated-containers\.before\.json/);
  assert.match(source, /cmp -s "\$unrelated_before" "\$unrelated_after"/);
  assert.match(source, /chmod 0600 "\$compose_file"/);
  assert.match(source, /verify_ztapi_runtime\(\)/);
  assert.match(
    source,
    /for required_container in \\\n\s+ztapi-server-1 ztapi-nginx-1 ztapi-mysql-1 ztapi-redis-1/,
  );
  assert.match(
    source,
    /restore_previous_release\(\)[\s\S]*?verify_ztapi_runtime/,
  );
  assert.match(source, /docker tag "\$old_server_image_id" "\$rollback_server_image"/);
  assert.match(source, /docker tag "\$old_nginx_image_id" "\$rollback_nginx_image"/);
  assert.match(source, /ZTAPI_SERVER_IMAGE=%s.*\$rollback_server_image/s);
  assert.match(source, /ZTAPI_NGINX_IMAGE=%s.*\$rollback_nginx_image/s);
  assert.match(source, /remove_rollback_image_tags/);
});

test('ZTAPI deployment versions administration assets with the checked out commit', () => {
  const source = readFileSync(workflowPath, 'utf8');

  assert.match(
    source,
    /test "\$\(git rev-parse HEAD\)" = "\$ZTAPI_RELEASE_COMMIT"/,
  );
  assert.match(source, /ZTAPI_RELEASE_VERSION=\$ZTAPI_RELEASE_COMMIT.*GITHUB_ENV/);
  assert.match(
    source,
    /printf 'ZTAPI_RELEASE_VERSION=%q\\n' "\$ZTAPI_RELEASE_VERSION"/,
  );
  assert.match(source, /ZTAPI_RELEASE_VERSION=\$ZTAPI_RELEASE_VERSION/);
});

test('ZTAPI deployment explicitly disables legacy payment routes in production', () => {
  const source = readFileSync(workflowPath, 'utf8');

  assert.match(
    source,
    /^\s*ZTAPI_LEGACY_PAYMENT_ENABLED=false\s*$/m,
    'the generated production .env must explicitly keep legacy payment routes disabled',
  );
  assert.doesNotMatch(
    source,
    /secrets\.ZTAPI_LEGACY_PAYMENT_ENABLED/,
    'legacy payment routes must not be switchable through a repository secret',
  );
});

test('ZTAPI deployment backs up the database before starting the new server', () => {
  const source = readFileSync(workflowPath, 'utf8');
  const backup = source.indexOf('mysqldump');
  const verifyBackup = source.indexOf('test -s "$database_backup"');
  const startPrivate = source.indexOf('up -d mysql redis server', verifyBackup);

  assert.ok(backup >= 0);
  assert.ok(verifyBackup > backup);
  assert.ok(startPrivate > verifyBackup);
  assert.match(source, /\/opt\/ztapi\/backups\/database-/);

  // Without --no-tablespaces mysqldump needs the PROCESS privilege, which the
  // application user does not have. MySQL 8 then emits a warning on every
  // deployment and the behaviour differs across server versions.
  assert.match(
    source,
    /mysqldump[^\n]*--no-tablespaces/,
    'mysqldump must pass --no-tablespaces so it does not require PROCESS',
  );
});

test('ZTAPI deployment does not expose setup credentials in process arguments', () => {
  const source = readFileSync(workflowPath, 'utf8');

  assert.match(source, /--post-file=-/);
  assert.doesNotMatch(source, /--post-data="\$setup_payload"/);
  assert.doesNotMatch(source, /--data "\$admin_login_payload"/);
});

test('ZTAPI deployment initializes privately before publishing Nginx', () => {
  const source = readFileSync(workflowPath, 'utf8');
  const startPrivate = source.indexOf('up -d mysql redis server');
  const initialize = source.indexOf('/api/setup');
  const publishNginx = source.indexOf(
    '"${compose[@]}" up -d nginx --wait --wait-timeout 120',
  );
  const stopExistingNginx = source.indexOf('stop nginx');
  const certbot = source.indexOf('certbot certonly --standalone');

  assert.ok(startPrivate >= 0);
  assert.ok(initialize > startPrivate);
  assert.ok(publishNginx > initialize);
  assert.ok(stopExistingNginx >= 0);
  assert.ok(certbot > stopExistingNginx);
  assert.match(source, /cleanup_and_restore/);
  assert.match(source, /systemctl enable --now nginx/);
  assert.match(source, /release-control\.sh finalize/);
  assert.match(source, /deploy_ztapi <\/dev\/null/);
  assert.match(
    source,
    /curl[\s\S]{0,200}--resolve ztapi\.vip:443:127\.0\.0\.1/,
  );
  assert.match(source, /for domain in ztapi\.vip www\.ztapi\.vip/);
  assert.match(source, /dig \+short A "\$domain"/);
  assert.match(source, /--resolve ztapi\.vip:443:123\.254\.104\.157/);
});

test('public endpoint verification tolerates bounded connection failures', () => {
  const source = readFileSync(workflowPath, 'utf8');
  const publicStepStart = source.indexOf('- name: Verify public endpoints');
  assert.ok(publicStepStart >= 0, 'public endpoint verification step must exist');
  const publicStep = source.slice(publicStepStart);

  assert.match(publicStep, /public_curl\(\)/);
  assert.match(publicStep, /--connect-timeout 10/);
  assert.match(publicStep, /--max-time 30/);
  assert.match(publicStep, /--retry 3/);
  assert.match(publicStep, /--retry-delay 2/);
  assert.match(publicStep, /--retry-connrefused/);
  assert.match(publicStep, /--retry-all-errors/);
  assert.match(publicStep, /public_curl --fail/);
  assert.doesNotMatch(
    publicStep,
    /public_curl[\s\S]{0,180}\|\s*grep\s+-[^\n]*q/,
    'public verification must capture complete responses before grep closes the pipe',
  );
  assert.match(
    publicStep,
    /test "\$\(public_curl --retry 0 --output \/dev\/null --write-out '%\{http_code\}'/,
  );
  assert.match(
    publicStep,
    /public_curl --retry 0 --output \/dev\/null --write-out '%\{http_code\}'[\s\S]{0,220}\/v1\/models/,
  );
  assert.match(
    publicStep,
    /public_curl --retry 0 --output \/dev\/null --write-out '%\{http_code\}'[\s\S]{0,260}\/api\/auth\/login/,
  );
  assert.match(
    publicStep,
    /if public_curl --retry 0[\s\S]{0,240}http:\/\/123\.254\.104\.157:18081/,
    'the external release gate must prove the loopback acceptance port is unreachable',
  );
  assert.match(publicStep, /acceptance port 18081 is externally reachable/);
});

test('ZTAPI deployment reuses valid certificates and serializes renewal', () => {
  const source = readFileSync(workflowPath, 'utf8');

  assert.match(source, /openssl x509 -checkend 604800 -noout/);
  assert.match(source, /pgrep -x certbot/);
  assert.match(source, /for attempt in \$\(seq 1 12\)/);
  assert.match(source, /certificate is still valid/i);
});

test('smoke test reads the login fields the backend actually returns', () => {
  const source = readFileSync(workflowPath, 'utf8');
  const authSource = readFileSync('server/controller/auth.go', 'utf8');

  // Derive the field names from the Go struct rather than hardcoding them, so
  // renaming either side breaks this test instead of only breaking production.
  const authData = authSource.match(
    /type ztAPIAuthData struct \{([\s\S]*?)\n\}/,
  );
  assert.ok(authData, 'ztAPIAuthData must exist in server/controller/auth.go');

  const tokenField = authData[1].match(
    /AccessToken\s+string\s+`json:"([^"]+)"`/,
  );
  assert.ok(tokenField, 'ztAPIAuthData must expose the access token field');
  const tokenJsonName = tokenField[1];

  assert.match(
    source,
    new RegExp(`jq -er '\\.data\\.${tokenJsonName}'`),
    `workflow must read .data.${tokenJsonName}, the field the backend returns`,
  );

  // Guard against the specific regression: reading a field that does not exist
  // makes jq -e exit non-zero and aborts the deployment after it succeeded.
  assert.doesNotMatch(
    source,
    /jq -er '\.data\.token'/,
    'workflow must not read .data.token; the backend returns .data.access_token',
  );

  // The user id is read from the same payload and must stay consistent too.
  const userIdField = authSource.match(/ID\s+int\s+`json:"([^"]+)"`/);
  assert.ok(userIdField, 'ZTAPIUserDTO must expose the id field');
  assert.match(
    source,
    new RegExp(`jq -er '\\.data\\.user\\.${userIdField[1]}'`),
    `workflow must read .data.user.${userIdField[1]}`,
  );
});

test('guarded deployment proves Telegram delivery through the deployed outbox path', () => {
  const source = readFileSync(workflowPath, 'utf8');

  assert.match(source, /\/api\/models\/ztapi\/health\/test-alert/);
  assert.match(source, /for alert_poll_attempt in \$\(seq 1 40\)/);
  assert.match(source, /alert_status.*\.data\.status/s);
  assert.match(source, /delivery_receipt \| fromjson/);
  assert.match(source, /telegram_message_id/);
  assert.match(source, /telegram_delivery_timestamp/);
  assert.match(source, /telegram-alert-production-code\.json/);
  assert.match(source, /write_receipt telegram_alert/);
  assert.match(source, /require_receipt telegram_alert/);
  assert.doesNotMatch(source, /api\.telegram\.org/);
});

test('guarded deployment completes and cleans an ordinary-user production journey', () => {
  const source = readFileSync(workflowPath, 'utf8');
  const quotation = JSON.parse(
    readFileSync('server/model/ztapi_quotation_v1.json', 'utf8'),
  );

  assert.ok(Array.isArray(quotation.entries));

  for (const route of [
    '/api/user/',
    '/api/auth/login',
    '/api/token/',
    '/api/user/models',
    '/v1/chat/completions',
    '/v1/embeddings',
    '/api/log/self',
    '/api/admin/request-logs',
    '/api/admin/users/',
  ]) {
    assert.match(source, new RegExp(route.replaceAll('/', '\\/')));
  }
  assert.match(source, /zt-gpt-4\.1-nano/);
  assert.match(source, /zt-text-embedding-3-small/);
  assert.match(source, /ordinary-user-production-code\.json/);
  assert.match(source, /cleanup_synthetic_acceptance/);
  assert.match(source, /balance-adjustments/);
  assert.match(source, /expected_status/);
  assert.match(source, /DELETE/);
  assert.match(source, /acceptance_final_quota.*0/s);
  assert.match(source, /acceptance_final_status.*2/s);
  assert.match(source, /write_receipt ordinary_user/);
  assert.match(source, /require_receipt ordinary_user/);
  assert.match(source, /https:\/\/ztapi\.vip\/api\/auth\/register/);
  assert.match(source, /jq -r '\.entries \| length'/);
  assert.match(source, /\[\.entries\[\] \| select\(\.modality/);
  assert.doesNotMatch(source, /jq -r '\.models \| length'/);
  assert.doesNotMatch(source, /echo "\$acceptance_(?:password|api_key|user_access_token)"/);
  assert.match(source, /acceptance_origin="http:\/\/127\.0\.0\.1:18081"/);
  const cleanupStart = source.indexOf('cleanup_synthetic_acceptance()');
  const cleanupEnd = source.indexOf('cleanup_and_restore()', cleanupStart);
  const cleanupSource = source.slice(cleanupStart, cleanupEnd);
  assert.match(
    cleanupSource,
    /"\$acceptance_origin\/api\/token\/\$acceptance_token_id"/,
    'synthetic API-key cleanup must use the loopback-only acceptance origin',
  );
  assert.doesNotMatch(
    cleanupSource,
    /https:\/\/ztapi\.vip\/api\/token\/\$acceptance_token_id/,
    'synthetic API-key cleanup must not call the public user API after it is gated',
  );
  const postcheck = source.indexOf('write_receipt postcheck');
  const internalUnauthorized = source.indexOf(
    '"$acceptance_origin/v1/models")" = "401"',
  );
  assert.ok(internalUnauthorized >= 0 && internalUnauthorized < postcheck);
});

test('guarded deployment atomically applies the approved commercial pricing policy', () => {
  const source = readFileSync(workflowPath, 'utf8');

  assert.match(source, /published_pricing_count=/);
  assert.match(source, /\/api\/models\/ztapi\/reprice-commercial-v2/);
  assert.match(source, /\{confirm:true\}/);
  assert.match(
    source,
    /\.data\.imported \+ \.data\.unchanged\) == \$expected/,
    'the migration must account for every published model',
  );
  assert.match(source, /\.data\.republished == \.data\.imported/);
  assert.match(source, /write_receipt commercial_pricing_v2/);
  assert.match(source, /require_receipt commercial_pricing_v2/);

  assert.match(source, /resource_type:"pool"[\s\S]{0,120}price_policy:"pool_official_80"/);
  assert.match(source, /input_per_million:"1\.65"/);
  assert.match(source, /output_per_million:"9\.90"/);
  assert.match(source, /input_sale_usd_per_million == "4\.0000000000"/);
  assert.match(source, /output_sale_usd_per_million == "24\.0000000000"/);

  assert.match(source, /price_policy:"enterprise_20_margin"/);
  assert.match(source, /input_sale_usd_per_million == "0\.3075000000"/);
  assert.match(source, /output_sale_usd_per_million == "2\.5625000000"/);
  assert.doesNotMatch(source, /price_policy:"enterprise_40_margin"/);
  assert.doesNotMatch(source, /0\.4100000000|3\.4166666667/);
});

test('guarded deployment proves public registration and leaves it enabled', () => {
  const source = readFileSync(workflowPath, 'utf8');

  assert.match(source, /acceptance_register_result/);
  assert.match(source, /\{username:\$username,password:\$password\}/);
  assert.match(source, /https:\/\/ztapi\.vip\/api\/auth\/register/);
  assert.match(source, /"Authorization: Bearer \$admin_access_token"/);
  assert.match(source, /"New-API-User: \$admin_user_id"/);
  assert.match(
    source,
    /for registration_option in RegisterEnabled PasswordRegisterEnabled/,
  );
  assert.match(source, /\{key:\$key,value:true\}/);
  assert.match(source, /\.data\.register_enabled == true/);
  assert.match(source, /\.data\.password_register_enabled == true/);
  assert.match(source, /write_receipt registration_gate/);
  assert.match(source, /require_receipt registration_gate/);

  const writeGate = source.indexOf('write_receipt registration_gate');
  const requireGate = source.indexOf('require_receipt registration_gate');
  const completed = source.indexOf('- name: Finalize verified deployment');
  assert.ok(writeGate >= 0 && writeGate < requireGate && requireGate < completed);
  assert.doesNotMatch(
    source.slice(requireGate, source.indexOf('\n', requireGate)),
    /\|\|\s*true/,
  );
  assert.match(source, /https:\/\/ztapi\.vip\/v1\/models\)" = "401"/);
});

test('guarded deployment proves ordinary users cannot use the administration hostname', () => {
  const source = readFileSync(workflowPath, 'utf8');

  assert.match(source, /admin_host_common_login_status/);
  assert.match(
    source,
    /admin_host_common_login_status[\s\S]*https:\/\/admin\.ztapi\.vip\/api\/auth\/login[\s\S]*= "401"/,
  );
  assert.match(source, /admin_host_common_api_status/);
  assert.match(
    source,
    /admin_host_common_api_status[\s\S]*"Authorization: Bearer \$acceptance_user_access_token"[\s\S]*"New-API-User: \$acceptance_user_id"[\s\S]*https:\/\/admin\.ztapi\.vip\/api\/user\/models[\s\S]*= "403"/,
  );

  const ordinaryLogin = source.indexOf('admin_host_common_login_status=');
  const ordinaryAPI = source.indexOf('admin_host_common_api_status=');
  const cleanup = source.indexOf('cleanup_synthetic_acceptance', ordinaryAPI);
  assert.ok(ordinaryLogin >= 0 && ordinaryAPI > ordinaryLogin && cleanup > ordinaryAPI);
});

test('certbot issues the three-hostname certificate non-interactively', () => {
  const source = readFileSync(workflowPath, 'utf8');
  const start = source.indexOf('certbot certonly --standalone');
  assert.ok(start >= 0, 'certbot invocation must exist');
  const end = source.indexOf('-d admin.ztapi.vip', start);
  assert.ok(end > start, 'certbot invocation must request admin.ztapi.vip');
  const invocation = source.slice(start, end);

  // The lineage name must be pinned: the workflow hardcodes
  // /etc/letsencrypt/live/ztapi.vip, and a changed domain set would otherwise
  // create a ztapi.vip-0001 lineage, breaking every later install step.
  assert.match(
    invocation,
    /--cert-name ztapi\.vip/,
    'certbot must pin --cert-name ztapi.vip so the live path stays stable',
  );

  // Growing the domain set on an existing lineage makes certbot prompt for
  // confirmation; under --non-interactive that prompt is a hard failure.
  assert.match(
    invocation,
    /--expand/,
    'certbot must pass --expand so added hostnames do not require a prompt',
  );

  assert.match(invocation, /--non-interactive/);
  for (const hostname of ['ztapi.vip', 'www.ztapi.vip']) {
    assert.ok(
      invocation.includes(`-d ${hostname}`),
      `${hostname} must be requested`,
    );
  }
});

test('ZTAPI deployment persists and verifies runtime branding', () => {
  const source = readFileSync(workflowPath, 'utf8');
  const persistBranding = source.indexOf('INSERT INTO options');
  const reloadServer = source.indexOf(
    '"${compose[@]}" up -d --force-recreate server --wait',
  );
  const publishNginx = source.indexOf(
    '"${compose[@]}" up -d nginx --wait --wait-timeout 120',
  );

  assert.ok(persistBranding >= 0);
  assert.ok(reloadServer > persistBranding);
  assert.ok(publishNginx > reloadServer);
  assert.match(source, /'SystemName', 'ZTAPI'/);
  assert.match(source, /'ServerAddress', 'https:\/\/ztapi\.vip'/);
  assert.match(source, /'Logo', '\/logo\.png'/);
  assert.match(source, /'passkey\.rp_display_name', 'ZTAPI'/);
  assert.match(source, /'passkey\.rp_id', 'ztapi\.vip'/);
  assert.match(source, /'passkey\.origins', 'https:\/\/ztapi\.vip'/);
  assert.match(source, /'general_setting\.docs_link', 'https:\/\/ztapi\.vip'/);
  assert.doesNotMatch(source, /\/api\/user\/login/);
  assert.match(
    source,
    /password NOT LIKE '\\\$argon2id\\\$v=19\\\$m=65536,t=3,p=2\\\$%'/,
  );
  assert.match(source, /CHAR_LENGTH\(password\) <> 97/);
  assert.match(source, /openssl rand -hex 8/);
  assert.match(source, /argon2 .* -id -t 3 -m 16 -p 2 -l 32 -e/);
  assert.match(source, /\/api\/auth\/login/);
  assert.match(source, /\.data\.user\.role == 100/);
  assert.match(source, /grep -q '"system_name":"ZTAPI"'/);
  assert.match(source, /grep -q '"server_address":"https:\\\/\\\/ztapi\.vip"'/);
});
