#!/usr/bin/env python3
"""Capture and replay Claude stdio against a real isolated daemon."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
SCENARIOS = ('two-turn', 'permission-deny', 'reply', 'question-answer', 'question-refuse', 'permission-allow', 'permission-cancel')
DEFAULT = ROOT / 'internal/e2e/testdata/claude-tapes'


def inventory():
    rows = []
    for path in sorted((ROOT / 'internal/e2e/realclaude').glob('*.go')):
        source = path.read_text()
        live = source.startswith('//go:build e2e_realclaude')
        for name in re.findall(r'^func (Test\w+)\(t \*testing\.T\)', source, re.M):
            rows.append({'name': name, 'file': str(path.relative_to(ROOT)),
                         'selection': 'live-tagged (includes gated probes)' if live else 'offline validation'})
    return {'inventory_kind': 'static declarations, not executed acceptance',
            'pilot_scenarios': list(SCENARIOS), 'existing_tests': rows}


def run(mode, directory, report_dir, compare=None, label=None):
    env = os.environ.copy()
    env['PYRY_CLAUDE_TAPE_MODE'] = mode
    env['PYRY_CLAUDE_TAPE_DIR'] = str(directory.resolve())
    name = 'TestRecordedClaude'
    if compare:
        env['PYRY_CLAUDE_TAPE_FRESH'] = str(compare.resolve())
        name = 'TestClaudeTapeCompatibility'
    else:
        env.pop('PYRY_CLAUDE_TAPE_FRESH', None)
    if mode == 'replay':
        for key in ('CLAUDE_CODE_OAUTH_TOKEN', 'ANTHROPIC_API_KEY'):
            env.pop(key, None)
    cmd = [sys.executable, str(ROOT / 'scripts/test-admission.py'), '--', 'go', 'test',
           '-tags=e2e', '-count=1', '-timeout=10m', '-json', '-run', '^' + name + '$', './internal/e2e']
    started = time.monotonic()
    # Preserve the admission descriptor when an operator reserves one slot for
    # the whole compatibility command. The nested launcher validates its inode.
    inherited = ()
    try:
        descriptor = int(env.get('PYRY_TEST_ADMISSION_FD', '-1'))
        os.fstat(descriptor)
        inherited = (descriptor,)
    except (ValueError, OSError):
        pass
    process = subprocess.Popen(cmd, cwd=ROOT, env=env, stdout=subprocess.PIPE,
                               stderr=subprocess.STDOUT, text=True, pass_fds=inherited)
    leaves, events, durations = {}, [], {}
    for line in process.stdout:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            print(line, end='', flush=True)
            continue
        events.append(event)
        if event.get('Action') in ('pass', 'fail', 'skip') and event.get('Test', '').startswith(name + '/'):
            leaves[event['Test']] = event['Action']
            durations[event['Test']] = event.get('Elapsed')
        if event.get('Action') == 'output':
            print(event.get('Output', ''), end='', flush=True)
    code = process.wait()
    expected = {name + '/' + scenario for scenario in SCENARIOS}
    success = code == 0 and set(leaves) == expected and all(result == 'pass' for result in leaves.values())
    report = {'mode': 'compare' if compare else mode, 'success': success,
              'seconds': round(time.monotonic() - started, 3), 'results': leaves, 'scenario_seconds': durations, 'exit_code': code}
    if report_dir:
        report_dir.mkdir(parents=True, exist_ok=True)
        label = label or report['mode'] + '-' + directory.name
        # Reports are separate from fixtures and retain failures, too.
        with (report_dir / (label + '.json')).open('x') as handle:
            json.dump({'summary': report, 'events': events}, handle, indent=2)
    print(json.dumps(report), flush=True)
    return success


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=('inventory', 'replay', 'capture', 'verify'))
    parser.add_argument('--directory', type=Path, default=DEFAULT)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    if args.command == 'inventory':
        print(json.dumps(inventory(), indent=2))
        return 0
    if args.command == 'replay':
        return 0 if run('replay', args.directory, args.output) else 1
    if not args.output:
        parser.error('capture and verify require a new --output directory outside the checkout')
    output = args.output.resolve()
    if output == ROOT or ROOT in output.parents:
        parser.error('live output must be outside the source checkout')
    output.mkdir(parents=True, exist_ok=False)
    fresh = output / 'fresh'
    fresh.mkdir()
    reports = output / 'reports'
    capture = run('capture', fresh, reports, label='capture')
    if args.command == 'capture':
        return 0 if capture else 1
    # Keep execution results distinct. A comparison failure never erases a
    # successful replay and cannot produce a successful compatibility receipt.
    saved_replay = run('replay', args.directory, reports, label='saved-replay')
    fresh_replay = run('replay', fresh, reports, label='fresh-replay') if capture else False
    comparison = run('replay', args.directory, reports, compare=fresh, label='request-contract') if capture else False
    summary = {'capture': capture, 'saved_replay': saved_replay, 'fresh_replay': fresh_replay,
               'request_contract': comparison, 'success': all((capture, saved_replay, fresh_replay, comparison))}
    with (output / 'summary.json').open('x') as handle:
        json.dump(summary, handle, indent=2)
    return 0 if summary['success'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
