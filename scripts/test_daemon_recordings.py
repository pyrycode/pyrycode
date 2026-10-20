"""The pilot runner must reject zero, skipped and failed execution evidence."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('daemon_recordings', Path(__file__).with_name('daemon-recordings.py'))
RUNNER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUNNER)


class FakeRun:
    def __init__(self, outcomes, code=0):
        self.stdout = iter(json.dumps({'Action': result, 'Test': name}) + '\n' for name, result in outcomes)
        self.code = code

    def wait(self):
        return self.code


class RecordedEvidenceTest(unittest.TestCase):
    def execute(self, outcomes, code=0):
        process = FakeRun(outcomes, code)
        with patch.object(RUNNER.subprocess, 'Popen', return_value=process) as launch, contextlib.redirect_stdout(io.StringIO()):
            success = RUNNER.run('replay', Path('/controlled-fixtures'), None)
        return success, launch.call_args.kwargs['env']

    def test_all_counted_successful_scenarios_required(self):
        expected = [('TestRecordedClaude/' + name, 'pass') for name in RUNNER.SCENARIOS]
        success, _ = self.execute(expected)
        self.assertTrue(success)
        for outcomes, code in [([], 0), ([('TestRecordedClaude', 'pass')], 0),
                               (expected[:1], 0), (expected, 1),
                               (expected + [('TestRecordedClaude/unexpected', 'pass')], 0),
                               (expected + [expected[0]], 0),
                               (expected + [('TestRecordedClaude', 'fail')], 0),
                               ([(expected[0][0], 'skip'), expected[1]], 0),
                               ([(expected[0][0], 'fail'), expected[1]], 0)]:
            with self.subTest(outcomes=outcomes, code=code):
                success, _ = self.execute(outcomes, code)
                self.assertFalse(success)

    def test_subset_requires_all_selected_and_no_other_leaves(self):
        with patch.object(RUNNER.subprocess, 'Popen', return_value=FakeRun([('TestRecordedClaude/reply', 'pass')])), contextlib.redirect_stdout(io.StringIO()):
            self.assertTrue(RUNNER.run('replay', Path('/fixtures'), None, scenarios=['reply']))
        with patch.object(RUNNER.subprocess, 'Popen', return_value=FakeRun([('TestRecordedClaude/reply', 'pass'), ('TestRecordedClaude/two-turn', 'pass')])), contextlib.redirect_stdout(io.StringIO()):
            self.assertFalse(RUNNER.run('replay', Path('/fixtures'), None, scenarios=['reply']))

    def test_verify_uses_distinct_reports_for_baseline_named_fresh(self):
        def launch(command, **kwargs):
            name = 'TestClaudeTapeCompatibility' if '^TestClaudeTapeCompatibility$' in command else 'TestRecordedClaude'
            process = FakeRun([(name + '/' + scenario, 'pass') for scenario in RUNNER.SCENARIOS])
            if name == 'TestClaudeTapeCompatibility':
                contracts = []
                for row in RUNNER.daemon_manifest.load()['scenarios']:
                    contract = dict(scenario=row['name'], policy=row['comparison_policy'])
                    if row['comparison_policy'] == 'native_coding_prefix':
                        contract.update(baseline_coding_iterations=1, fresh_coding_iterations=2)
                    if row['comparison_policy'] == 'native_conversation_workers':
                        contract.update(baseline_workers=[], fresh_workers=[])
                    contracts.append(json.dumps(dict(Action='output', Test=name + '/' + row['name'], Output='CLAUDE_COMPARISON ' + json.dumps(contract))) + '\n')
                process.stdout = iter(contracts + list(process.stdout))
            return process

        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / 'baseline/fresh'
            output = Path(temporary) / 'verify'
            with patch.object(RUNNER.sys, 'argv', ['daemon-recordings.py', 'verify', '--directory', str(directory), '--output', str(output)]), patch.object(RUNNER.subprocess, 'Popen', side_effect=launch), patch.object(RUNNER.subprocess, 'check_output', return_value='test-revision\n'), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(0, RUNNER.main())
            self.assertEqual({'capture.json', 'saved-replay.json', 'fresh-replay.json', 'request-contract.json'},
                             {path.name for path in (output / 'reports').glob('*.json')})
            self.assertTrue(json.loads((output / 'summary.json').read_text())['success'])

    def test_comparison_requires_exact_policy_evidence(self):
        name = 'TestClaudeTapeCompatibility/native-suggestions'
        good = dict(scenario='native-suggestions', policy='native_coding_prefix', baseline_coding_iterations=1, fresh_coding_iterations=2)
        for mutate in (None, lambda r: r.update(policy='strict_ordered'), lambda r: r.update(fresh_coding_iterations=7), lambda r: r.update(fresh_coding_iterations=True), lambda r: r.update(extra='unknown')):
            row = good.copy()
            if mutate:
                mutate(row)
            process = FakeRun([(name, 'pass')])
            process.stdout = iter([json.dumps(dict(Action='output', Test=name, Output='CLAUDE_COMPARISON ' + json.dumps(row))) + '\n'] + list(process.stdout))
            with patch.object(RUNNER.subprocess, 'Popen', return_value=process), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(mutate is None, RUNNER.run('replay', Path('/baseline'), None, compare=Path('/fresh'), scenarios=['native-suggestions']))
        with patch.object(RUNNER.subprocess, 'Popen', return_value=FakeRun([(name, 'pass')])), contextlib.redirect_stdout(io.StringIO()):
            self.assertFalse(RUNNER.run('replay', Path('/baseline'), None, compare=Path('/fresh'), scenarios=['native-suggestions']))

    def test_conversation_comparison_reports_closed_actual_worker_inventories(self):
        name = 'TestClaudeTapeCompatibility/conversation-system-prompt'
        fact = dict(source_role='prompted', user_sequence=1, native_status=dict(ExitCode=0, Signal=0), production_wait=dict(status=dict(ExitCode=-1, Signal=9), group_cancel=True))
        good = dict(scenario='conversation-system-prompt', policy='native_conversation_workers', baseline_workers=[], fresh_workers=[fact])
        mutations = [None, lambda r: r.pop('baseline_workers'), lambda r: r.update(fresh_workers=None),
                     lambda r: r.update(fresh_workers=[fact, fact]), lambda r: r.update(policy='strict_ordered'),
                     lambda r: r['fresh_workers'][0].update(source_role='bootstrap'),
                     lambda r: r['fresh_workers'][0].update(user_sequence=True),
                     lambda r: r['fresh_workers'][0].update(extra='unknown'),
                     lambda r: r['fresh_workers'][0]['native_status'].update(ExitCode=7),
                     lambda r: r['fresh_workers'][0]['production_wait'].update(group_cancel=1),
                     lambda r: r['fresh_workers'][0]['production_wait']['status'].update(Signal=9.0)]
        for mutate in mutations:
            row = json.loads(json.dumps(good))
            if mutate:
                mutate(row)
            process = FakeRun([(name, 'pass')])
            process.stdout = iter([json.dumps(dict(Action='output', Test=name, Output='CLAUDE_COMPARISON ' + json.dumps(row))) + '\n'] + list(process.stdout))
            with patch.object(RUNNER.subprocess, 'Popen', return_value=process), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(mutate is None, RUNNER.run('replay', Path('/baseline'), None, compare=Path('/fresh'), scenarios=['conversation-system-prompt']))

    def test_batch_cli_runs_four_comparisons_and_one_gate_group(self):
        calls = []
        def run(mode, directory, report_dir, **kwargs):
            calls.append((mode, kwargs.get('label'), kwargs.get('scenarios')))
            if mode == 'capture':
                for name in kwargs['scenarios']:
                    (directory / (name + '.json')).write_text(json.dumps({'producer': {'revision': 'revision'}}))
            return True
        def git(command, **kwargs):
            return '' if 'status' in command else 'revision\n'
        for changed in (False, True):
            with self.subTest(changed=changed), tempfile.TemporaryDirectory() as temporary:
                output = Path(temporary) / 'batch'
                def current(command, **kwargs):
                    if changed and 'rev-parse' in command and len(calls) >= 4:
                        return 'changed\n'
                    return git(command, **kwargs)
                calls.clear()
                with patch.object(RUNNER.sys, 'argv', ['daemon-recordings.py', 'batch', '--batch', 'permissions', '--output', str(output)]), patch.object(RUNNER, 'inventory', return_value={'issues': [], 'summary': {}}), patch.object(RUNNER, 'run', side_effect=run), patch.object(RUNNER.subprocess, 'check_output', side_effect=current), patch.object(RUNNER.daemon_batch, 'gates', return_value={'success': True, 'source_revision': 'revision'}) as gates, contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(1 if changed else 0, RUNNER.main())
                self.assertEqual(4, len(calls))
                self.assertEqual(['capture', 'saved-replay', 'fresh-replay', 'request-contract'], [row[1] for row in calls])
                self.assertTrue(all(row[2] == ['permission-deny', 'permission-allow', 'permission-cancel'] for row in calls))
                self.assertEqual(0 if changed else 1, gates.call_count)
                self.assertEqual(not changed, json.loads((output / 'summary.json').read_text())['success'])

    def test_replay_drops_both_provider_credentials(self):
        with patch.dict(os.environ, {'CLAUDE_CODE_OAUTH_TOKEN': 'unit-oauth-sentinel',
                                    'ANTHROPIC_API_KEY': 'unit-api-sentinel', 'CLAUDE_CONFIG_DIR': '/private/credential-config'}, clear=True):
            _, env = self.execute([])
        self.assertFalse('CLAUDE_CODE_OAUTH_TOKEN' in env, 'Provider configuration leaked into replay environment')
        self.assertFalse('ANTHROPIC_API_KEY' in env, 'Provider configuration leaked into replay environment')
        self.assertFalse('CLAUDE_CONFIG_DIR' in env, 'Provider configuration leaked into replay environment')
        self.assertEqual('replay', env['PYRY_CLAUDE_TAPE_MODE'])
        self.assertEqual(list(RUNNER.SCENARIOS), json.loads(env['PYRY_CLAUDE_TAPE_SCENARIOS']))

    def test_inventory_accounts_for_pilot_and_original_suite(self):
        inventory = RUNNER.inventory()
        self.assertEqual(list(RUNNER.SCENARIOS), inventory['recorded_scenarios'])
        self.assertTrue(inventory['existing_tests'])
        self.assertIn('not executed acceptance', inventory['inventory_kind'])
        self.assertTrue(any(row['name'] == 'TestInteractiveStreamMultiTurnContinuity'
                            for row in inventory['existing_tests']))


if __name__ == '__main__':
    unittest.main()
