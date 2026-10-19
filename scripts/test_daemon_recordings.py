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

    def test_two_counted_successful_scenarios_required(self):
        expected = [('TestRecordedClaude/' + name, 'pass') for name in RUNNER.SCENARIOS]
        success, _ = self.execute(expected)
        self.assertTrue(success)
        for outcomes, code in [([], 0), ([('TestRecordedClaude', 'pass')], 0),
                               (expected[:1], 0), (expected, 1),
                               (expected + [('TestRecordedClaude/unexpected', 'pass')], 0),
                               ([(expected[0][0], 'skip'), expected[1]], 0),
                               ([(expected[0][0], 'fail'), expected[1]], 0)]:
            with self.subTest(outcomes=outcomes, code=code):
                success, _ = self.execute(outcomes, code)
                self.assertFalse(success)

    def test_verify_uses_distinct_reports_for_baseline_named_fresh(self):
        def launch(command, **kwargs):
            name = 'TestClaudeTapeCompatibility' if '^TestClaudeTapeCompatibility$' in command else 'TestRecordedClaude'
            return FakeRun([(name + '/' + scenario, 'pass') for scenario in RUNNER.SCENARIOS])

        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / 'baseline/fresh'
            output = Path(temporary) / 'verify'
            with patch.object(RUNNER.sys, 'argv', ['daemon-recordings.py', 'verify', '--directory', str(directory), '--output', str(output)]), patch.object(RUNNER.subprocess, 'Popen', side_effect=launch), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(0, RUNNER.main())
            self.assertEqual({'capture.json', 'saved-replay.json', 'fresh-replay.json', 'request-contract.json'},
                             {path.name for path in (output / 'reports').glob('*.json')})
            self.assertTrue(json.loads((output / 'summary.json').read_text())['success'])

    def test_replay_drops_both_provider_credentials(self):
        with patch.dict(os.environ, {'CLAUDE_CODE_OAUTH_TOKEN': 'unit-oauth-sentinel',
                                    'ANTHROPIC_API_KEY': 'unit-api-sentinel'}):
            _, env = self.execute([])
        self.assertNotIn('CLAUDE_CODE_OAUTH_TOKEN', env)
        self.assertNotIn('ANTHROPIC_API_KEY', env)
        self.assertEqual('replay', env['PYRY_CLAUDE_TAPE_MODE'])

    def test_inventory_accounts_for_pilot_and_original_suite(self):
        inventory = RUNNER.inventory()
        self.assertEqual(list(RUNNER.SCENARIOS), inventory['pilot_scenarios'])
        self.assertTrue(inventory['existing_tests'])
        self.assertIn('not executed acceptance', inventory['inventory_kind'])
        self.assertTrue(any(row['name'] == 'TestInteractiveStreamMultiTurnContinuity'
                            for row in inventory['existing_tests']))


if __name__ == '__main__':
    unittest.main()
