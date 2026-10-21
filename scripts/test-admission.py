#!/usr/bin/env python3
"""Serialize expensive project checks on this host, including nested gates."""
import fcntl
import os
from pathlib import Path
import signal
import runpy
import subprocess
import sys
import time


def group_alive(pid):
    states = subprocess.check_output(["ps", "-axo", "pgid=,stat="], text=True)
    return any(parts[0] == str(pid) and not parts[1].startswith("Z") for line in states.splitlines() if len(parts := line.split()) == 2)


def signal_group(pid, signum):
    try:
        os.killpg(pid, signum)
    except ProcessLookupError:
        pass
    except PermissionError:
        # Darwin reports EPERM for a pinned group containing only zombies.
        # A live group or an unreadable process inventory must stay an error.
        if sys.platform != "darwin":
            raise
        if group_alive(pid):
            raise


def drain_group(pid):
    # Keep the pinned leader and hold until SIGKILL recipients have exited.
    # An unreadable inventory cannot certify cleanup and must not admit work.
    warned = False
    while True:
        try:
            signal_group(pid, signal.SIGKILL)
            if not group_alive(pid):
                return
        except (OSError, subprocess.CalledProcessError):
            if not warned:
                print("Test admission: cannot certify child cleanup; keeping the host hold.", file=sys.stderr, flush=True)
                warned = True
        time.sleep(.1)


def main():
    args = sys.argv[1:]
    if args[:1] == ["--"]:
        args = args[1:]
    if not args:
        raise SystemExit("usage: test-admission.py -- command [arguments...]")
    if Path(args[0]).name == 'go':
        root = Path(__file__).resolve().parents[1]
        runpy.run_path(str(root / 'scripts/test-scope.py'))['check_selection'](root, 'go', args[1:])
    path = Path(os.environ.get("PYRY_TEST_ADMISSION_LOCK", str(Path.home() / ".local/share/pyry-fleet/test-admission.lock")))
    path.parent.mkdir(parents=True, exist_ok=True)
    fd = None
    inherited_lock = False
    try:
        inherited = int(os.environ.get("PYRY_TEST_ADMISSION_FD", "-1"))
        current = os.fstat(inherited)
        expected = path.stat()
        if (current.st_dev, current.st_ino) == (expected.st_dev, expected.st_ino):
            fcntl.flock(inherited, fcntl.LOCK_EX | fcntl.LOCK_NB)
            fd = inherited
            inherited_lock = True
    except (ValueError, OSError):
        pass
    if fd is None:
        fd = os.open(path, os.O_CREAT | os.O_RDWR, 0o600)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            # The dispatcher owns the clock and gives each spawn a private
            # telemetry directory. Report directly even when output is hidden
            # in a file or the shell has moved this command to the background.
            wait = None
            if os.environ.get('PYRY_TEST_WAIT_DIRECTORY'):
                root = Path(__file__).resolve().parents[1]
                agents = Path(os.environ.get('AGENTS_REPO_PATH', root.parent / 'pyrycode-agents'))
                helper = agents / 'dispatcher/scripts/test-wait.py'
                if helper.is_file():
                    wait = runpy.run_path(str(helper))['AdmissionWait']()
            started = time.monotonic()
            next_note = started
            while True:
                now = time.monotonic()
                if wait is not None:
                    wait.heartbeat()
                if now >= next_note:
                    print(f"Test admission: waiting ({int(now - started)} s so far).", file=sys.stderr, flush=True)
                    next_note = now + 30
                time.sleep(.1)
                try:
                    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    break
                except BlockingIOError:
                    pass
            if wait is not None:
                wait.finish()
            print(f"Test admission: acquired after {int(time.monotonic() - started)}s waiting", file=sys.stderr, flush=True)
    os.set_inheritable(fd, True)
    os.environ["PYRY_TEST_ADMISSION_FD"] = str(fd)
    if inherited_lock:
        os.execvpe(args[0], args, os.environ)
    child = None
    interrupted = None
    finishing = False

    def forward(signum, _frame):
        nonlocal interrupted
        interrupted = time.monotonic()
        if child is not None and not finishing:
            signal_group(child.pid, signum)

    for signum in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(signum, forward)
    try:
        child = subprocess.Popen(args, pass_fds=(fd,), start_new_session=True)
        # Observe termination without reaping the leader. Its PID pins the group
        # identity until every remaining member has received cleanup signals.
        while os.waitid(os.P_PID, child.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT) is None:
            if interrupted is not None and time.monotonic() - interrupted > 60:
                signal_group(child.pid, signal.SIGKILL)
            time.sleep(0.05)
    finally:
        finishing = True
        if child is not None:
            drain_group(child.pid)
            child.wait()
        os.close(fd)
    return child.returncode if child.returncode >= 0 else 128 - child.returncode


if __name__ == "__main__":
    raise SystemExit(main())
