import copy
import contextlib
import io
import json
import os
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import patch

import daemon_batch as batch
import daemon_manifest as manifest
import daemon_inventory as inventory


class ManifestTest(unittest.TestCase):
    def test_selection_and_schema_reject_drift(self):
        value = manifest.load()
        self.assertEqual(['permission-deny', 'permission-allow', 'permission-cancel'], manifest.select(value, 'permissions'))
        for group, names in [('unknown', None), (None, []), (None, ['reply', 'reply']), (None, ['../reply']), ('core', ['reply'])]:
            with self.assertRaises(ValueError):
                manifest.select(value, group, names)
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'manifest.json'
            for mutate in (lambda m: m['scenarios'].append(m['scenarios'][0]),
                           lambda m: m['batches'].update(broken=['missing']),
                           lambda m: m['scenarios'][0].update(retained_live=[]),
                           lambda m: m['scenarios'][0].update(unexpected=True),
                           lambda m: m['scenarios'][0].update(claude_args=None),
                           lambda m: m['scenarios'][0].update(claude_args=['']),
                           lambda m: m['scenarios'][0].update(claude_args=[1]),
                           lambda m: m['scenarios'][2].update(live_contracts={})):
                bad = copy.deepcopy(value)
                mutate(bad)
                path.write_text(json.dumps(bad))
                with self.assertRaises(ValueError):
                    manifest.load(path)


class InventoryTest(unittest.TestCase):
    def test_transitive_dependency_fingerprint_and_no_path_uncertainty(self):
        def node(name, refs=(), command=False, test=False):
            return dict(name=name, references=list(refs), provider_command=command, test=test,
                        file='cases_test.go', live_tagged=True, env_gates=[], source_sha256=name, context_sha256='package context')
        functions = [node('TestLive', ['wrapper'], test=True), node('wrapper', ['provider']), node('provider', command=True),
                     node('TestControl', test=True), node('TestStubHelperProcess', test=True)]
        nodes = {row['name']: row for row in functions}
        value = dict(provider_roots=['provider'], scenarios=[dict(name='one', live_tests=['TestLive'], live_contracts={
            'TestLive': inventory.contract_digest(nodes, inventory.closure(nodes, 'TestLive'))})], reviews={})
        result = inventory.inventory(Path('.'), value, functions)
        rows = {row['name']: row for row in result['existing_tests']}
        self.assertEqual(['TestLive', 'wrapper', 'provider'], rows['TestLive']['provider_path'])
        self.assertEqual('no_known_provider_path', rows['TestControl']['dependency'])
        self.assertEqual('process_helper', rows['TestStubHelperProcess']['dependency'])
        self.assertIsNone(result['summary']['total_replay_eligible'])
        self.assertFalse(result['issues'])
        nodes['provider']['source_sha256'] = 'changed helper body'
        stale = inventory.inventory(Path('.'), value, functions)
        self.assertEqual(0, stale['summary']['shared_live_entries'])
        self.assertIn('Source changed since boundary review: TestLive', stale['issues'])


