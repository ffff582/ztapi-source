import assert from 'node:assert/strict';
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { delimiter, join } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import YAML from 'yaml';

const workflowPath = '.github/workflows/ztapi-deploy.yml';

function read(path) {
  return readFileSync(path, 'utf8').replaceAll('\r\n', '\n');
}

function sortedModelNames(rows) {
  return rows.map((row) => row.model_name).sort();
}

function assertExactCatalog(actual, expected) {
  assert.deepEqual([...actual].sort(), [...expected].sort());
}

test('public source gate hashes the anonymous exact commit tree before SSH', () => {
  const source = read(workflowPath);
  const gateStart = source.indexOf('- name: Verify corresponding public source');
  const sshStart = source.indexOf('- name: Install SSH tooling');
  assert.ok(gateStart >= 0 && sshStart > gateStart);
  const gate = source.slice(gateStart, sshStart);

  assert.match(gate, /codeload\.github\.com\/ffff582\/ztapi-source\/tar\.gz\/\$public_source_commit/);
  assert.match(gate, /tar -tzf "\$public_archive"/);
  assert.match(gate, /grep -Eq '\(\^\/\|\(\^\|\/\)\\\.\\\.\(\/\|\$\)\)'/);
  assert.match(gate, /--strip-components=1/);
  assert.match(gate, /find "\$public_tree" -type f/);
  assert.match(gate, /PUBLIC-SOURCE-MANIFEST\.json/);
  assert.match(gate, /cmp -s "\$expected_paths" "\$actual_paths"/);
  assert.match(gate, /sha256sum/);
  assert.match(gate, /stat -c %s/);
  assert.match(gate, /ZTAPI_SOURCE_COMMIT=\$public_source_commit.*GITHUB_ENV/);
  assert.doesNotMatch(gate, /Authorization:/i);
});

test('archive safety checks a captured listing without a pipefail SIGPIPE bypass', () => {
  const source = read(workflowPath);
  const gateStart = source.indexOf('- name: Verify corresponding public source');
  const sshStart = source.indexOf('- name: Install SSH tooling');
  const gate = source.slice(gateStart, sshStart);
  assert.match(gate, /tar -tzf "\$public_archive" > "\$archive_entries"/);
  assert.match(gate, /grep -Eq[^\n]* "\$archive_entries"/);
  assert.doesNotMatch(gate, /tar -tzf "\$public_archive" \| grep/);
});

test('deployment requires completed financial CI for the exact private commit', () => {
  const source = read(workflowPath);
  const workflow = YAML.parse(source);
  assert.equal(workflow.permissions.actions, 'read');

  const ciStart = source.indexOf('- name: Verify mandatory CI for exact release');
  const sourceStart = source.indexOf('- name: Verify corresponding public source');
  const sshStart = source.indexOf('- name: Install SSH tooling');
  assert.ok(ciStart >= 0 && ciStart < sourceStart && sourceStart < sshStart);
  const gate = source.slice(ciStart, sourceStart);
  assert.match(gate, /actions\/workflows\/ztapi-financial-ci\.yml\/runs/);
  assert.match(gate, /head_sha=\$ZTAPI_RELEASE_COMMIT/);
  assert.match(gate, /event=workflow_dispatch/);
  assert.match(gate, /\.head_sha == \$release_commit/);
  assert.match(gate, /\.status == "completed"/);
  assert.match(gate, /\.conclusion == "success"/);
  assert.match(gate, /\.path == "\.github\/workflows\/ztapi-financial-ci\.yml"/);
  assert.doesNotMatch(gate, /329193222/);
});

test('external verification owns finalization and has a rollback path', () => {
  const source = read(workflowPath);
  const deployStart = source.indexOf('- name: Deploy ZTAPI');
  const externalStart = source.indexOf('- name: Verify public endpoints');
  const finalizeStart = source.indexOf('- name: Finalize verified deployment');
  const rollbackStart = source.indexOf('- name: Roll back failed external acceptance');
  assert.ok(deployStart >= 0 && externalStart > deployStart);
  assert.ok(finalizeStart > externalStart && rollbackStart > finalizeStart);
  assert.doesNotMatch(source.slice(deployStart, externalStart), /deployment_completed=true/);
  assert.match(source.slice(finalizeStart, rollbackStart), /release-control\.sh finalize/);
  assert.match(source.slice(rollbackStart), /if:\s*always\(\)/);
  assert.match(source.slice(rollbackStart), /release-control\.sh rollback/);
  assert.match(source, /previous_register_enabled/);
  assert.match(source, /previous_password_register_enabled/);
  assert.match(source, /restore_registration_options/);
  assert.match(source, /rollback-state\.env/);
  assert.match(source, /had_previous_release=false/);
  assert.match(source, /had_previous_release=true/);
  assert.match(source, /if \[ "\$had_previous_release" = true \]/);
  assert.match(source, /printf 'had_previous_release=%q\\n' "\$had_previous_release"/);
});

