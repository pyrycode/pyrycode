"""Batch planning, fail-closed scaffolding and once-per-batch regression gates."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time


def inherited_admission():
    try:
        fd = int(os.environ.get('PYRY_TEST_ADMISSION_FD', '-1'))
        os.fstat(fd)
        return (fd,)
    except (ValueError, OSError):
        return ()


def plan(root, manifest, scenarios, audit):
    selected = [row for row in manifest['scenarios'] if row['name'] in scenarios]
    return {'scenarios': scenarios,
            'boundaries': {row['name']: row['boundary'] for row in selected},
            'live_tests': sorted({name for row in selected for name in row['live_tests']}),
            'retained_live': {row['name']: row['retained_live'] for row in selected},
            'inventory_summary': audit['summary'],
            'manifest_sha256': hashlib.sha256((root / 'internal/e2e/testdata/claude-scenarios.json').read_bytes()).hexdigest(),
            'gates_once': ['check-common', 'selected-live-tests', 'restoration-201', 'citation-and-doc-guards'],
            'promotion': 'Inspect successful candidates, implement/review shared assertions, commit fixtures, then run batch. No automatic fixture replacement or live-test retirement.'}


def scaffold(root, args, audit):
    if not args.name or not re.fullmatch(r'[a-z][a-z0-9-]*', args.name) or args.name in audit['recorded_scenarios']:
        raise ValueError('Scaffold requires a new safe --name')
    indexed = {row['name']: row for row in audit['existing_tests']}
    if not args.live_test or len(set(args.live_test)) != len(args.live_test) or any(name not in indexed for name in args.live_test):
        raise ValueError('Scaffold requires unique existing --live-test names')
    if any(indexed[name]['disposition'] in ('live_only', 'shared_replay') or indexed[name]['dependency'] == 'process_helper' for name in args.live_test):
        raise ValueError('Cannot scaffold live-only, already-owned or process-helper entries')
    if not args.retained_live or any(not text.strip() for text in args.retained_live):
        raise ValueError('Scaffold requires explicit --retained-live obligations')
    if not args.output:
        raise ValueError('Scaffold requires a new --output directory outside the checkout')
    output = args.output.resolve()
    if output == root or root in output.parents:
        raise ValueError('Scaffold proposals must be outside the checkout')
    output.mkdir(parents=True, exist_ok=False)
    row = {'boundary': 'daemon_stdio', 'comparison_policy': 'strict_ordered', 'name': args.name, 'stdio_permission': args.stdio_permission, 'initial_yolo': False, 'claude_args': [],
           'live_tests': args.live_test, 'retained_live': args.retained_live,
           'live_contracts': {name: indexed[name]['contract_sha256'] for name in args.live_test}}
    (output / 'manifest-fragment.json').write_text(json.dumps({'scenario': row, 'batch': {args.name: [args.name]}}, indent=2) + '\n')
    source = '''//go:build e2e

package e2e

import "testing"

func init() {
 recordedScenarioDrivers["NAME"] = func(t *testing.T, c recordedScenarioContext) {
  t.Helper()
  t.Fatal("Implement and review the shared NAME assertions and external-input checkpoints before recording")
 }
}
'''.replace('NAME', args.name)
    (output / ('recorded_' + args.name.replace('-', '_') + '_test.go')).write_text(source)
    (output / 'REVIEW.txt').write_text('''This is a failing proposal, not a migrated test.
1. Map every original assertion, external input and physical/timing witness.
2. Implement the shared live/capture/replay driver. Select correct permission/bypass/spawn configuration.
3. Add meaningful positive/negative evidence controls; retain all live obligations.
4. Merge the manifest fragment and driver, review updated dependency fingerprints, commit source.
5. Capture the named scenario into a new directory; inspect privacy/provenance/completeness.
6. Commit inspected candidates without replacing existing fixtures. Run the complete batch workflow once.
No live tests are deleted, retagged or retired by this tool.
''')
    print(json.dumps({'proposal': str(output), 'scenario': args.name, 'implemented': False}))


def events(path):
    for line in path.read_text().splitlines():
        try:
            yield json.loads(line)
        except ValueError:
            continue


def counted(path, roots=None, cuts=False):
    finished = {}
    duplicates = False
    rejected = False
    for event in events(path):
        action, name = event.get('Action'), event.get('Test', '')
        if action in ('fail', 'skip'):
            rejected = True
        if action not in ('pass', 'fail', 'skip') or not name:
            continue
        if (cuts and name.startswith('TestCheckpointResumeCorpus/')) or (not cuts and name in roots):
            duplicates |= name in finished
            finished[name] = action
    expected = len(finished) == 201 if cuts else set(finished) == set(roots)
    return {'success': bool(expected and not rejected and not duplicates and all(x == 'pass' for x in finished.values())),
            'results': finished}


def gates(root, manifest, scenarios, output, agents_repo):
    live = os.environ.copy()
    for key in list(live):
        if key.startswith('PYRY_CLAUDE_TAPE_'):
            live.pop(key)
    offline = live.copy()
    for key in ('CLAUDE_CODE_OAUTH_TOKEN', 'ANTHROPIC_API_KEY', 'CLAUDE_CONFIG_DIR'):
        offline.pop(key, None)
    live['PYRY_REALCLAUDE_EVIDENCE_DIR'] = str(output / 'live-evidence')
    logs = output / 'gates'
    logs.mkdir()
    stages = []
    revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()

    def stage(name, command, env, roots=None, cuts=False):
        started = time.monotonic()
        path = logs / (name + '.log')
        with os.fdopen(os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), 'w') as log:
            code = subprocess.call([sys.executable, str(root / 'scripts/test-admission.py'), '--'] + command,
                                   cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT,
                                   pass_fds=inherited_admission())
        result = {'stage': name, 'exit_code': code, 'seconds': round(time.monotonic() - started, 3), 'success': code == 0}
        if roots is not None or cuts:
            result['evidence'] = counted(path, roots, cuts)
            result['success'] &= result['evidence']['success']
        stages.append(result)
        (logs / 'stages.json').write_text(json.dumps({'source_revision': revision, 'stages': stages}, indent=2) + '\n')
        notice = {key: result[key] for key in ('stage', 'exit_code', 'seconds', 'success')}
        if 'evidence' in result:
            notice['executed'] = len(result['evidence']['results'])
        print(json.dumps(notice), flush=True)
        return result['success']

    success = stage('check-common', ['make', 'check-common'], offline)
    roots = sorted({name for row in manifest['scenarios'] if row['name'] in scenarios for name in row['live_tests']})
    if success and roots:
        pattern = '^(' + '|'.join(roots) + ')$'
        success = stage('selected-live', ['go', 'test', '-race', '-tags=e2e_realclaude', '-count=1', '-timeout=180m', '-json', '-run', pattern, './internal/e2e/realclaude'], live, roots=roots)
    if success:
        # The older check-restoration wrapper does not preserve an outer admission FD.
        success = stage('restoration', ['go', 'test', '-race', '-count=1', '-timeout=30m', '-json', '-run', '^TestCheckpointResumeCorpus$', './internal/thread'], dict(offline, PYRY_EXHAUSTIVE_TESTS='1'), cuts=True)
    if success:
        env = offline.copy()
        if agents_repo:
            env['AGENTS_REPO_PATH'] = str(agents_repo.resolve())
        success = stage('guards', ['make', 'cite-guard', 'docs-guard'], env)
    final_revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()
    dirty = subprocess.check_output(['git', 'status', '--porcelain'], cwd=root, text=True)
    return {'success': success and revision == final_revision and not dirty,
            'source_revision': revision, 'stages': stages,
            'live_selection': roots, 'live_selection_applicable': bool(roots)}


def provenance(root, revision, scenarios, fresh):
    revisions = {}
    try:
        for name in scenarios:
            revisions[name] = json.loads((fresh / (name + '.json')).read_text())['producer']['revision']
        current = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()
        success = current == revision and all(value == revision for value in revisions.values())
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError):
        success = False
    return {'success': success, 'expected_revision': revision, 'producer_revisions': revisions}
