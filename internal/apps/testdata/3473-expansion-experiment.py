"""Reproduce the retained-expansion investigation without editing production files.

Run from any directory: python3 internal/apps/testdata/3473-expansion-experiment.py
OUT_DIRECTORY. Requires Go and the baseline revision in the local Git history.
Expected failures: baseline-delayed and no-reuse. Inspect their logs and counts.
"""
import json
import os
from pathlib import Path
import platform
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[3]
OUT = Path(sys.argv[1]).resolve()
OUT.mkdir(parents=True, exist_ok=False)
TEST = ROOT / 'internal/apps/artifact_resolution_test.go'
PROD = ROOT / 'internal/apps/artifacts.go'
BASE = '4bc9790a35943200b86946a2968e10914e74b8c7'
baseline = subprocess.check_output(['git', 'show', BASE + ':internal/apps/artifact_resolution_test.go'], cwd=ROOT, text=True)
for variable, call, label in [('validated', 'ValidateArtifacts', 'validation'), ('installed', 'InstallBuild', 'installation'), ('verified', 'VerifyBuild', 'verification')]:
    lines = baseline.splitlines()
    index = next(i for i, line in enumerate(lines) if line.startswith('\t' + variable + ', err := ' + call))
    lines.insert(index, '\tstarted' + (':=' if variable == 'validated' else '=') + ' time.Now()')
    lines.insert(index + 2, '\tt.Logf("' + label + ': %s", time.Since(started))')
    baseline = '\n'.join(lines) + '\n'
delay = '''
type testExpansionDelayContext struct {
    context.Context
    delayed bool
}
func (c *testExpansionDelayContext) Err() error {
    if !c.delayed {
        c.delayed = true
        time.Sleep(1100 * time.Millisecond)
    }
    return c.Context.Err()
}
'''
for name, source in [('baseline', baseline), ('candidate', TEST.read_text())]:
    for delayed in [False, True]:
        variant = name + ('-delayed' if delayed else '')
        if delayed:
            source = source.replace('InstallBuild(ctx, root, stage,', 'InstallBuild(&testExpansionDelayContext{Context: ctx}, root, stage,') + delay
        file = OUT / (variant + '.go')
        file.write_text(source)
        (OUT / (variant + '-overlay.json')).write_text(json.dumps({'Replace': {str(TEST): str(file)}}))
prod = PROD.read_text()
needle = 'resolved, cached := s.resolved[p]'
assert prod.count(needle) == 1
(OUT / 'no-reuse.go').write_text(prod.replace(needle, 'var resolved []string\n\t\t\tcached := false'))
(OUT / 'no-reuse-overlay.json').write_text(json.dumps({'Replace': {str(PROD): str(OUT / 'no-reuse.go')}}))

env = dict(os.environ, GOMAXPROCS='2', GOFLAGS='-p=2')
selection = '^TestRetained(RepeatedExpansion|ResolutionCancellation|LongLinks)$'
results = {}
for variant in ['baseline', 'candidate', 'baseline-delayed', 'candidate-delayed']:
    command = ['go', 'test', '-race', '-c', '-overlay=' + str(OUT / (variant + '-overlay.json')), '-o', str(OUT / (variant + '.test')), './internal/apps']
    with (OUT / ('compile-' + variant + '.log')).open('w') as log:
        subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
    results['compile-' + variant] = {'command': command, 'exit': 0}

# Each pair competes concurrently. Linux children share two available CPUs;
# Darwin has no sched_setaffinity, so record that distinction explicitly.
cpus = sorted(os.sched_getaffinity(0))[:2] if hasattr(os, 'sched_getaffinity') else None
def pin():
    os.sched_setaffinity(0, cpus)
for delayed in [False, True]:
    processes = []
    for name in ['baseline', 'candidate']:
        variant = name + ('-delayed' if delayed else '')
        command = [str(OUT / (variant + '.test')), '-test.v', '-test.count=' + ('1' if delayed else '20'), '-test.run=' + selection, '-test.timeout=10m']
        log = (OUT / (variant + '.log')).open('w')
        process = subprocess.Popen(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, preexec_fn=pin if cpus else None)
        processes.append((variant, command, process, log))
    for variant, command, process, log in processes:
        code = process.wait()
        log.close()
        results[variant] = {'command': command, 'exit': code}
command = ['go', 'test', '-race', '-count=1', '-v', '-overlay=' + str(OUT / 'no-reuse-overlay.json'), '-run=^TestRetainedRepeatedExpansion$', './internal/apps']
with (OUT / 'no-reuse.log').open('w') as log:
    code = subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT).returncode
results['no-reuse'] = {'command': command, 'exit': code}
for variant in ['baseline', 'candidate', 'baseline-delayed', 'candidate-delayed', 'no-reuse']:
    lines = (OUT / (variant + '.log')).read_text().splitlines()
    results[variant]['counts'] = {name: sum(line.startswith(prefix) for line in lines) for name, prefix in [('executed', '=== RUN'), ('passed', '--- PASS:'), ('failed', '--- FAIL:'), ('skipped', '--- SKIP:')]}
    results[variant]['skipReasons'] = []  # These three tests have no skip branches.
record = {
    'baselineRevision': BASE,
    'candidateRevision': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
    'platform': platform.platform(), 'architecture': platform.machine(),
    'go': subprocess.check_output(['go', 'version'], text=True).strip(),
    'environment': {'GOMAXPROCS': '2', 'GOFLAGS': '-p=2'},
    'childAffinity': cpus, 'results': results,
}
(OUT / 'results.json').write_text(json.dumps(record, indent=2) + '\n')
print(json.dumps(record, indent=2))
expected = {'baseline': 0, 'candidate': 0, 'baseline-delayed': 1, 'candidate-delayed': 0, 'no-reuse': 1}
if any(results[name]['exit'] != code for name, code in expected.items()):
    sys.exit('Unexpected experiment outcome; inspect all logs before acceptance')