test('registration rollback is armed before the first option mutation', () => {
  const source = read(workflowPath);
  const snapshot = source.indexOf('previous_password_register_enabled=');
  const armed = source.indexOf('registration_options_mutated=true', snapshot);
  const loop = source.indexOf('for registration_option in RegisterEnabled', snapshot);
  assert.ok(snapshot >= 0 && armed > snapshot && loop > armed);
});

test('external acceptance has a server-side watchdog and an always-run fallback', () => {
  const source = read(workflowPath);
  const state = source.indexOf('rollback-state.env');
  const watchdog = source.indexOf('systemd-run', state);
  const pending = source.indexOf('external_acceptance_pending', state);
  assert.ok(state >= 0 && watchdog > state && pending > watchdog);
  assert.match(source, /--on-active=10m/);
  assert.match(source, /"\$control_script" rollback "\$ZTAPI_RELEASE_VERSION"/);
  assert.match(source, /id: finalize_deployment/);
  assert.match(
    source,
    /if:\s*always\(\)[^\n]*steps\.finalize_deployment\.outcome != 'success'/,
  );

  const control = read('deploy/scripts/ztapi-release-control.sh');
  assert.match(control, /flock/);
  assert.match(control, /ztapi-release-watchdog-/);
  assert.match(control, /systemctl stop "\$watchdog_timer"/);
});

test('release-control terminal receipts cannot turn a rollback into finalize success', () => {
  const control = read('deploy/scripts/ztapi-release-control.sh');
  const terminalCheck = control.indexOf('if [ "$action" = finalize ]');
  const stateCheck = control.indexOf('if [ ! -s "$state_file" ]');
  assert.ok(terminalCheck >= 0 && stateCheck > terminalCheck);
  assert.match(
    control,
    /if \[ "\$action" = finalize \] && \[ -s "\$receipt_dir\/rollback\.receipt" \]; then[\s\S]*exit 1/,
  );
  assert.match(
    control,
    /if \[ "\$action" = finalize \] && \[ -s "\$receipt_dir\/completed\.receipt" \]; then[\s\S]*exit 0/,
  );
});

