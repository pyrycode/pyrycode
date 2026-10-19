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

            argv = [capture.__file__, "--focused", "--mobile", str(mobile), "--mobile-agents", str(mobile_agents), "--daemon-agents", str(daemon_agents)]
            with patch.dict(os.environ, {}, clear=True), patch("sys.argv", argv), \
                 patch.object(capture, "revision", side_effect=lambda path: ("b" if path == mobile else "a") * 40), \
                 patch.object(capture.tempfile, "mkdtemp", return_value=str(recovery)), \
                 patch.object(capture.subprocess, "run", side_effect=run):
                self.assertEqual(capture.main(), 1)
            self.assertEqual(len(calls), 2)

    def test_historical_selection_and_observed_order(self):
        methods = ["interactiveTurn_case"+str(i) for i in range(63)] + [capture.METHOD.split("#")[1]]
        source = 'elif [ -n "${LIVE}" ]; then\n  # LIVE curates\n'
        source += '  TEST_TARGET="' + ','.join('${TEST_CLASS}#'+m for m in methods) + '"\nelse\n'
        with patch.object(capture.subprocess, "check_output", return_value=source):
            selected, provenance = capture.historical_selection(Path("/mobile"))
        self.assertEqual(len(selected), 64)
        self.assertEqual(selected[-1], capture.METHOD)
        self.assertEqual(provenance["historical_mobile_revision"], capture.HISTORICAL_MOBILE)
        lines = ["10-12 20:00:01.123  1 2 I TestRunner: started: interactiveTurn_case1(de.pyryco.mobile.e2e.InteractiveStreamE2ETest)",
                 "10-12 20:00:02.456  1 2 I TestRunner: finished: interactiveTurn_case1(de.pyryco.mobile.e2e.InteractiveStreamE2ETest)",
                 "10-12 20:00:03.456  1 2 I TestRunner: started: "+capture.METHOD.split("#")[1]+"(de.pyryco.mobile.e2e.InteractiveStreamE2ETest)"]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ("a-logcat-one.txt", "b-logcat-two.txt"):
                (root / name).write_text("\n".join(lines))
            observed = capture.execution_records(root, root / "private")
        self.assertEqual([r["event"] for r in observed], ["started", "finished", "started"])
        self.assertEqual(observed[-1]["method"], capture.METHOD)

    def test_shared_launch_is_one_bounded_harness(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            mobile, agents = root / "mobile", root / "agents"
            for path in (mobile / "scripts/android-test-gate.py", agents / "scripts/android-test-gate.py",
                         agents / "dispatcher/scripts/live-claude-gate.py"):
                path.parent.mkdir(parents=True, exist_ok=True); path.touch()
            recovery = root / "recovery"; recovery.mkdir(mode=0o700)
            selected = [capture.METHOD]+["de.pyryco.mobile.e2e.InteractiveStreamE2ETest#interactiveTurn_case"+str(i) for i in range(63)]
            calls = []
            def run(command, **kwargs):
                calls.append(command)
                if len(calls) == 1:
                    self.assertEqual(command[-1], ",".join(selected))
                    report = mobile / "build/dispatcher-tests/live-test/dispatcher.xml"
                    report.parent.mkdir(parents=True); report.write_text("<testsuite/>")
                else:
                    self.assertEqual(command[-1], "^TestRealClaude_ToolThenTextSharedHarnessCapture$")
                    driver = json.loads((recovery / "driver.json").read_text())
                    self.assertEqual(driver["selected_methods"], selected)
                    self.assertEqual(driver["mode"], "shared-harness")
                return subprocess.CompletedProcess(command, 1)
            argv = [capture.__file__, "--mobile", str(mobile), "--mobile-agents", str(agents), "--daemon-agents", str(agents)]
            with patch.dict(os.environ, {}, clear=True), patch("sys.argv", argv), \
                 patch.object(capture, "revision", return_value="a"*40), \
                 patch.object(capture, "historical_selection", return_value=(selected, {})), \
                 patch.object(capture, "source_differences", return_value={}), \
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
