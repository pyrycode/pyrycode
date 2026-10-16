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
        focused = subprocess.check_output(["make", "-n", "test"], cwd=root).decode()
        self.assertNotIn("scripts/check-restoration.py", focused)

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
