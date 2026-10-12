#!/usr/bin/env python3
"""Record the historical shared mobile harness through ordinary live admission.

Run from a clean committed ticket checkout on the admitted Android host. Both
diagnostic failures and passes retain XML and raw records outside the worktree.
Authentication belongs to the two admitted launchers; this driver reads no login.
"""
import argparse
import hashlib
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time

METHOD = "de.pyryco.mobile.e2e.InteractiveStreamE2ETest#interactiveTurn_toolThenText_statusBandNeverEmptyWhileBusy"
HISTORICAL_MOBILE = "5de4f4459"
HISTORICAL_DAEMON = "6019328b378cad587f69b7bc94de37febbdf8556"


def historical_selection(mobile):
    source = subprocess.check_output(["git", "show", HISTORICAL_MOBILE+":scripts/e2e-emulator.sh"], cwd=mobile, text=True)
    branch = source[source.index('elif [ -n "${LIVE}" ]; then\n  # LIVE curates'):].split("\nelse\n", 1)[0]
    assignments = "\n".join(line for line in branch.splitlines() if line.lstrip().startswith('TEST_TARGET="'))
    selected = [METHOD.split("#")[0]+"#"+name for name in re.findall(r"#(interactiveTurn_\w+)", assignments)]
    if len(selected) != 64 or len(set(selected)) != 64 or METHOD not in selected:
        raise RuntimeError("historical selector must identify exactly 64 methods including the target")
    return selected, dict(historical_mobile_revision=HISTORICAL_MOBILE, historical_daemon_revision=HISTORICAL_DAEMON,
                         historical_selector_sha256=hashlib.sha256(source.encode()).hexdigest(),
                         historical_settings="Unknown operator model/effort/permission; no configuration equivalence claimed")


def source_differences(mobile, mobile_agents, daemon):
    paths = ["scripts/e2e-emulator.sh", "scripts/android-test-gate.py",
             "app/src/androidTest/java/de/pyryco/mobile/e2e/InteractiveStreamE2ETest.kt"]
    differences = {"mobile_changed_paths": subprocess.check_output(
        ["git", "diff", "--name-only", HISTORICAL_MOBILE, "HEAD", "--", *paths], cwd=mobile, text=True).splitlines()}
    for path in paths:
        differences[path+"_sha256"] = hashlib.sha256((mobile / path).read_bytes()).hexdigest()
    differences["mobile_agents_revision"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=mobile_agents, text=True).strip()
    differences["admitted_launcher_sha256"] = hashlib.sha256((mobile_agents / "scripts/android-test-gate.py").read_bytes()).hexdigest()
    differences["daemon_parser_changed_paths"] = subprocess.check_output(
        ["git", "diff", "--name-only", HISTORICAL_DAEMON, "HEAD", "--", "internal/streamsup", "cmd/pyry/streamsup_runner.go"], cwd=daemon, text=True).splitlines()
    return differences


def execution_records(report_dir, recovery):
    # TestRunner records are observed execution, unlike JUnit report ordering.
    # Raw logcats stay private; only identities and timestamps leave this reader.
    recovery.mkdir(mode=0o700, exist_ok=True)
    found = {}
    pattern = re.compile(r"^(\d\d-\d\d \d\d:\d\d:\d\d\.\d+) .*?TestRunner\s*:\s*(started|finished): (interactiveTurn_\w+)\((de\.pyryco\.mobile\.e2e\.InteractiveStreamE2ETest)\)")
    paths = sorted(report_dir.glob("*logcat*.txt"))
    if len(paths) > 256:
        raise RuntimeError("too many execution logs")
    total = 0
    for index, path in enumerate(paths):
        total += path.stat().st_size
        if total > 32 << 20:
            raise RuntimeError("execution logs exceed diagnostic bound")
        # Native transport returns the bounded parsed census, not raw logcats.
        target = recovery / f"execution-{index}.log"
        shutil.copyfile(path, target); target.chmod(0o600)
        for line in path.read_text(errors="replace").splitlines():
            match = pattern.search(line)
            if match:
                stamp, event, name, cls = match.groups()
                found[(stamp, event, cls+"#"+name)] = dict(timestamp=stamp, event=event, method=cls+"#"+name)
    return [found[key] for key in sorted(found)]


def revision(root):
    if subprocess.check_output(["git", "status", "--porcelain"], cwd=root):
        raise RuntimeError("capture requires clean committed daemon and mobile sources")
    return subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mobile", type=Path, required=True)
    parser.add_argument("--mobile-agents", type=Path, required=True)
    parser.add_argument("--daemon-agents", type=Path, required=True)
    parser.add_argument("--focused", action="store_true", help="run the unchanged named proof instead of the 64-method diagnostic")
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
    selected, provenance = ([METHOD], {}) if args.focused else historical_selection(mobile)
    if not args.focused:
        provenance.update(source_differences(mobile, args.mobile_agents.resolve(), daemon))
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
        run = subprocess.run([sys.executable, str(launcher), "live", "--tests", ",".join(selected)],
                             cwd=mobile, env=env, stdout=out, stderr=err)
    reports = [p for p in (mobile / "build/dispatcher-tests").glob("live-*/dispatcher.xml")
               if p.stat().st_mtime_ns >= started]
    xml_file = ""
    observed = []
    if len(reports) == 1:
        xml_file = "mobile.xml"
        shutil.copyfile(reports[0], recovery / xml_file)
        (recovery / xml_file).chmod(0o600)
        if not args.focused:
            observed = execution_records(reports[0].parent, recovery)
    # Refuse source movement during the live drive. Actual binary provenance is
    # independently checked against its embedded build revision by the Go reader.
    if revision(daemon) != daemon_revision or revision(mobile) != mobile_revision:
        raise RuntimeError("sources changed during mobile execution; retain recovery but do not promote")
    manifest = recovery / "driver.json"
    manifest.touch(mode=0o600)
    manifest.write_text(json.dumps(dict(daemon_revision=daemon_revision, mobile_revision=mobile_revision,
                                       xml_file=xml_file, exit_code=run.returncode,
                                       mode="focused" if args.focused else "shared-harness",
                                       selected_methods=selected, execution_records=observed, provenance=provenance))+"\n")
    admission_env = os.environ.copy()
    admission_env.update(AGENTS_REPO_PATH=str(args.daemon_agents.resolve()),
                         PYRY_TOOL_THEN_TEXT_MOBILE_DIR=str(recovery))
    # Ordinary live admission supplies the credential deny needles; no login is
    # fetched or copied by this driver. XML failure remains diagnostic evidence.
    reader = "^TestRealClaude_StreamEventCapture$" if args.focused else "^TestRealClaude_ToolThenTextSharedHarnessCapture$"
    result = subprocess.run([sys.executable, str(live_gate), "go", "--tests", reader],
                            cwd=daemon, env=admission_env)
    print(f"#2939 mobile launcher exit={run.returncode}; admission exit={result.returncode}; recovery={recovery}")
    return result.returncode


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, RuntimeError, subprocess.CalledProcessError) as error:
        sys.exit(f"#2939 capture instrument: {error}")
