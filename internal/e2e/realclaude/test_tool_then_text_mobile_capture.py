import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("capture", Path(__file__).with_name("tool_then_text_mobile_capture.py"))
capture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capture)


class PortableCaptureTest(unittest.TestCase):
    def test_admitted_launch_and_diagnostic_recovery(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            mobile, mobile_agents, daemon_agents = (root / name for name in ("mobile", "mobile-agents", "daemon-agents"))
            for path in (mobile / "scripts/android-test-gate.py", mobile_agents / "scripts/android-test-gate.py",
                         daemon_agents / "dispatcher/scripts/live-claude-gate.py"):
                path.parent.mkdir(parents=True, exist_ok=True)
                path.touch()
            recovery = root / "recovery"
            recovery.mkdir(mode=0o700)
            calls = []

            def run(command, **kwargs):
                calls.append(command)
                env = kwargs["env"]
                if len(calls) == 1:
                    self.assertEqual(command[-3:], ["live", "--tests", capture.METHOD])
                    self.assertEqual(env["PYRYCODE_SRC"], str(Path(capture.__file__).resolve().parents[3]))
                    self.assertIn("-tags=e2e_realclaude", env["GOFLAGS"])
                    self.assertEqual(env["PYRY_TOOL_THEN_TEXT_CAPTURE_DIR"], str(recovery))
                    report = mobile / "build/dispatcher-tests/live-test/dispatcher.xml"
                    report.parent.mkdir(parents=True)
                    report.write_text("<testsuite><testcase><failure/></testcase></testsuite>")
                else:
                    self.assertEqual(command[-3:], ["go", "--tests", "^TestRealClaude_StreamEventCapture$"])
                    self.assertEqual(env["PYRY_TOOL_THEN_TEXT_MOBILE_DIR"], str(recovery))
                    manifest = json.loads((recovery / "driver.json").read_text())
                    self.assertEqual(manifest["exit_code"], 1)
                    self.assertEqual(manifest["daemon_revision"], "a" * 40)
                    self.assertEqual(manifest["mobile_revision"], "b" * 40)
                    self.assertIn("<failure", (recovery / manifest["xml_file"]).read_text())
                    self.assertEqual((recovery / "driver.json").stat().st_mode & 0o777, 0o600)
                return subprocess.CompletedProcess(command, 1)

            argv = [capture.__file__, "--mobile", str(mobile), "--mobile-agents", str(mobile_agents), "--daemon-agents", str(daemon_agents)]
            with patch.dict(os.environ, {}, clear=True), patch("sys.argv", argv), \
                 patch.object(capture, "revision", side_effect=lambda path: ("b" if path == mobile else "a") * 40), \
                 patch.object(capture.tempfile, "mkdtemp", return_value=str(recovery)), \
                 patch.object(capture.subprocess, "run", side_effect=run):
                self.assertEqual(capture.main(), 1)
            self.assertEqual(len(calls), 2)

    def test_wrong_binary_selection_never_launches(self):
        with patch.dict(os.environ, {"PYRY_BIN": "/wrong/daemon"}, clear=True), \
             patch("sys.argv", [capture.__file__, "--mobile", "/mobile", "--mobile-agents", "/agents", "--daemon-agents", "/daemon-agents"]), \
             patch.object(capture.subprocess, "run") as run:
            with self.assertRaisesRegex(RuntimeError, "binary selection"):
                capture.main()
            run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
