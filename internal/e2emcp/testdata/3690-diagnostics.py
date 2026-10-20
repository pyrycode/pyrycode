"""Prepare offline cancellation experiments; execute Go checks via test admission."""
import hashlib
import json
from pathlib import Path
import sys

root = Path(__file__).resolve().parents[3]
scratch = Path(sys.argv[1]).resolve()
scratch.mkdir(parents=True, exist_ok=True, mode=0o700)
server = root / 'internal/e2emcp/server.go'
test = root / 'internal/e2emcp/proxy_test.go'
body = server.read_text()


def replace_once(text, old, new):
    assert text.count(old) == 1, old
    return text.replace(old, new)


def overlay(name, source, text):
    target = scratch / (name + '.go')
    target.write_text(text)
    (scratch / (name + '.json')).write_text(
        json.dumps({'Replace': {str(source): str(target)}}))
    return {'source': str(source.relative_to(root)),
            'sourceSHA256': hashlib.sha256(source.read_bytes()).hexdigest(),
            'overlaySHA256': hashlib.sha256(text.encode()).hexdigest()}


manifest = {}
# bad must cancel before Abort attempts to acquire the forwarding mutex.
# Its omission strands the large write until the failing watchdog releases it.
disabled = replace_once(body, '\n\ts.cancel()\n\ts.listener.Close()',
                        '\n\t// Diagnostic: cancellation disabled.\n\ts.listener.Close()')
manifest['disabled'] = overlay('disabled', server, disabled)

# Content-free parent-process observations, never child IPC or capabilities.
phases = replace_once(body, 'import (', 'import (\n "fmt"')
for old, new in {
    'case <-ctx.Done():\n\t\t\tfor _, f := range files':
        'case <-ctx.Done():\n\t\t\tfmt.Fprintf(os.Stderr, "phase ctx-close %d\\n", time.Now().UnixNano())\n\t\t\tfor _, f := range files',
    'done <- result{kind: name, count: count, eof: eof, err: err}':
        'fmt.Fprintf(os.Stderr, "phase pump-%s-return %d\\n", name, time.Now().UnixNano())\n\t\tdone <- result{kind: name, count: count, eof: eof, err: err}',
    'err := cmd.Wait()':
        'err := cmd.Wait()\n\t\tfmt.Fprintf(os.Stderr, "phase server-Wait-return %d\\n", time.Now().UnixNano())',
    'done <- result{kind: "stderr", eof: err == nil, stderr: body.String(), err: err}':
        'fmt.Fprintf(os.Stderr, "phase stderr-return %d\\n", time.Now().UnixNano())\n\t\tdone <- result{kind: "stderr", eof: err == nil, stderr: body.String(), err: err}',
}.items():
    phases = replace_once(phases, old, new)
manifest['phases'] = overlay('phases', server, phases)

# Restore the premature observation and elapsed predicate while retaining
# direct parent/owned-context diagnostics. Controlled delay must falsify it.
old = test.read_text()
start = old.index('\t\t\t\t\t// A byte proves forwarding')
end = old.index('\n\t\t\t\t}', start)
old = old[:start] + '\t\t\t\t\tt.Logf("validation-only readiness age=%s", time.Since(observedAt))' + old[end:]
old = replace_once(old, 'joinElapsed := time.Since(started)',
                  'joinElapsed := time.Since(started)\n'
                  '\t\t\tif joinElapsed > time.Second {\n'
                  '\t\t\t\tt.Fatalf("elapsed-only oracle: Join=%s owned=%v parent=%v", joinElapsed, s.ctx.Err(), ctx.Err())\n'
                  '\t\t\t}')
manifest['old-oracle'] = overlay('old-oracle', test, old)
print(json.dumps(manifest, indent=2))