class BatchTest(unittest.TestCase):
    def test_once_per_batch_gates_preserve_counts_and_offline_isolation(self):
        value = manifest.load()
        selected = manifest.select(value, 'permissions')
        roots = sorted({name for row in value['scenarios'] if row['name'] in selected for name in row['live_tests']})
        commands = []
        def call(command, **kwargs):
            commands.append(command)
            if 'check-common' in command:
                self.assertFalse('CLAUDE_CODE_OAUTH_TOKEN' in kwargs['env'], 'Provider credential inherited by offline gate')
                self.assertFalse('ANTHROPIC_API_KEY' in kwargs['env'], 'Provider credential inherited by offline gate')
                self.assertFalse(any(k.startswith('PYRY_CLAUDE_TAPE_') for k in kwargs['env']))
            names = roots if '-tags=e2e_realclaude' in command else [f'TestCheckpointResumeCorpus/cut-{i}' for i in range(201)] if '^TestCheckpointResumeCorpus$' in command else []
            for name in names:
                kwargs['stdout'].write(json.dumps(dict(Action='pass', Test=name)) + '\n')
            return 0
        def output(command, **kwargs):
            return '' if 'status' in command else 'revision\n'
        with tempfile.TemporaryDirectory() as temporary, patch.object(batch.subprocess, 'call', side_effect=call), patch.object(batch.subprocess, 'check_output', side_effect=output), patch.dict(os.environ, {'CLAUDE_CODE_OAUTH_TOKEN':'sentinel', 'ANTHROPIC_API_KEY':'sentinel', 'PYRY_CLAUDE_TAPE_SCENARIOS':'["reply"]'}), contextlib.redirect_stdout(io.StringIO()):
            result = batch.gates(Path(temporary), value, selected, Path(temporary), None)
        self.assertTrue(result['success'])
        self.assertEqual(4, len(commands))
        self.assertEqual(1, sum('check-common' in command for command in commands))
        self.assertEqual(roots, result['live_selection'])
        self.assertEqual(201, len(result['stages'][2]['evidence']['results']))

    def test_zero_duplicate_skipped_and_foreign_evidence_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'events'
            for events in ([], [('pass','other')], [('skip','root')], [('pass','root'),('pass','root')], [('pass','root'),('fail','')]):
                path.write_text(''.join(json.dumps(dict(Action=a, Test=n))+'\n' for a,n in events))
                self.assertFalse(batch.counted(path, ['root'])['success'])
            path.write_text(json.dumps(dict(Action='pass', Test='root'))+'\n')
            self.assertTrue(batch.counted(path, ['root'])['success'])

    def test_failed_gate_stops_later_stages(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(batch.subprocess, 'call', return_value=1) as run, patch.object(batch.subprocess, 'check_output', side_effect=['revision\n','revision\n','']), contextlib.redirect_stdout(io.StringIO()):
            result = batch.gates(Path(temporary), manifest.load(), ['reply'], Path(temporary), None)
        self.assertFalse(result['success'])
        self.assertEqual(1, run.call_count)

    def test_provenance_rejects_missing_or_different_revision(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(batch.subprocess, 'check_output', return_value='revision\n'):
            path = Path(temporary)
            self.assertFalse(batch.provenance(path, 'revision', ['one'], path)['success'])
            (path/'one.json').write_text(json.dumps(dict(producer=dict(revision='other'))))
            self.assertFalse(batch.provenance(path, 'revision', ['one'], path)['success'])
            (path/'one.json').write_text(json.dumps(dict(producer=dict(revision='revision'))))
            self.assertTrue(batch.provenance(path, 'revision', ['one'], path)['success'])

    def test_scaffold_is_a_failing_proposal_and_never_overwrites(self):
        audit = dict(recorded_scenarios=['existing'], existing_tests=[dict(name='TestCandidate', disposition='candidate', dependency='possible_provider_dependency', contract_sha256='a'*64)])
        with tempfile.TemporaryDirectory() as temporary, contextlib.redirect_stdout(io.StringIO()):
            root = Path(temporary)/'repo'
            output = Path(temporary)/'proposal'
            args = SimpleNamespace(name='queue-next', live_test=['TestCandidate'], retained_live=['Actual Bash and elapsed time remain live'], output=output, stdio_permission=False)
            batch.scaffold(root, args, audit)
            self.assertIn('t.Fatal(', (output/'recorded_queue_next_test.go').read_text())
            self.assertEqual(['TestCandidate'], json.loads((output/'manifest-fragment.json').read_text())['scenario']['live_tests'])
            with self.assertRaises(FileExistsError):
                batch.scaffold(root, args, audit)
            args.name='../escape'
            with self.assertRaises(ValueError):
                batch.scaffold(root, args, audit)
