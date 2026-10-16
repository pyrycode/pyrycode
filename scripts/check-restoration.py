#!/usr/bin/env python3
"""Require the exhaustive proof when its code, dependencies or build inputs change."""
import json
import os
from pathlib import Path
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]


def affected(paths, dependencies):
    global_inputs = {"go.mod", "go.sum", "go.work", "go.work.sum", "Makefile"}
    return any(path in global_inputs or path.startswith(("scripts/", ".github/")) or any(path.startswith(directory + "/") for directory in dependencies) for path in paths)


def run(*args):
    return subprocess.check_output(args, cwd=ROOT)


def required():
    try:
        base = run("git", "merge-base", os.environ.get("TARGET_DEFAULT_BRANCH", "main"), "HEAD").decode().strip()
        paths = run("git", "diff", "--name-only", "-z", base, "--").split(b"\0")
        paths += run("git", "ls-files", "--others", "--exclude-standard", "-z").split(b"\0")
        raw = run("go", "list", "-deps", "-test", "-json", "./internal/thread").decode()
        decoder = json.JSONDecoder()
        dependencies = set()
        while raw.strip():
            value, end = decoder.raw_decode(raw.lstrip())
            raw = raw.lstrip()[end:]
            directory = value.get("Dir")
            if directory and Path(directory).is_relative_to(ROOT):
                dependencies.add(str(Path(directory).relative_to(ROOT)))
        if "internal/thread" not in dependencies:
            raise ValueError("restoration package missing from dependency analysis")
        return affected([path.decode() for path in paths if path], dependencies)
    except (OSError, subprocess.CalledProcessError, ValueError, UnicodeError) as error:
        print(f"Restoration dependency analysis unavailable; requiring the complete proof: {error}", file=sys.stderr)
        return True


def main():
    if not required():
        print("Complete restoration proof not required: no restoration code, dependency or build input changed.")
        return 0
    env = dict(os.environ, PYRY_EXHAUSTIVE_TESTS="1")
    return subprocess.call([sys.executable, str(ROOT / "scripts/test-admission.py"), "--", "go", "test", "-race", "-count=1", "-json", "-run", "^TestCheckpointResumeCorpus$", "./internal/thread"], cwd=ROOT, env=env)


if __name__ == "__main__":
    raise SystemExit(main())
