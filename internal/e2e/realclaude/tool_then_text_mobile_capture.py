#!/usr/bin/env python3
"""Run the admitted unchanged mobile method and promote its private daemon input.

Run from a clean committed ticket checkout on the admitted Android host. Both
diagnostic failures and passes retain XML and raw records outside the worktree.
Authentication belongs to the two admitted launchers; this driver reads no login.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time

METHOD = "de.pyryco.mobile.e2e.InteractiveStreamE2ETest#interactiveTurn_toolThenText_statusBandNeverEmptyWhileBusy"


def revision(root):
    if subprocess.check_output(["git", "status", "--porcelain"], cwd=root):
        raise RuntimeError("capture requires clean committed daemon and mobile sources")
    return subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mobile", type=Path, required=True)
    parser.add_argument("--mobile-agents", type=Path, required=True)
    parser.add_argument("--daemon-agents", type=Path, required=True)
    args = parser.parse_args()
    daemon = Path(__file__).resolve().parents[3]
    mobile = args.mobile.resolve()
    if os.environ.get("PYRY_BIN") or os.environ.get("PYRY_E2E_CLAUDE_BIN_FILE"):
        raise RuntimeError("remove inherited test binary selection; launcher must build this ticket's daemon")
    daemon_revision, mobile_revision = revision(daemon), revision(mobile)
    launcher = mobile / "scripts/android-test-gate.py"
    live_gate = args.daemon_agents.resolve() / "dispatcher/scripts/live-claude-gate.py"
    if not launcher.is_file() or not live_gate.is_file() or not (args.mobile_agents / "scripts/android-test-gate.py").is_file():
        raise RuntimeError("both admitted launchers and mobile agents are required")
    recovery = Path(tempfile.mkdtemp(prefix="pyry-2939-mobile-"))
    env = os.environ.copy()
    if "-tags" in env.get("GOFLAGS", ""):
        raise RuntimeError("GOFLAGS already selects tags; use a clean invocation for the capture build")
    env.update(PYRYCODE_SRC=str(daemon), GOFLAGS=env.get("GOFLAGS", "")+" -tags=e2e_realclaude",
               PYRY_TOOL_THEN_TEXT_CAPTURE_DIR=str(recovery), AGENTS_REPO_PATH=str(args.mobile_agents.resolve()))
    started = time.time_ns()
    print(f"#2939 private recovery: {recovery}", flush=True)
    # Raw launcher logs may contain diagnostic payloads. Keep them private and
    # print only the recovery path/counts from the deny-scanning Go reader.
    stdout_path, stderr_path = recovery / "launcher.stdout", recovery / "launcher.stderr"
    for path in (stdout_path, stderr_path):
        path.touch(mode=0o600)
    with stdout_path.open("wb") as out, stderr_path.open("wb") as err:
        run = subprocess.run([sys.executable, str(launcher), "live", "--tests", METHOD],
                             cwd=mobile, env=env, stdout=out, stderr=err)
    reports = [p for p in (mobile / "build/dispatcher-tests").glob("live-*/dispatcher.xml")
               if p.stat().st_mtime_ns >= started]
    xml_file = ""
    if len(reports) == 1:
        xml_file = "mobile.xml"
        shutil.copyfile(reports[0], recovery / xml_file)
        (recovery / xml_file).chmod(0o600)
    # Refuse source movement during the live drive. Actual binary provenance is
    # independently checked against its embedded build revision by the Go reader.
    if revision(daemon) != daemon_revision or revision(mobile) != mobile_revision:
        raise RuntimeError("sources changed during mobile execution; retain recovery but do not promote")
    manifest = recovery / "driver.json"
    manifest.touch(mode=0o600)
    manifest.write_text(json.dumps(dict(daemon_revision=daemon_revision, mobile_revision=mobile_revision,
                                       xml_file=xml_file, exit_code=run.returncode))+"\n")
    admission_env = os.environ.copy()
    admission_env.update(AGENTS_REPO_PATH=str(args.daemon_agents.resolve()),
                         PYRY_TOOL_THEN_TEXT_MOBILE_DIR=str(recovery))
    # Ordinary live admission supplies the credential deny needles; no login is
    # fetched or copied by this driver. XML failure remains diagnostic evidence.
    result = subprocess.run([sys.executable, str(live_gate), "go", "--tests", "^TestRealClaude_StreamEventCapture$"],
                            cwd=daemon, env=admission_env)
    print(f"#2939 mobile launcher exit={run.returncode}; admission exit={result.returncode}; recovery={recovery}")
    return result.returncode


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, RuntimeError, subprocess.CalledProcessError) as error:
        sys.exit(f"#2939 capture instrument: {error}")
