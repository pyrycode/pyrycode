package streamsup

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// The bound on what an exit record carries of the child's stderr (#2723): at
// most stderrTailLines lines and stderrTailBytes bytes, keeping the END, which is
// where claude prints the reason it is exiting.
const (
	stderrTailBytes = 1024
	stderrTailLines = 5
)

// stderrDrainGrace bounds how long finish waits for the stderr reader to reach
// EOF after the child has exited. It only matters when a descendant of claude
// inherited the write end and is still alive; with no such holder EOF is
// immediate.
const stderrDrainGrace = 250 * time.Millisecond

// stderrTail is an io.Writer that remembers only the last stderrTailBytes bytes
// written to it. Memory stays bounded however much the child writes.
type stderrTail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *stderrTail) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > stderrTailBytes {
		p = p[len(p)-stderrTailBytes:]
	}
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - stderrTailBytes; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	t.mu.Unlock()
	return n, nil
}

// String returns the tail with trailing newlines trimmed, cut to its last
// stderrTailLines lines. The result is always a suffix of what was written.
func (t *stderrTail) String() string {
	t.mu.Lock()
	s := strings.TrimRight(string(t.buf), "\r\n")
	t.mu.Unlock()
	lines := strings.Split(s, "\n")
	if len(lines) > stderrTailLines {
		lines = lines[len(lines)-stderrTailLines:]
	}
	return strings.Join(lines, "\n")
}

// daemonLogOnly marks an slog attribute value that may reach the daemon's own
// log output but never the log ring: control.SlogTee replaces any value with a
// LogDaemonOnly method in its ring copy, so it stays out of `pyry logs` and the
// debug bundle a paired phone can fetch. The contract is the method set rather
// than a shared type because internal/control imports internal/sessions, an edge
// this package must not grow.
//
// It is only ever an attribute VALUE. MarshalText routes it through slog's text
// quoting, so untrusted bytes cannot forge a record or a field.
type daemonLogOnly string

// LogDaemonOnly is the marker control.SlogTee recognises.
func (daemonLogOnly) LogDaemonOnly() {}

func (d daemonLogOnly) MarshalText() ([]byte, error) { return []byte(d), nil }

// stderrCapture feeds one spawn's stderr into a stderrTail WITHOUT changing when
// cmd.Wait returns. See captureStderr for the two wirings.
type stderrCapture struct {
	tail stderrTail
	// pr and pw are the private pipe on the forward == nil path; both nil on the
	// forward path and when the pipe could not be created.
	pr, pw *os.File
	done   chan struct{}
}

// captureStderr sets cmd.Stderr. With forward non-nil the child already had an
// exec-owned pipe, so the tail rides beside forward on it and nothing about Wait
// changes. With forward nil — the interactive daemon's path, where stderr used
// to go to /dev/null — the child gets the write end of a private os.Pipe: an
// *os.File is handed to the child directly with no exec copier, so Wait still
// does not wait on stderr, exactly as with the old nil. An exec-owned pipe there
// would make Wait block on any descendant of claude that inherited stderr, up to
// WaitDelay, and turn such a clean exit into exec.ErrWaitDelay.
//
// A pipe that cannot be created degrades to the old nil — the tail is a
// diagnostic and must never fail a spawn.
func captureStderr(cmd *exec.Cmd, forward io.Writer, log *slog.Logger) *stderrCapture {
	c := &stderrCapture{}
	if forward != nil {
		cmd.Stderr = io.MultiWriter(forward, &c.tail)
		return c
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		log.Debug("streamsup: stderr tail unavailable", "err", err)
		return c
	}
	c.pr, c.pw = pr, pw
	cmd.Stderr = pw
	return c
}

// started is called once after cmd.Start, with whether it succeeded. The
// parent's write end is closed either way, so EOF arrives when the child and any
// descendant holding stderr have exited.
func (c *stderrCapture) started(ok bool) {
	if c.pw == nil {
		return
	}
	_ = c.pw.Close()
	if !ok {
		_ = c.pr.Close()
		c.pr = nil
		return
	}
	c.done = make(chan struct{})
	go func() {
		defer close(c.done)
		_, _ = io.Copy(&c.tail, c.pr)
	}()
}

// finish is called once after cmd.Wait and returns the tail. It waits for the
// reader to reach EOF for at most stderrDrainGrace, then closes the read end —
// which unblocks a Read on the poller-backed pipe — and joins the reader, so the
// goroutine never outlives the spawn.
func (c *stderrCapture) finish() string {
	if c.done != nil {
		timer := time.NewTimer(stderrDrainGrace)
		select {
		case <-c.done:
		case <-timer.C:
		}
		timer.Stop()
		_ = c.pr.Close()
		<-c.done
	}
	return c.tail.String()
}