test('release-control state is isolated for every workflow execution of the same commit', () => {
  const workflow = read(workflowPath);
  const control = read('deploy/scripts/ztapi-release-control.sh');

  assert.match(workflow, /ZTAPI_RELEASE_EXECUTION_ID: \$\{\{ github\.run_id \}\}-\$\{\{ github\.run_attempt \}\}/);
  assert.match(
    workflow,
    /echo "ZTAPI_RELEASE_EXECUTION_ID=\$ZTAPI_RELEASE_EXECUTION_ID" >> "\$GITHUB_ENV"/,
  );
  assert.match(
    workflow,
    /printf 'ZTAPI_RELEASE_EXECUTION_ID=%q\\n' "\$ZTAPI_RELEASE_EXECUTION_ID"/,
  );
  assert.match(
    workflow,
    /receipt_dir="\/opt\/ztapi\/release-receipts\/\$ZTAPI_RELEASE_VERSION\/\$ZTAPI_RELEASE_EXECUTION_ID"/,
  );
  assert.match(
    workflow,
    /release-control\.sh finalize \$ZTAPI_RELEASE_VERSION \$ZTAPI_RELEASE_EXECUTION_ID/,
  );
  assert.match(
    workflow,
    /release-control\.sh rollback \$ZTAPI_RELEASE_VERSION \$ZTAPI_RELEASE_EXECUTION_ID/,
  );

  assert.match(control, /release_execution_id=\$\{3:\?usage:/);
  assert.match(control, /\[\[ "\$release_execution_id" =~ \^\[0-9\]\+\-\[0-9\]\+\$ \]\]/);
  assert.match(
    control,
    /receipt_dir="\/opt\/ztapi\/release-receipts\/\$release_commit\/\$release_execution_id"/,
  );
});

test('a stale watchdog cannot roll back a newer deployment execution', () => {
  const bash = process.platform === 'win32'
    ? 'C:/Program Files/Git/bin/bash.exe'
    : '/usr/bin/bash';
  const root = mkdtempSync(join(tmpdir(), 'ztapi-release-control-'));
  const oldCommit = '1'.repeat(40);
  const oldExecution = '100-1';
  const newExecution = '200-1';
  const oldReceiptDir = join(root, oldCommit, oldExecution);
  const fakeBin = join(root, 'bin');
  mkdirSync(oldReceiptDir, { recursive: true });
  mkdirSync(fakeBin, { recursive: true });
  writeFileSync(
    join(root, 'active-execution.env'),
    `commit=${oldCommit}\nexecution_id=${newExecution}\n`,
  );
  writeFileSync(
    join(oldReceiptDir, 'rollback-state.env'),
    [
      `ZTAPI_RELEASE_VERSION=${oldCommit}`,
      `ZTAPI_RELEASE_EXECUTION_ID=${oldExecution}`,
      'had_previous_release=false',
      'previous_register_enabled=true',
      'previous_password_register_enabled=true',
      'host_nginx_was_active=false',
      'unrelated_before=',
      'rollback_server_image=',
      'rollback_nginx_image=',
      '',
    ].join('\n'),
  );
  for (const command of ['docker', 'flock', 'systemctl']) {
    writeFileSync(join(fakeBin, command), '#!/usr/bin/env bash\nexit 0\n');
  }
  const transformed = read('deploy/scripts/ztapi-release-control.sh').replaceAll(
    '/opt/ztapi/release-receipts',
    '${ZTAPI_RELEASE_RECEIPTS_ROOT}',
  );
  const script = join(root, 'release-control.sh');
  writeFileSync(script, transformed);

  try {
    const result = spawnSync(bash, [script, 'rollback', oldCommit, oldExecution], {
      encoding: 'utf8',
      env: {
        ...process.env,
        PATH: `${fakeBin}${delimiter}${process.env.PATH}`,
        ZTAPI_RELEASE_RECEIPTS_ROOT: root.replaceAll('\\', '/'),
      },
    });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(existsSync(join(oldReceiptDir, 'rollback.receipt')), false);
    assert.equal(existsSync(join(oldReceiptDir, 'rollback-state.env')), true);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('deployment atomically claims shared production ownership before mutation', () => {
  const workflow = read(workflowPath);
  const claimFunction = workflow.indexOf('claim_release_execution()');
  const trap = workflow.indexOf('trap cleanup_and_restore EXIT');
  const claimCall = workflow.indexOf('claim_release_execution', trap);
  const releaseExtraction = workflow.indexOf('tar -xzf /tmp/ztapi-source.tgz');

  assert.ok(claimFunction >= 0);
  assert.match(workflow, /active_execution_file="\/opt\/ztapi\/release-receipts\/active-execution\.env"/);
  assert.match(workflow, /global_control_lock="\/opt\/ztapi\/release-receipts\/release-control\.lock"/);
  assert.match(workflow, /systemctl stop 'ztapi-release-watchdog-\*\.timer'/);
  assert.match(workflow, /mv "\$active_execution_tmp" "\$active_execution_file"/);
  assert.ok(trap >= 0 && claimCall > trap && releaseExtraction > claimCall);

  const control = read('deploy/scripts/ztapi-release-control.sh');
  assert.match(control, /lock_file="\/opt\/ztapi\/release-receipts\/release-control\.lock"/);
  assert.match(control, /active_execution_file="\/opt\/ztapi\/release-receipts\/active-execution\.env"/);
  assert.match(control, /grep -Fxq "execution_id=\$release_execution_id" "\$active_execution_file"/);
});

test('internal failure disarms the external watchdog before local rollback', () => {
  const source = read(workflowPath);
  const cleanupStart = source.indexOf('cleanup_and_restore()');
  const cleanupEnd = source.indexOf('\n          trap cleanup_and_restore EXIT', cleanupStart);
  const cleanup = source.slice(cleanupStart, cleanupEnd);
  const disarm = cleanup.indexOf('systemctl stop "$watchdog_unit.timer"');
  const restore = cleanup.indexOf('restore_previous_release');
  assert.ok(disarm >= 0 && restore > disarm);
});

test('first-unlock external rollback restores registration before stopping the stack', () => {
  const control = read('deploy/scripts/ztapi-release-control.sh');
  const firstUnlockMatch = control.match(
    /if \[ "\$\{had_previous_release:-false\}" = true \]; then[\s\S]*?\n    else\n(?<body>[\s\S]*?)\n    fi/,
  );
  assert.ok(firstUnlockMatch?.groups?.body);
  const firstUnlock = firstUnlockMatch.groups.body;
  const restore = firstUnlock.indexOf('restore_registration_options');
  const down = firstUnlock.indexOf('down --remove-orphans');
  assert.ok(restore >= 0, 'first-unlock rollback must restore both registration options');
  assert.ok(down > restore, 'registration options must be restored while MySQL is still available');
});

test('runtime source metadata and UI use the immutable public commit URL', () => {
  const metadata = read('server/common/ztapi_source.go');
  const backendTest = read('server/controller/ztapi_source_test.go');
  const home = read('web/console/src/features/home/HomePage.tsx');
  const admin = read('server/web/classic/src/ztapi/layout/AdminShell.jsx');
  const nginx = read('deploy/nginx/ztapi.conf');
  const dockerfile = read('deploy/nginx/Dockerfile');
  const offer = read('tools/public-source/templates/SOURCE-OFFER.md');

  assert.match(metadata, /SourceCommit\s+string\s+`json:"source_commit"`/);
  assert.match(metadata, /SourceURL\s+string\s+`json:"source_url"`/);
  assert.match(metadata, /ZTAPI_SOURCE_COMMIT/);
  assert.match(metadata, /\/tree\//);
  assert.match(backendTest, /source_commit/);
  assert.match(backendTest, /source_url/);
  assert.match(home, /VITE_ZTAPI_SOURCE_URL/);
  assert.match(admin, /VITE_ZTAPI_SOURCE_URL/);
  assert.doesNotMatch(home, /href="https:\/\/github\.com\/ffff582\/ztapi-source"/);
  assert.doesNotMatch(admin, /href='https:\/\/github\.com\/ffff582\/ztapi-source'/);
  assert.match(nginx, /tree\/__ZTAPI_SOURCE_COMMIT__/);
  assert.match(dockerfile, /ARG ZTAPI_SOURCE_COMMIT/);
  assert.match(offer, /resolved target/i);
  assert.match(offer, /\.well-known\/source/);
});

test('wallet address is required secret-backed runtime input with no compose default', () => {
  const source = read(workflowPath);
  const compose = read('deploy/docker/docker-compose.prod.yml');
  assert.match(source, /secrets\.ZTAPI_USDT_RECEIVING_ADDRESS/);
  assert.match(source, /validate_tron_address/);
  assert.match(source, /printf 'ZTAPI_USDT_RECEIVING_ADDRESS=%q\\n'/);
  assert.match(compose, /USDT_TRC20_RECEIVING_ADDRESS:\s*\$\{ZTAPI_USDT_RECEIVING_ADDRESS:\?[^}]+\}/);
  assert.doesNotMatch(compose, /USDT_TRC20_RECEIVING_ADDRESS:\s*["']?T[1-9A-HJ-NP-Za-km-z]{33}["']?/);
});

test('ordinary-user acceptance creates retrieves cancels and reconciles an unpaid order', () => {
  const source = read(workflowPath);
  const orderModel = read('server/model/usdt_topup_order.go');
  const cancelledStatus = orderModel.match(
    /USDTTopUpStatusExpired\s*=\s*"([^"]+)"/,
  )?.[1];
  assert.ok(cancelledStatus, 'the persisted cancellation status must be discoverable');
  const create = source.indexOf('acceptance_usdt_create_result=');
  const seed = source.indexOf('acceptance_seed_result=');
  assert.ok(create >= 0 && create < seed, 'unpaid order must be exercised before temporary credit');
  assert.match(source, /\/api\/user\/topup\/usdt-trc20\/orders/);
  assert.match(source, /\{amount:10\}/);
  assert.match(source, /\.data\.status == "pending"/);
  assert.match(source, /\.data\.pay_amount \| test\("\^10\\\\\.\[0-9\]\{2\}\$"\)/);
  assert.match(source, /\.data\.receiving_address \| test\("\^T/);
  assert.match(source, /acceptance_usdt_get_result/);
  assert.match(source, /acceptance_usdt_cancel_result/);
  assert.match(
    source,
    new RegExp(`\\.data\\.status == "${cancelledStatus}"`),
    'deployment acceptance must use the status emitted by CancelUSDTTopUpOrder',
  );
  assert.match(source, /acceptance_pre_order_quota/);
  assert.match(source, /acceptance_post_cancel_quota/);
  assert.match(source, /acceptance_outstanding_trade_no/);
  assert.match(source, /cleanup_synthetic_acceptance\(\)[\s\S]*acceptance_outstanding_trade_no/);
});

test('ordinary-user acceptance credit covers the image reservation and remains cleanup-safe', () => {
  const source = read(workflowPath);
  const seedMatch = source.match(/acceptance_seed_quota=(\d+)/);
  assert.ok(seedMatch, 'deployment acceptance must define one auditable temporary-credit amount');

  const seedQuota = Number(seedMatch[1]);
  const imageReservationQuota = Math.round(
    ((200_000 * 6.5) + (196 * 39)) * (500_000 / 1_000_000),
  );
  assert.ok(
    seedQuota >= imageReservationQuota + 500_000,
    `temporary credit ${seedQuota} must cover image reservation ${imageReservationQuota} plus prior acceptance traffic`,
  );
  assert.match(source, /--argjson delta "\$acceptance_seed_quota"/);
  assert.match(source, /\.data\.balance_after == \$seed_quota/);
  assert.match(source, /\.delta == \$seed_quota/);
  assert.match(source, /cleanup_synthetic_acceptance_with_retry/);
  assert.match(source, /\.data\.balance_after == 0/);
});

test('failed synthetic-order cleanup retains the trade number for retry', () => {
  const source = read(workflowPath);
  const cleanupStart = source.indexOf('cleanup_synthetic_acceptance()');
  const cleanupEnd = source.indexOf('\n          cleanup_and_restore()', cleanupStart);
  const cleanup = source.slice(cleanupStart, cleanupEnd);
  assert.match(cleanup, /\.data\.status == "expired"/);
  assert.match(
    cleanup,
    /if [^\n]*cancel[^\n]*; then[\s\S]*acceptance_outstanding_trade_no=""[\s\S]*else[\s\S]*cleanup_status=1/,
  );
  assert.match(source, /cleanup_synthetic_acceptance_with_retry\(\)/);
  assert.match(source, /for cleanup_attempt in 1 2 3/);
  assert.match(cleanup, /acceptance_token_deleted/);
  assert.doesNotMatch(cleanup, /acceptance_token_id=""/);
  assert.ok(
    source.match(/cleanup_synthetic_acceptance_with_retry/g)?.length >= 3,
    'retry helper must be defined and used in both normal and failure cleanup paths',
  );
});

test('acceptance compares both catalogs to the frozen exact publication set', () => {
  const source = read(workflowPath);
  assert.match(source, /ztapi_public_pricing_baseline_v1\.json/);
  assert.match(source, /ztapi_quotation_v1\.json/);
  assert.match(source, /expected_published_models/);
  assert.match(source, /expected_unpublished_models/);
  assert.match(source, /cmp -s "\$expected_published_models" "\$user_catalog_models"/);
  assert.match(source, /cmp -s "\$expected_published_models" "\$openai_catalog_models"/);
  assert.match(source, /comm -12 "\$expected_unpublished_models" "\$user_catalog_models"/);
  assert.match(source, /comm -12 "\$expected_unpublished_models" "\$openai_catalog_models"/);
  assert.match(source, /expected_pricing_model_count=/);
  assert.match(source, /expected_public_model_count=/);
  assert.doesNotMatch(
    source,
    /wc -l < "\$expected_published_models"[^\n]*\)" = (?:39|41)/,
    'publication count must be derived from the frozen pricing baseline',
  );
  assert.doesNotMatch(source, /\(\.catalog \| length\) == 37/);
});

test('deployment verifies, publishes, and bills GPT Image 2 through an ordinary user path', () => {
  const source = read(workflowPath);
  const channelSelection = source.slice(
    source.indexOf('ztapi_image_channel_id='),
    source.indexOf('image_discovery_result='),
  );
  assert.match(source, /ztapi_image_channel_id/);
  assert.match(channelSelection, /c\.ztapi_family = 'openai'/);
  assert.match(channelSelection, /ORDER BY COALESCE\(c\.priority, 0\) ASC/);
  assert.doesNotMatch(
    channelSelection,
    /abilities|a\.model = 'gpt-image-2'/,
    'enterprise channel selection must not require the not-yet-discovered image model',
  );
  assert.match(source, /fetch_models\/\$ztapi_image_channel_id\?import=true/);
  assert.match(source, /zt-gp-image-2/);
  assert.match(source, /gpt-image-2/);
  assert.match(source, /models\/ztapi\/\$ztapi_image_model_id\/verify/);
  assert.match(source, /media_price_contract/);
  assert.match(source, /\/v1\/images\/generations/);
  assert.match(source, /image_request_id/);
  assert.match(source, /image_billed_amount/);
  assert.match(source, /image_acceptance_status="success"/);
  assert.match(source, /image:\{model:\$image_model,request_id:\$image_request_id,status:\$image_status,billed_amount:\$image_billed_amount\}/);
});

test('unchanged GPT Image 2 code and pricing skip paid deployment acceptance', () => {
  const source = read(workflowPath);
  const fingerprint = read('deploy/scripts/ztapi-image-acceptance-fingerprint.sh');
  const rollout = source.slice(
    source.indexOf('# Publish GPT Image 2'),
    source.indexOf('# Publish Gemini 2.5 Flash Image'),
  );
  const ordinaryAcceptance = source.slice(
    source.indexOf('image_acceptance_status='),
    source.indexOf('gemini_image_acceptance_status='),
  );

  assert.match(fingerprint, /server\/relay\/channel\/openai\/relay_image\.go/);
  assert.match(fingerprint, /server\/service\/ztapi_media_billing\.go/);
  assert.match(fingerprint, /server\/model\/ztapi_quotation_v1\.json/);
  assert.match(fingerprint, /\.github\/workflows\/ztapi-deploy\.yml/);
  assert.match(
    source,
    /current_image_acceptance_fingerprint=\$\(bash[\s\S]*?ztapi-image-acceptance-fingerprint\.sh[\s\S]*?previous_image_acceptance_fingerprint=""[\s\S]*?if \[ "\$had_previous_release" = true \]; then[\s\S]*?previous_image_acceptance_fingerprint=\$\(bash[\s\S]*?ztapi-image-acceptance-fingerprint\.sh[\s\S]*?ztapi_image_code_changed=false/,
    'deployment must compare the previous and candidate image acceptance surfaces before replacing the release',
  );
  assert.match(
    source,
    /if \[ "\$previous_image_acceptance_fingerprint" != "\$current_image_acceptance_fingerprint" \]; then[\s\S]*ztapi_image_code_changed=true/,
    'a changed image acceptance surface must force a fresh paid acceptance',
  );
  assert.match(
    rollout,
    /ztapi_image_requires_acceptance="\$ztapi_image_code_changed"[\s\S]*if ! echo "\$ztapi_image_model"[\s\S]*ztapi_image_requires_acceptance=true/,
    'identity drift must force a fresh paid acceptance',
  );
  assert.match(
    rollout,
    /if \[ "\$current_image_price_sha" != "\$image_media_price_sha" \]; then[\s\S]*ztapi_image_requires_acceptance=true/,
    'price-contract drift must force a fresh paid acceptance',
  );
  assert.match(
    rollout,
    /if \[ "\$ztapi_image_requires_acceptance" = true \]; then[\s\S]*image_verification_result=[\s\S]*models\/ztapi\/\$ztapi_image_model_id\/verify[\s\S]*fi/,
    'the paid admin verifier must be conditional',
  );
  assert.match(
    ordinaryAcceptance,
    /image_acceptance_status="previously_accepted_unchanged"[\s\S]*if \[ "\$ztapi_image_requires_acceptance" = true \]; then[\s\S]*model:"zt-gp-image-2"[\s\S]*image_acceptance_status="success"[\s\S]*fi/,
    'the paid ordinary-user generation must use the same change gate',
  );
  assert.match(
    source,
    /image:\{model:\$image_model,request_id:\$image_request_id,status:\$image_status,billed_amount:\$image_billed_amount\}/,
    'the receipt must distinguish a fresh image call from an unchanged prior acceptance',
  );
});

test('deployment verifies, publishes, and bills Gemini 2.5 image through the native bridge', () => {
  const source = read(workflowPath);
  const channelProvisioning = source.slice(
    source.indexOf('provision_gemini_image_channel'),
    source.indexOf('blocked_media_count='),
  );
  assert.ok(channelProvisioning.length > 0, 'deployment must provision the native Gemini route before cutover');
  assert.match(channelProvisioning, /ztapi_key_ciphertext/);
  assert.match(channelProvisioning, /ztapi_family[^\n]*'gemini'/);
  assert.match(channelProvisioning, /type[^\n]*24/);
  assert.match(channelProvisioning, /source\.name = 'Yunxin enterprise'/);
  assert.match(channelProvisioning, /UPDATE channels AS target[\s\S]*target\.name = 'Yunxin enterprise Gemini'/);
  assert.doesNotMatch(
    channelProvisioning,
    /AIHUB_SK|AIHUB_TOKEN|sk-[A-Za-z0-9]/,
    'Gemini route provisioning must copy encrypted server state without embedding a plaintext upstream key',
  );
  assert.match(source, /ztapi_gemini_image_channel_id/);
  assert.match(
    source,
    /SELECT c\.id[\s\S]*c\.name = 'Yunxin enterprise Gemini'[\s\S]*fetch_models\/\$ztapi_gemini_image_channel_id\?import=true/,
    'Gemini discovery, verification, and publication must pin the dedicated enterprise channel by name',
  );
  assert.match(source, /fetch_models\/\$ztapi_gemini_image_channel_id\?import=true/);
  assert.match(source, /gemini-2\.5-flash-image/);
  assert.match(source, /zt-gemini-2\.5-flash-image/);
  assert.match(source, /models\/ztapi\/\$ztapi_gemini_image_model_id\/verify/);
  assert.match(source, /gemini_image_media_price_contract/);
  assert.match(source, /gemini_image_request_id/);
  assert.match(source, /gemini_image_billed_amount/);
  assert.match(source, /gemini_image_settlement/);
  assert.match(source, /price_rule_ids\.input_tokens/);
  assert.match(source, /expected_charged_quota/);
  assert.match(source, /restore_gemini_rollout_state/);
  assert.match(source, /gemini_was_published/);
  assert.match(source, /gemini_channel_backup/);
  assert.match(source, /UNHEX\('/);
  assert.match(
    source,
    /CONVERT\(UNHEX\(''', HEX\(channel_info\), '''\) USING utf8mb4\)/,
    'Gemini rollback must restore channel_info as utf8mb4 JSON instead of binary',
  );
  assert.match(
    source,
    /CONVERT\(UNHEX\(''', HEX\(settings\), '''\) USING utf8mb4\)/,
    'Gemini rollback must restore settings as utf8mb4 JSON instead of binary',
  );
  const workflowRestore = source.slice(
    source.indexOf('restore_gemini_rollout_state()'),
    source.indexOf('cleanup_synthetic_acceptance()'),
  );
  assert.match(
    workflowRestore,
    /< "\$gemini_channel_backup"\s*\|\| return 1/,
    'a failed Gemini channel restore must propagate out of the rollback function',
  );
  assert.match(source, /printf 'gemini_rollout_state_captured=%q/);
  const releaseControl = read('deploy/scripts/ztapi-release-control.sh');
  assert.match(releaseControl, /restore_gemini_rollout_state/);
  assert.match(releaseControl, /gemini_channel_backup/);
  assert.match(releaseControl, /source_model = 'gemini-2\.5-flash-image'/);
  const restoreFunction = releaseControl.slice(
    releaseControl.indexOf('restore_gemini_rollout_state()'),
    releaseControl.indexOf('\nverify_runtime()'),
  );
  assert.doesNotMatch(
    restoreFunction,
    /rm -f "\$gemini_channel_backup"/,
    'a successful channel restore must retain its backup until the entire rollback succeeds',
  );
  assert.match(restoreFunction, /gemini-rollout-restored/);
  assert.match(releaseControl, /registration-options-restored/);
  const rollbackCase = releaseControl.slice(
    releaseControl.indexOf('  rollback)'),
    releaseControl.indexOf('\n  *)', releaseControl.indexOf('  rollback)')),
  );
  const rollbackSucceeded = rollbackCase.indexOf('test "$rollback_status" -eq 0');
  const writeRollbackReceipt = rollbackCase.indexOf('write_receipt rollback');
  const removeGeminiBackup = rollbackCase.indexOf('rm -f "${gemini_channel_backup:-}"');
  assert.ok(
    rollbackSucceeded >= 0 && writeRollbackReceipt > rollbackSucceeded && removeGeminiBackup > writeRollbackReceipt,
    'the terminal rollback receipt and channel-backup cleanup must happen only after every rollback step succeeds',
  );
  assert.match(source, /published_media_count:2/);
});

test('an unchanged published Gemini image product does not block unrelated deployments', () => {
  const source = read(workflowPath);
  const rollout = source.slice(
    source.indexOf('# Publish Gemini 2.5 Flash Image'),
    source.indexOf("test \"$(curl --silent --output /dev/null", source.indexOf('# Publish Gemini 2.5 Flash Image')),
  );

  assert.match(
    rollout,
    /gemini_image_requires_acceptance=true[\s\S]*if \[ "\$gemini_was_published" = true \]; then[\s\S]*gemini_image_requires_acceptance=false/,
    'an already-published Gemini image product should start as acceptance-complete',
  );
  assert.match(
    rollout,
    /if ! echo "\$ztapi_gemini_image_model"[\s\S]*gemini_image_requires_acceptance=true[\s\S]*gemini_image_identity_result=/,
    'an identity change must restore the blocking acceptance requirement',
  );
  assert.match(
    rollout,
    /if \[ "\$current_gemini_image_price_sha" != "\$gemini_image_media_price_sha" \]; then[\s\S]*gemini_image_requires_acceptance=true[\s\S]*gemini_image_price_result=/,
    'a price-contract change must restore the blocking acceptance requirement',
  );
  assert.match(
    rollout,
    /if \[ "\$gemini_image_requires_acceptance" = true \]; then[\s\S]*gemini_image_verification_result=[\s\S]*models\/ztapi\/\$ztapi_gemini_image_model_id\/verify[\s\S]*fi/,
    'the paid upstream verifier must run only when first publication or configuration changes require it',
  );

  const ordinaryAcceptance = source.slice(
    source.indexOf('gemini_image_acceptance_status='),
    source.indexOf('registration_status=', source.indexOf('gemini_image_acceptance_status=')),
  );
  assert.match(
    ordinaryAcceptance,
    /gemini_image_acceptance_status="previously_accepted_unchanged"[\s\S]*if \[ "\$gemini_image_requires_acceptance" = true \]; then[\s\S]*model:"zt-gemini-2\.5-flash-image"[\s\S]*gemini_image_acceptance_status="success"[\s\S]*fi/,
    'the paid ordinary-user image call must use the same first-publication or configuration-change gate',
  );
  assert.match(
    ordinaryAcceptance,
    /if \[ "\$gemini_image_requires_acceptance" = true \]; then[\s\S]*gemini_image_settlement=[\s\S]*fi/,
    'Gemini billing reconciliation must remain mandatory whenever a fresh paid call is required',
  );
});

test('pre-cutover media guard permits both published image products', () => {
  const source = read(workflowPath);
  const guard = source.slice(
    source.indexOf('blocked_media_count='),
    source.indexOf('write_receipt smoke'),
  );

  assert.ok(guard.length > 0, 'pre-cutover blocked-media guard must exist');
  assert.doesNotMatch(
    guard,
    /'gpt-image-2'/,
    'an idempotent redeploy must not reject the already-published GPT Image 2',
  );
	assert.doesNotMatch(
		guard,
		/'gemini-2\.5-flash-image'/,
		'an idempotent redeploy must not reject the published Gemini image product',
	);
  for (const unresolvedModel of [
    'doubao-seedance-2.0',
    'doubao-seedance-2.0-fast',
    'doubao-seedance-2.0-mini',
  ]) {
    assert.match(guard, new RegExp(`'${unresolvedModel.replaceAll('.', '\\.')}'`));
  }
  assert.match(guard, /test "\$blocked_media_count" = "0"/);
});

test('exact catalog comparison rejects an unpublished substitution at the same count', () => {
  const baseline = JSON.parse(read('server/model/testdata/ztapi_public_pricing_baseline_v1.json'));
  const quotation = JSON.parse(read('server/model/ztapi_quotation_v1.json'));
	const expected = [
		...sortedModelNames(baseline),
		'zt-gp-image-2',
		'zt-gemini-2.5-flash-image',
	].sort();
  const unpublished = quotation.entries
    .filter((entry) => entry.status === 'mapping_pending')
    .map((entry) => `zt-${entry.label.toLowerCase().replaceAll(' ', '-')}`)
    .sort();

	assert.equal(expected.length, 41);
  assert.ok(unpublished.length > 0, 'quotation must retain blocked media candidates');

  const swapped = [...expected.slice(1), unpublished[0]];
  assert.equal(swapped.length, expected.length, 'count-only validation would pass');
  assert.throws(
    () => assertExactCatalog(swapped, expected),
    { name: 'AssertionError' },
  );
});

test('deployment validates media and token catalog shapes separately', () => {
  const source = read(workflowPath);
  const catalogCheck = source.slice(
    source.indexOf('acceptance_catalog=$(curl'),
    source.indexOf('acceptance_catalog_count=', source.indexOf('acceptance_catalog=$(curl')),
  );

  assert.match(catalogCheck, /if \(\.modality == "image" or \.modality == "video"\) then/);
  assert.match(catalogCheck, /\.input_price_per_million == ""/);
  assert.match(catalogCheck, /\.output_price_per_million == ""/);
  assert.match(catalogCheck, /\(\.billing_dimensions \| length\) == 0/);
  assert.match(catalogCheck, /\(\.sale_usd \| length\) == 0/);
  assert.match(catalogCheck, /\.billing_rule == "multi_dimension"/);
  assert.match(catalogCheck, /\(\.supported_options \| type\) == "object"/);
  assert.match(catalogCheck, /\(\.pricing_rules \| length\) > 0/);
  assert.match(catalogCheck, /\(\.billing_unit \| length\) > 0/);
  assert.match(catalogCheck, /else[\s\S]*\(\.input_price_per_million \| tonumber\) >= 0/);

  const pricingCheck = source.slice(
    source.indexOf('acceptance_pricing=$(curl'),
    source.indexOf('acceptance_catalog_count=', source.indexOf('acceptance_pricing=$(curl')),
  );
  assert.match(pricingCheck, /acceptance_origin\/api\/pricing/);
  assert.match(pricingCheck, /has\("input_price_per_million"\)/);
  assert.match(pricingCheck, /has\("output_price_per_million"\)/);
  assert.match(pricingCheck, /has\("billing_dimensions"\)/);
  assert.match(pricingCheck, /has\("sale_usd"\)/);
  assert.match(pricingCheck, /\.input_price_per_million == ""/);
  assert.match(pricingCheck, /\.output_price_per_million == ""/);
  assert.match(pricingCheck, /\(\.billing_dimensions \| length\) == 0/);
  assert.match(pricingCheck, /\(\.sale_usd \| length\) == 0/);
});

test('anonymous history evidence records parents, tag target, and private SHA absence', () => {
  const source = read(workflowPath);
  const gateStart = source.indexOf('- name: Verify corresponding public source');
  const sshStart = source.indexOf('- name: Install SSH tooling');
  const gate = source.slice(gateStart, sshStart);
  assert.match(gate, /public-parent-shas/);
  assert.match(gate, /parent_count/);
  assert.match(gate, /git clone[\s\S]*?https:\/\/github\.com\/ffff582\/ztapi-source\.git/);
  assert.match(gate, /git -C[^\n]*rev-parse "refs\/tags\/\$source_tag\^\{commit\}"/);
  assert.match(gate, /git -C[^\n]*cat-file -e "\$ZTAPI_RELEASE_VERSION\^\{commit\}"/);
});
