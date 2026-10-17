"""Apply the pinned Core launcher policy before test admission."""
import os
from pathlib import Path
import runpy
import sys


def check_selection(root, kind, args):
    root = Path(root).resolve()
    agents = Path(os.environ.get('AGENTS_REPO_PATH', root.parent / 'pyrycode-agents'))
    guard = agents / 'bin/check-test-selection'
    if guard.is_file():
        runpy.run_path(str(guard))['check_selection'](root, kind, args)
        return
    # Active old consumers have no new binding. Preserve their graceful drain.
    git = root / '.git'
    if git.is_file():
        link = git.read_text().strip()
        if not link.startswith('gitdir: '):
            sys.exit('Managed test context has an invalid Git worktree link.')
        git = (root / link[8:]).resolve()
    if (git / 'pyry-test-context').exists() or (git / 'pyry-test-managed').exists():
        sys.exit('Managed test selection helper is unavailable; no test was admitted.')
