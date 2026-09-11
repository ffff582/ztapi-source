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
  assert.doesNotMatch(source, /\(\.catalog \| length\) == 37/);
});

test('deployment verifies, publishes, and bills GPT Image 2 through an ordinary user path', () => {
  const source = read(workflowPath);
  assert.match(source, /ztapi_image_channel_id/);
  assert.match(source, /fetch_models\/\$ztapi_image_channel_id\?import=true/);
  assert.match(source, /zt-gp-image-2/);
  assert.match(source, /gpt-image-2/);
  assert.match(source, /models\/ztapi\/\$ztapi_image_model_id\/verify/);
  assert.match(source, /media_price_contract/);
  assert.match(source, /\/v1\/images\/generations/);
  assert.match(source, /image_request_id/);
  assert.match(source, /image_billed_amount/);
  assert.match(source, /published_media_count:1/);
});

test('exact catalog comparison rejects an unpublished substitution at the same count', () => {
  const baseline = JSON.parse(read('server/model/testdata/ztapi_public_pricing_baseline_v1.json'));
  const quotation = JSON.parse(read('server/model/ztapi_quotation_v1.json'));
  const expected = [...sortedModelNames(baseline), 'zt-gp-image-2'].sort();
  const unpublished = quotation.entries
    .filter((entry) => entry.status === 'mapping_pending')
    .map((entry) => `zt-${entry.label.toLowerCase().replaceAll(' ', '-')}`)
    .sort();

  assert.equal(expected.length, 38);
  assert.ok(unpublished.length > 0, 'quotation must retain blocked media candidates');

  const swapped = [...expected.slice(1), unpublished[0]];
  assert.equal(swapped.length, expected.length, 'count-only validation would pass');
  assert.throws(
    () => assertExactCatalog(swapped, expected),
    { name: 'AssertionError' },
  );
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
