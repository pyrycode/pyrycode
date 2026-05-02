// Package rotation watches claude's session directory for /clear-style UUID
// rotations and notifies its host (internal/sessions) so the registry can
// follow claude to the new <uuid>.jsonl in real time.
//
// The package has no dependency on internal/sessions; the contract is
// expressed via a Config struct of closures over primitive types so the
// import direction stays one-way (sessions -> rotation).
package rotation

import "strings"

// Probe answers "what JSONL does PID currently have open?".
//
// Returns absolute path on success, "" if PID has no JSONL fd open. Returns
// error only for unrecoverable probe failures; transient conditions like
// "process exited" or "permission denied on one fd" are squashed to ("", nil)
// so the watcher skips and retries on the next event.
type Probe interface {
	OpenJSONL(pid int) (string, error)
}

// OpenFile is one row of lsof's machine-readable output (Darwin).
type OpenFile struct {
	FD   string // e.g. "12u" — see `lsof -F` docs
	Name string // path
}

// parseProcFD interprets a single readlink target from /proc/<pid>/fd/<n>.
// Returns the path if it looks like a regular file path (rooted at "/"),
// otherwise "" (sockets like "socket:[123]", pipes like "pipe:[456]",
// anon_inode entries, etc.).
func parseProcFD(linkTarget string) string {
	if !strings.HasPrefix(linkTarget, "/") {
		return ""
	}
	return linkTarget
}

// parseLsofOutput parses `lsof -nP -p <pid> -F fn` output (file/name fields
// only). Records start with 'p<pid>' and contain alternating 'f<fd>' and
// 'n<name>' lines. Returns one OpenFile per (f, n) pair. Sockets and pipes
// are filtered out (their 'n' value does not start with '/'). Order matches
// lsof's output order. An orphaned 'f' record without a following 'n' is
// dropped.
func parseLsofOutput(raw string) []OpenFile {
	var out []OpenFile
	var pendingFD string
	for _, line := range strings.Split(raw, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'f':
			pendingFD = line[1:]
		case 'n':
			name := line[1:]
			if pendingFD == "" {
				continue
			}
			fd := pendingFD
			pendingFD = ""
			if !strings.HasPrefix(name, "/") {
				continue
			}
			out = append(out, OpenFile{FD: fd, Name: name})
		}
	}
	return out
}
