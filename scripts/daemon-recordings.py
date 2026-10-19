#!/usr/bin/env python3
"""Capture and replay Claude stdio against a real isolated daemon."""
import argparse
import hashlib
from collections import Counter
import daemon_inventory
import daemon_manifest
import daemon_batch
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
SCENARIOS = tuple(row['name'] for row in daemon_manifest.load()['scenarios'])
DEFAULT = ROOT / 'internal/e2e/testdata/claude-tapes'


def inventory():
    return daemon_inventory.inventory(ROOT, daemon_manifest.load())


def run(mode, directory, report_dir, compare=None, label=None, scenarios=None):
    scenarios = list(SCENARIOS) if scenarios is None else scenarios
    env = os.environ.copy()
    env['PYRY_CLAUDE_TAPE_SCENARIOS'] = json.dumps(scenarios)
    env['PYRY_CLAUDE_TAPE_MODE'] = mode
    env['PYRY_CLAUDE_TAPE_DIR'] = str(directory.resolve())
    name = 'TestRecordedClaude'
    if compare:
        env['PYRY_CLAUDE_TAPE_FRESH'] = str(compare.resolve())
        name = 'TestClaudeTapeCompatibility'
    else:
        env.pop('PYRY_CLAUDE_TAPE_FRESH', None)
    if mode == 'replay':
        for key in ('CLAUDE_CODE_OAUTH_TOKEN', 'ANTHROPIC_API_KEY', 'CLAUDE_CONFIG_DIR'):
            env.pop(key, None)
    cmd = [sys.executable, str(ROOT / 'scripts/test-admission.py'), '--', 'go', 'test',
           '-tags=e2e', '-count=1', '-timeout=' + ('30m' if 'native-suggestions' in scenarios else '10m'), '-json', '-run', '^' + name + '$', './internal/e2e']
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
    leaves, events, durations, counts = {}, [], {}, Counter()
    rejected = False
    for line in process.stdout:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            print(line, end='', flush=True)
            continue
        events.append(event)
        if event.get('Action') in ('fail', 'skip'):
            rejected = True
        if event.get('Action') in ('pass', 'fail', 'skip') and event.get('Test', '').startswith(name + '/'):
            counts[event['Test']] += 1
            leaves[event['Test']] = event['Action']
            durations[event['Test']] = event.get('Elapsed')
        if event.get('Action') == 'output':
            print(event.get('Output', ''), end='', flush=True)
    code = process.wait()
    expected = {name + '/' + scenario for scenario in scenarios}
    success = code == 0 and not rejected and set(leaves) == expected and all(result == 'pass' for result in leaves.values()) and all(value == 1 for value in counts.values())
    report = {'mode': 'compare' if compare else mode, 'success': success,
              'seconds': round(time.monotonic() - started, 3), 'results': leaves, 'scenario_seconds': durations, 'exit_code': code, 'scenarios': scenarios}
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
    parser.add_argument('command', choices=('inventory', 'plan', 'scaffold', 'replay', 'capture', 'verify', 'batch'))
    parser.add_argument('--directory', type=Path, default=DEFAULT)
    parser.add_argument('--output', type=Path)
    selection = parser.add_mutually_exclusive_group()
    selection.add_argument('--batch')
    selection.add_argument('--scenario', action='append')
    parser.add_argument('--name', help='New scaffold scenario name')
    parser.add_argument('--live-test', action='append', default=[])
    parser.add_argument('--retained-live', action='append', default=[])
    parser.add_argument('--agents-repo', type=Path)
    parser.add_argument('--stdio-permission', action='store_true')
    args = parser.parse_args()
    try:
        manifest = daemon_manifest.load()
        scenarios = daemon_manifest.select(manifest, args.batch, args.scenario)
        if args.command in ('inventory', 'plan', 'scaffold', 'batch'):
            audit = inventory()
            if args.command == 'inventory':
                print(json.dumps(audit, indent=2))
                return 1 if audit['issues'] else 0
            if audit['issues']:
                raise ValueError('; '.join(audit['issues']))
            if args.command == 'plan':
                print(json.dumps(daemon_batch.plan(ROOT, manifest, scenarios, audit), indent=2))
                return 0
            if args.command == 'scaffold':
                daemon_batch.scaffold(ROOT, args, audit)
                return 0
        if args.command == 'replay':
            return 0 if run('replay', args.directory, args.output, scenarios=scenarios) else 1
        if not args.output:
            parser.error('capture, verify and batch require a new --output directory outside the checkout')
        output = args.output.resolve()
        if output == ROOT or ROOT in output.parents:
            parser.error('live output must be outside the source checkout')
        revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
        if args.command == 'batch' and subprocess.check_output(['git', 'status', '--porcelain'], cwd=ROOT, text=True):
            raise ValueError('Commit clean source before a batch run')
        output.mkdir(parents=True, exist_ok=False)
        fresh = output / 'fresh'
        fresh.mkdir()
        reports = output / 'reports'
        capture = run('capture', fresh, reports, label='capture', scenarios=scenarios)
        if args.command == 'capture':
            return 0 if capture else 1
        saved_replay = run('replay', args.directory, reports, label='saved-replay', scenarios=scenarios)
        fresh_replay = run('replay', fresh, reports, label='fresh-replay', scenarios=scenarios) if capture else False
        comparison = run('replay', args.directory, reports, compare=fresh, label='request-contract', scenarios=scenarios) if capture else False
        summary = {'capture': capture, 'saved_replay': saved_replay, 'fresh_replay': fresh_replay,
                   'request_contract': comparison, 'scenarios': scenarios, 'source_revision': revision,
                   'success': all((capture, saved_replay, fresh_replay, comparison))}
        if args.command == 'batch' and summary['success']:
            summary['plan'] = daemon_batch.plan(ROOT, manifest, scenarios, audit)
            summary['provenance'] = daemon_batch.provenance(ROOT, revision, scenarios, fresh)
            if summary['provenance']['success']:
                summary['gates'] = daemon_batch.gates(ROOT, manifest, scenarios, output, args.agents_repo)
                summary['success'] = summary['gates']['success'] and summary['gates']['source_revision'] == revision
            else:
                summary['success'] = False
        with (output / 'summary.json').open('x') as handle:
            json.dump(summary, handle, indent=2)
        return 0 if summary['success'] else 1
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(str(error), file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
