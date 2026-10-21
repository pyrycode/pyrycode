import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch
import importlib.util


WRAPPER = Path(__file__).with_name("test-admission.py")


class AdmissionTests(unittest.TestCase):
    def test_wait_reports_survive_redirected_output_and_end_before_test_execution(self):
        import fcntl
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            # The helper protocol is dispatcher-owned. This fixture exposes
            # exactly when admission calls it, independently of printed output.
            agents = root / 'agents'
            helper = agents / 'dispatcher/scripts/test-wait.py'
            helper.parent.mkdir(parents=True)
            helper.write_text("from pathlib import Path\nimport os\nclass AdmissionWait:\n def heartbeat(self): Path(os.environ['PYRY_TEST_WAIT_DIRECTORY'], 'waiting').touch()\n def finish(self): Path(os.environ['PYRY_TEST_WAIT_DIRECTORY'], 'acquired').touch()\n")
            env = dict(os.environ, AGENTS_REPO_PATH=str(agents), PYRY_TEST_WAIT_DIRECTORY=str(root), PYRY_TEST_ADMISSION_LOCK=str(root / 'hold'))
            with (root / 'hold').open('w') as locked, (root / 'output').open('w') as output:
                fcntl.flock(locked, fcntl.LOCK_EX)
                code = "import os; from pathlib import Path; assert Path(os.environ['PYRY_TEST_WAIT_DIRECTORY'], 'acquired').exists()"
                waiter = subprocess.Popen([sys.executable, str(WRAPPER), '--', sys.executable, '-c', code], env=env, stdout=output, stderr=output)
                try:
                    deadline = time.monotonic() + 3
                    while not (root / 'waiting').exists() and time.monotonic() < deadline:
                        time.sleep(.01)
                    self.assertTrue((root / 'waiting').exists())
                    self.assertFalse((root / 'acquired').exists())
                    fcntl.flock(locked, fcntl.LOCK_UN)
                    self.assertEqual(waiter.wait(timeout=5), 0)
                finally:
                    if waiter.poll() is None:
                        waiter.kill()
                        waiter.wait(timeout=5)

    def test_darwin_signal_to_exited_group_is_allowed_only_without_live_members(self):
        spec = importlib.util.spec_from_file_location("admission", WRAPPER)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with patch.object(module.sys, "platform", "darwin"), patch.object(module.os, "killpg", side_effect=PermissionError), patch.object(module, "group_alive", return_value=False):
            module.signal_group(123, signal.SIGTERM)
        with patch.object(module.sys, "platform", "darwin"), patch.object(module.os, "killpg", side_effect=PermissionError), patch.object(module, "group_alive", return_value=True):
            with self.assertRaises(PermissionError):
                module.signal_group(123, signal.SIGTERM)

    def test_competing_checks_serialize_and_nested_shells_do_not_deadlock(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, PYRY_TEST_ADMISSION_LOCK=str(root / "hold"))
            program = "import sys,time; from pathlib import Path; p=Path(sys.argv[1]); p.write_text(str(time.monotonic())); time.sleep(.2); p.with_suffix('.end').write_text(str(time.monotonic()))"
            one = subprocess.Popen([sys.executable, str(WRAPPER), "--", sys.executable, "-c", program, str(root / "one")], env=env)
            two = subprocess.Popen([sys.executable, str(WRAPPER), "--", sys.executable, "-c", program, str(root / "two")], env=env)
            self.assertEqual(one.wait(timeout=5), 0)
            self.assertEqual(two.wait(timeout=5), 0)
            first, second = sorted([root / "one", root / "two"], key=lambda p: float(p.read_text()))
            self.assertLessEqual(float(first.with_suffix('.end').read_text()), float(second.read_text()))
            nested = subprocess.run([sys.executable, str(WRAPPER), "--", "sh", "-c", 'exec "$1" "$2" -- "$1" -c "print(42)"', "sh", sys.executable, str(WRAPPER)], env=env, capture_output=True, text=True, timeout=5)
            self.assertEqual(nested.returncode, 0, nested.stderr)
            self.assertEqual(nested.stdout.strip(), "42")

    def test_stale_inherited_descriptor_and_failed_exec_cannot_keep_a_place(self):
        with tempfile.TemporaryDirectory() as directory:
            env = dict(os.environ, PYRY_TEST_ADMISSION_LOCK=str(Path(directory) / "hold"), PYRY_TEST_ADMISSION_FD="99999")
            failed = subprocess.run([sys.executable, str(WRAPPER), "--", "/missing-command"], env=env, capture_output=True, timeout=5)
            self.assertNotEqual(failed.returncode, 0)
            good = subprocess.run([sys.executable, str(WRAPPER), "--", "true"], env=env, timeout=5)
            self.assertEqual(good.returncode, 0)

    def test_termination_stops_child_before_next_check(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, PYRY_TEST_ADMISSION_LOCK=str(root / "hold"))
            code = "import os,sys,time; from pathlib import Path; Path(sys.argv[1]).write_text(str(os.getpid())); time.sleep(60)"
            owner = subprocess.Popen([sys.executable, str(WRAPPER), "--", sys.executable, "-c", code, str(root / "pid")], env=env)
            deadline = time.monotonic() + 5
            while not (root / "pid").exists() and time.monotonic() < deadline:
                time.sleep(.01)
            pid = int((root / "pid").read_text())
            owner.terminate()
            self.assertEqual(owner.wait(timeout=5), 128 + signal.SIGTERM)
            with self.assertRaises(ProcessLookupError):
                os.kill(pid, 0)
            self.assertEqual(subprocess.run([sys.executable, str(WRAPPER), "--", "true"], env=env, timeout=5).returncode, 0)

    def test_successful_child_and_graceful_signal_cleanup_keep_the_exit_status(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, PYRY_TEST_ADMISSION_LOCK=str(root / "hold"))
            self.assertEqual(subprocess.run([sys.executable, str(WRAPPER), "--", "true"], env=env, timeout=5).returncode, 0)
            code = "import signal,time,sys; from pathlib import Path; p=Path(sys.argv[1]); signal.signal(signal.SIGTERM, lambda *args: (time.sleep(3.2), p.with_suffix('.done').touch(), sys.exit(7))); p.touch(); time.sleep(60)"
            owner = subprocess.Popen([sys.executable, str(WRAPPER), "--", sys.executable, "-c", code, str(root / "ready")], env=env)
            deadline = time.monotonic() + 5
            while not (root / "ready").exists() and time.monotonic() < deadline:
                time.sleep(.01)
            owner.terminate()
            self.assertEqual(owner.wait(timeout=5), 7)
            self.assertTrue((root / "ready.done").exists())

    def test_killed_waiter_never_starts_and_killed_owner_child_keeps_the_lock(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, PYRY_TEST_ADMISSION_LOCK=str(root / "hold"))
            code = "import time,sys; from pathlib import Path; p=Path(sys.argv[1]); p.touch(); time.sleep(.8); p.with_suffix('.done').touch()"
            owner = subprocess.Popen([sys.executable, str(WRAPPER), "--", sys.executable, "-c", code, str(root / "child")], env=env)
            deadline = time.monotonic() + 5
            while not (root / "child").exists() and time.monotonic() < deadline:
                time.sleep(.01)
            waiter = subprocess.Popen([sys.executable, str(WRAPPER), "--", "touch", str(root / "forbidden")], env=env, stderr=subprocess.DEVNULL)
            time.sleep(.1)
            waiter.terminate()
            waiter.wait(timeout=5)
            owner.kill()
            owner.wait(timeout=5)
            check = subprocess.run([sys.executable, str(WRAPPER), "--", sys.executable, "-c", "from pathlib import Path; import sys; assert Path(sys.argv[1]).exists()", str(root / "child.done")], env=env, timeout=5)
            self.assertEqual(check.returncode, 0)
            self.assertFalse((root / "forbidden").exists())

    def test_inherited_descriptor_for_another_file_cannot_bypass_the_hold(self):
        import fcntl
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with (root / "hold").open("w") as locked, (root / "other").open("w") as foreign:
                fcntl.flock(locked, fcntl.LOCK_EX)
                env = dict(os.environ, PYRY_TEST_ADMISSION_LOCK=str(root / "hold"), PYRY_TEST_ADMISSION_FD=str(foreign.fileno()))
                waiter = subprocess.Popen([sys.executable, str(WRAPPER), "--", "touch", str(root / "forbidden")], env=env, pass_fds=(foreign.fileno(),), stderr=subprocess.DEVNULL)
                time.sleep(.1)
                self.assertIsNone(waiter.poll())
                self.assertFalse((root / "forbidden").exists())
                waiter.terminate()
                waiter.wait(timeout=5)


if __name__ == "__main__":
    unittest.main()
