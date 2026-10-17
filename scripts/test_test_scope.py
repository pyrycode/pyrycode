import fcntl
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


class ScopeTests(unittest.TestCase):
    def test_full_builder_go_is_rejected_before_wait_or_inherited_admission(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            scripts = root / 'scripts'
            scripts.mkdir()
            for name in ['test-admission.py', 'test-scope.py']:
                source = Path(__file__).with_name(name)
                if source.exists():
                    shutil.copy(source, scripts / name)
            agents = root / 'agents'
            (agents / 'bin').mkdir(parents=True)
            # The canonical consumer bridge represents the frozen builder
            # decision here. The shared policy has its own real socket tests.
            (agents / 'bin/check-test-selection').write_text("def check_selection(root, kind, args):\n    if kind == 'go' and args == ['test', './...']:\n        raise SystemExit('Test selection policy: full acceptance belongs to the dispatcher/verifier')\n")
            marker = root / 'started'
            go = root / 'go'
            go.write_text('#!/bin/sh\ntouch "' + str(marker) + '"\n')
            go.chmod(0o755)
            env = dict(os.environ, AGENTS_REPO_PATH=str(agents), PYRY_TEST_ADMISSION_LOCK=str(root / 'hold'))
            with (root / 'hold').open('w') as hold:
                fcntl.flock(hold, fcntl.LOCK_EX)
                for inherited in [False, True]:
                    selected = dict(env)
                    if inherited:
                        selected['PYRY_TEST_ADMISSION_FD'] = str(hold.fileno())
                    result = subprocess.run([sys.executable, str(scripts / 'test-admission.py'), '--', str(go), 'test', './...'], env=selected, pass_fds=(hold.fileno(),) if inherited else (), capture_output=True, text=True, timeout=2)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn('full acceptance belongs', result.stderr)
                    self.assertNotIn('waiting', result.stderr)
                    self.assertFalse(marker.exists())

    def test_marked_worktree_without_updated_consumer_fails_closed(self):
        import runpy
        from unittest.mock import patch
        guard_path = Path(__file__).with_name('test-scope.py')
        self.assertTrue(guard_path.exists(), 'product compatibility guard must exist')
        guard = runpy.run_path(str(guard_path))['check_selection']
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / '.git').mkdir()
            with patch.dict(os.environ, {'AGENTS_REPO_PATH': str(root / 'missing-agents')}):
                guard(root, 'go', ['test', './...'])
                (root / '.git/pyry-test-managed').touch()
                with self.assertRaises(SystemExit):
                    guard(root, 'go', ['test', './...'])


if __name__ == '__main__':
    unittest.main()
