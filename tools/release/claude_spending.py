"""Compose the independent spending installer with the existing bounded rollout."""
import base64
import copy
import importlib.util
import json
import os
from pathlib import Path
import re


def sibling(name):
    spec = importlib.util.spec_from_file_location('_claude_spending_' + name, Path(__file__).with_name(name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


rolling = sibling('longtext_security')
spending = sibling('spending_release')
ROOT = Path(__file__).resolve().parent
RELEASE = 'claude-spend-20261102-r1'
SPENDING = 'ztapi-spending-20261102-r1'
_base_isolated_tests = rolling.isolated_tests


def require(ok, code):
    if not ok:
        raise RuntimeError(code)


def save(name, value):
    require('/' not in name and '\\' not in name, 'invalid_feature_receipt')
    spending.protected_save(ROOT / name, value)


def configure_main(directory):
    rolling.STAGE = Path(directory)
    rolling.RELEASE = RELEASE
    rolling.CANDIDATE = 'ztapi-claude-spend-candidate-r1'
    rolling.PREVIOUS = 'ztapi-server-before-claude-spend-r1'
    rolling.build_image = build_main_image
    rolling.isolated_tests = isolated_tests


def build_main_image(original, build):
    stage = rolling.STAGE
    raw = (stage / 'ztapi-linux-amd64').read_bytes()
    require(rolling.sha(raw) == build['binary_sha256'], 'binary_hash_mismatch')
    require(re.fullmatch('sha256:[a-f0-9]{64}', original['Image']) is not None, 'invalid_base_image')
    require(re.fullmatch('[a-f0-9]{40}', build['commit']) is not None, 'invalid_source_commit')
    directory = stage / 'main-build'
    require(not directory.is_symlink(), 'unsafe_main_build_context')
    directory.mkdir(mode=0o700, exist_ok=True)
    require(set(p.name for p in directory.iterdir()) <= {'Dockerfile', 'ztapi-linux-amd64'}, 'nonbinary_main_build_context')
    recipe = ('FROM ' + original['Image'] + '\nCOPY ztapi-linux-amd64 /usr/local/bin/ztapi\n'
              'LABEL org.opencontainers.image.revision="' + build['commit'] + '"\n').encode()
    spending.protected_save(directory / 'Dockerfile', recipe)
    spending.protected_save(directory / 'ztapi-linux-amd64', raw, mode=0o555)
    image = 'ztapi-main:' + RELEASE
    rolling.docker('build', '--network=none', '--pull=false', '-f', str(directory / 'Dockerfile'), '-t', image, str(directory), timeout=180)
    return image


def isolated_tests(image, build):
    results = _base_isolated_tests(image, build)
    for name in ('claude-stability.test', 'controller-stability.test', 'spending.test', 'spending-audit.test'):
        path = rolling.STAGE / name
        expected = build['test_binaries'][name]
        require(rolling.sha(path.read_bytes()) == expected, 'extra_test_hash_mismatch')
        os.chmod(path, 0o555)
        args = ['-test.count=1', '-test.timeout=120s']
        if name == 'controller-stability.test':
            args.append('-test.run=^TestClaudeStability')
        output = rolling.docker('run', '--rm', '--network=none', '--read-only', '--cap-drop=ALL',
                                '--security-opt=no-new-privileges', '--memory=256m', '--cpus=1',
                                '--tmpfs', '/tmp:rw,noexec,nosuid,nodev,size=96m,mode=1777',
                                '--entrypoint', '/security.test', '--mount',
                                'type=bind,src=' + str(path) + ',dst=/security.test,readonly', image,
                                *args, timeout=140, combined=True)
        require(b'PASS' in output and b'no tests to run' not in output.lower(), 'extra_test_did_not_pass')
        results[name] = {'passed': True, 'sha256': expected, 'output_sha256': rolling.sha(output)}
    rolling.save('combined-isolated-tests.json', results)
    return results


def rebase_for_spending(baseline, config, service):
    require(service.get('State', {}).get('Running') is True, 'spending_not_running')
    require(re.fullmatch('[a-f0-9]{64}', service.get('Id', '')) is not None, 'invalid_spending_identity')
    require(service.get('Config', {}).get('Labels', {}).get('ztapi.spending.release') == spending.RELEASE,
            'unowned_spending_service')
    result = copy.deepcopy(baseline)
    require(SPENDING not in result['containers'], 'spending_already_in_main_baseline')
    result['nginx_sha256'] = rolling.sha(config)
    result['nginx_base64'] = base64.b64encode(config).decode()
    result['containers'][SPENDING] = {'id': service['Id'], 'image': service['Image'], 'started_at': service['State']['StartedAt']}
    return result


def prepare_main_stage(baseline, build):
    stage = ROOT / 'main'
    require(not stage.is_symlink(), 'unsafe_main_stage')
    stage.mkdir(mode=0o700, exist_ok=True)
    configure_main(stage)
    rolling.save('feature-owner.json', {'release': RELEASE})
    names = ['ztapi-linux-amd64', 'common-security.test', 'types-security.test',
             'claude-stability.test', 'controller-stability.test', 'spending.test', 'spending-audit.test', 'public-dist.tar.gz']
    for name in names:
        raw = (ROOT / name).read_bytes()
        expected = build['binary_sha256'] if name == 'ztapi-linux-amd64' else (
            build['public_archive_sha256'] if name == 'public-dist.tar.gz' else build['test_binaries'][name])
        require(rolling.sha(raw) == expected, 'main_stage_artifact_mismatch')
        rolling.save(name, raw)
    rolling.save('build.json', build)
    rolling.save('baseline.json', baseline)
    return stage


def prepare(baseline, build):
    require(build['tests_passed'] is True and build['paid_model_calls'] == 0, 'local_acceptance_required')
    prepare_main_stage(baseline, build)
    # Linux acceptance runs without runtime secrets or networking before a new
    # reader account or sidecar is created. Neither prepare changes live routes.
    main = rolling.prepare(baseline, build)
    alerts = spending.prepare(baseline, build, stage=ROOT / 'spending')
    result = {'prepared': True, 'release': RELEASE, 'source_commit': build['commit'],
              'spending': alerts, 'main': main, 'paid_model_calls': 0}
    save('feature-prepared.json', result)
    return result


def activate(baseline, build):
    require(not (ROOT / 'feature-published.json').exists(), 'feature_already_published')
    prepared = json.loads((ROOT / 'feature-prepared.json').read_bytes())
    require(prepared['prepared'] and prepared['source_commit'] == build['commit'], 'feature_not_prepared')
    route = spending.activate_route(baseline, stage=ROOT / 'spending')
    config = rolling.docker('exec', rolling.NGINX, 'cat', rolling.CONF)
    require(rolling.sha(config) == route['nginx_sha256'], 'spending_route_drift')
    rebased = rebase_for_spending(baseline, config, rolling.inspect(SPENDING))
    configure_main(ROOT / 'main')
    original = (rolling.STAGE / 'nginx-original.conf').read_bytes()
    require(rolling.sha(original) == baseline['nginx_sha256'], 'original_main_config_drift')
    rolling.save('nginx-before-spending-route.conf', original)
    rolling.save('baseline-before-spending-route.json', baseline)
    rolling.save('nginx-original.conf', config, replace=True)
    rolling.save('baseline.json', rebased, replace=True)
    rolling.save('feature-route-rebase.json', {'old_sha256': baseline['nginx_sha256'],
                                             'new_sha256': rebased['nginx_sha256'], 'spending': prepared['spending']['container']})
    main = rolling.activate(rebased, build)
    result = {'deployed': True, 'release': RELEASE, 'source_commit': build['commit'],
              'main': main, 'spending': route, 'paid_model_calls': 0}
    save('feature-published.json', result)
    return result


if __name__ == '__main__':
    import fcntl
    import sys
    require(os.name == 'posix' and os.geteuid() == 0, 'root_linux_required')
    require(ROOT.is_dir() and not ROOT.is_symlink() and ROOT.stat().st_mode & 0o777 == 0o700,
            'private_feature_stage_required')
    # Serialize this workflow, but let the narrow route installer take the common
    # edge lock itself. The rolling phase acquires that lock after route install.
    feature_lock = os.open(ROOT / '.feature-owner.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    fcntl.flock(feature_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    baseline = json.loads((ROOT / 'baseline.json').read_bytes())
    build = json.loads((ROOT / 'build.json').read_bytes())
    phase = sys.argv[1]
    if phase == 'prepare':
        result = prepare(baseline, build)
    elif phase == 'activate':
        lock = os.open('/run/ztapi-traffic-hold-r6/owner.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        # activate_route obtains/releases this same edge lock; acquire it only
        # around rolling.activate through a scoped wrapper below.
        original_activate = rolling.activate
        def locked_activate(*args):
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            try:
                return original_activate(*args)
            finally:
                fcntl.flock(lock, fcntl.LOCK_UN)
        rolling.activate = locked_activate
        result = activate(baseline, build)
    else:
        raise RuntimeError('unknown_feature_phase')
    print(json.dumps(result), flush=True)
