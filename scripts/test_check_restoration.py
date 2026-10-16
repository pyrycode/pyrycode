import importlib.util
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("restoration", Path(__file__).with_name("check-restoration.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class SelectionTests(unittest.TestCase):
    def test_aggregate_check_selects_restoration_once_but_focused_test_stays_focused(self):
        root = Path(__file__).resolve().parents[1]
        aggregate = subprocess.check_output(["make", "-n", "check"], cwd=root).decode()
        self.assertEqual(aggregate.count("python3 scripts/check-restoration.py"), 1)
        self.assertGreater(aggregate.index('echo "All checks passed"'), aggregate.index("python3 scripts/check-restoration.py"))
        release = subprocess.check_output(["make", "-n", "preship"], cwd=root).decode()
        self.assertEqual(release.count("PYRY_EXHAUSTIVE_TESTS=1"), 1)
        self.assertNotIn("scripts/check-restoration.py", release)
        self.assertIn("PYRY_SLOW_TESTS=1", release)
        self.assertIn("-tags e2e_realclaude", release)
        self.assertIn("-tags e2e_liverelay", release)
        for plan in [aggregate, release]:
            self.assertEqual(plan.count("go vet ./..."), 1)
            self.assertEqual(plan.count("-tags e2e -race -count=1"), 1)
        focused = subprocess.check_output(["make", "-n", "test"], cwd=root).decode()
        self.assertNotIn("scripts/check-restoration.py", focused)

    def test_affected_aggregate_helper_invokes_the_full_proof_once(self):
        with patch.object(module, "required", return_value=True), patch.object(module.subprocess, "call", return_value=0) as execute:
            self.assertEqual(module.main(), 0)
        execute.assert_called_once()
        command = execute.call_args.args[0]
        self.assertIn("^TestCheckpointResumeCorpus$", command)
        self.assertEqual(execute.call_args.kwargs["env"]["PYRY_EXHAUSTIVE_TESTS"], "1")

    def test_transitive_dependencies_fixtures_and_build_inputs_require_full_proof(self):
        dependencies = {"internal/thread", "internal/history", "internal/protocol"}
        for path in ["internal/thread/checkpoint_import.go", "internal/thread/testdata/golden.json", "internal/protocol/thread.go", "internal/history/append.go", "go.mod", "go.sum", "go.work", "Makefile", "scripts/check-restoration.py", ".github/workflows/ci.yml"]:
            with self.subTest(path=path):
                self.assertTrue(module.affected([path], dependencies))
        self.assertFalse(module.affected(["docs/thread.md", "cmd/pyry/main.go"], dependencies))

    def test_missing_base_or_dependency_analysis_requires_full_proof(self):
        with patch.object(module, "run", side_effect=OSError("unavailable")):
            self.assertTrue(module.required())
        with patch.object(module, "run", side_effect=[b"base", b"", b"", b"{}"]):
            self.assertTrue(module.required())


if __name__ == "__main__":
    unittest.main()
