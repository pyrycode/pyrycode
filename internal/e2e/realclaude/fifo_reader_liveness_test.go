//go:build e2e_realclaude

package realclaude

// A liveness read for the background-on-timeout probes (#1239), plus the
// offline self-check that proves it.
//
// # The question, and why ps cannot answer it
//
// The probes in background_trigger_probe_test.go stage a live claude turn
// around a command the test holds open through a FIFO (holdProbeFIFO).
// Holding the write end proves the command could not have FINISHED. It does not
// prove the command is ALIVE: a command that was killed leaves the same held
// write end and records identically.
//
// A pid lookup is the wrong instrument. `ps` lists zombies, so a SIGKILLed but
// unreaped process reads as alive, and the pid has to be pinned to the right
// process first. The FIFO answers directly, with no pid at all:
//
//	open(path, O_WRONLY|O_NONBLOCK) succeeds  <=> some process holds the read end
//	open(path, O_WRONLY|O_NONBLOCK) == ENXIO  <=> none does
//
// # The failure mode this file exists to close
//
// This family has twice shipped an instrument whose failure mode is
// indistinguishable from the result it claims (#1230's MUST FIX; re-derived on
// #1235, where `ps` exiting 1 on a bad column argument is indistinguishable from
// `ps` exiting 1 on a dead pid). The FIFO read has exactly such a mode, and it
// sits on the SUCCESS arm, where errno discrimination cannot reach it: if the
// path is a regular file — or a character device such as /dev/null — the open
// succeeds with NO READER ANYWHERE, and a bare open reports that as "alive".
//
// So the mode gate is a POSITIVE allowlist (Lstat, then Mode().Type() must equal
// os.ModeNamedPipe) and it runs BEFORE the open. A regular-file blacklist is not
// equivalent: /dev/null passes a blacklist and its bare open succeeds.
// TestFIFOReaderLiveness_NonPipePathsAreInstrumentFailures is the case that
// discriminates the two, and it measures the bare open rather than citing it.
//
// The same defect has a DUAL form, and it is the one that breaks the consumer:
// an instrument that can only ever answer "reader present". Two differently
// constructed FIFOs cannot prove the read flips — only one FIFO across one
// reader's lifetime can, which is what
// TestFIFOReaderLiveness_FlipsAcrossOneReaderLifetime does.
//
// # Lstat, not Stat — deliberate
//
// The caller creates the path itself, so a symlink sitting there means
// something else wrote it. Stat would follow the link to a target the gate never
// inspected; Lstat rejects it as instrument failure. That is the conservative
// arm, and it is intentional — do not "fix" it to Stat.
//
// # Kill() does not flip the read; Wait() is the synchronisation point
//
// Measured during design: five consecutive reads taken after Kill() and before
// Wait() all returned reader-present. The kernel closes the reader's fds before
// the process becomes reapable, but the flip is only guaranteed observable once
// the process has been waited for. A flip test that kills without reaping does
// not fail intermittently — it fails every time, and looks like the read is
// broken. Hence the Wait() in the flip test is load-bearing, not tidiness.
//
// # Everything here runs offline
//
// No claude, no credentials, no daemon, no env gate, no t.Skip. This is a
// deterministic RED/GREEN gate, not a probe:
//
//	go test -tags e2e_realclaude -run '^TestFIFOReaderLiveness' -v ./internal/e2e/realclaude/

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Liveness verdicts. Three-valued and never a bare boolean at any layer: a
// consumer (#1240) records which arm fired and why, verbatim, into published
// evidence, and "instrument failed" must never collapse into either answer.
const (
	// fifoLiveReaderPresent: the open succeeded, so some process currently
	// holds the FIFO's read end.
	fifoLiveReaderPresent = "reader-present"
	// fifoLiveNoReader: the open returned ENXIO. This is the ONLY input in the
	// whole instrument that produces this verdict.
	fifoLiveNoReader = "no-reader"
	// fifoLiveInstrumentFailed: the read could not be taken — a failing Lstat
	// (including a missing path), a non-FIFO mode, or any errno from the open
	// other than ENXIO. Never a statement about the command.
	fifoLiveInstrumentFailed = "instrument-failed"
)

// Fixture names and timings for the self-checks. Per-file consts are the
// package convention; nothing here is shared with background_trigger_probe_test.go.
const (
	fifoLiveFIFOName       = "liveness-hold"
	fifoLiveReaderCommand  = "cat"
	fifoLiveRegularName    = "liveness-regular"
	fifoLiveSymlinkName    = "liveness-symlink"
	fifoLiveMissingName    = "liveness-missing"
	fifoLiveSymlinkTarget  = "liveness-symlink-target"
	fifoLiveReadRepeats    = 3
	fifoLiveRendezvousWait = 10 * time.Second
	fifoLiveReaderExitWait = 10 * time.Second
	fifoLiveSettleWindow   = 500 * time.Millisecond
)

// fifoLiveOutcome is one liveness read. Consumers record it verbatim into
// published evidence, so every field is self-describing: Detail says which arm
// fired and why, and Mode carries the gate's own input so a reader of the
// evidence sees what was admitted or rejected, not merely the verdict.
type fifoLiveOutcome struct {
	Verdict   string `json:"verdict"`
	Detail    string `json:"detail"`
	Path      string `json:"path"`
	Mode      string `json:"mode,omitempty"`
	Errno     int    `json:"errno,omitempty"`
	ErrnoName string `json:"errno_name,omitempty"`
}

// fifoLiveRead answers "is some process currently holding this FIFO's read
// end?" with no pid and no ps.
//
// It takes no *testing.T and never fails a test: an instrument failure observed
// mid-turn is a datum to publish, not a reason to abort the turn.
//
// The order of the three steps is the contract, not an implementation detail.
// The mode gate runs before the open, so a missing path surfaces as a failing
// Lstat and never reaches the open's errno arms.
func fifoLiveRead(path string) fifoLiveOutcome {
	info, err := os.Lstat(path)
	if err != nil {
		out := fifoLiveOutcome{
			Verdict: fifoLiveInstrumentFailed,
			Detail:  fmt.Sprintf("lstat %s: %v", path, err),
			Path:    path,
		}
		fifoLiveRecordErrno(&out, err)
		return out
	}

	// Positive allowlist: os.ModeNamedPipe and nothing else. A blacklist of
	// regular files would admit /dev/null, whose bare open succeeds with no
	// reader anywhere — the inverting failure this gate exists to close.
	mode := info.Mode()
	if mode.Type() != os.ModeNamedPipe {
		return fifoLiveOutcome{
			Verdict: fifoLiveInstrumentFailed,
			Detail: fmt.Sprintf("not-a-fifo: %s has mode %s; only a named pipe can "+
				"answer the reader-presence question", path, mode),
			Path: path,
			Mode: mode.String(),
		}
	}

	f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		out := fifoLiveClassifyOpenErr(err)
		out.Path = path
		out.Mode = mode.String()
		return out
	}
	// Closing immediately is load-bearing: this is a transient SECOND write end
	// retiring. POSIX delivers EOF to a FIFO reader only when the LAST writer
	// closes, so while holdProbeFIFO's write end is held the reader never
	// notices. Without a persistent write end held elsewhere, this Close would
	// be the last writer closing and would kill the very reader it observes.
	_ = f.Close()
	return fifoLiveOutcome{
		Verdict: fifoLiveReaderPresent,
		Detail: fmt.Sprintf("open(%s, O_WRONLY|O_NONBLOCK) succeeded: a process "+
			"currently holds the read end", path),
		Path: path,
		Mode: mode.String(),
	}
}

// fifoLiveClassifyOpenErr maps a non-nil error from the O_WRONLY|O_NONBLOCK
// open onto a verdict. ENXIO is the ONLY input that yields no-reader; every
// other error, errno or not, is instrument failure with the errno recorded.
//
// It is a pure function over error so the EACCES and EINTR arms can be covered
// without root-dependence or a skip: a real EACCES needs a mkfifo(…, 0o000)
// FIFO, which SUCCEEDS as uid 0, and EINTR cannot be produced from a real open
// on demand at all.
func fifoLiveClassifyOpenErr(err error) fifoLiveOutcome {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return fifoLiveOutcome{
			Verdict: fifoLiveInstrumentFailed,
			Detail: fmt.Sprintf("open carried no syscall.Errno, so the reader-presence "+
				"question was never answered: %v", err),
		}
	}

	out := fifoLiveOutcome{
		Errno:     int(errno),
		ErrnoName: fifoLiveErrnoName(errno),
	}
	if errors.Is(err, syscall.ENXIO) {
		out.Verdict = fifoLiveNoReader
		out.Detail = "open returned ENXIO: no process holds the read end"
		return out
	}
	out.Verdict = fifoLiveInstrumentFailed
	out.Detail = fmt.Sprintf("open failed with %s, which says nothing about reader "+
		"presence: %v", out.ErrnoName, err)
	return out
}

// fifoLiveErrnoNames covers the errnos this instrument can record.
//
// syscall.Errno.Error() returns the PLATFORM'S MESSAGE, which differs across
// systems for the same errno (ENXIO is "device not configured" on Darwin and
// "no such device or address" on Linux), so published evidence needs a stable
// name of our own. golang.org/x/sys/unix.ErrnoName is deliberately not used:
// x/sys is an indirect dependency, and importing it would promote it to direct
// — a go.mod edit, which is a production change.
var fifoLiveErrnoNames = map[syscall.Errno]string{
	syscall.ENXIO:   "ENXIO",
	syscall.EACCES:  "EACCES",
	syscall.EINTR:   "EINTR",
	syscall.EPERM:   "EPERM",
	syscall.ENOENT:  "ENOENT",
	syscall.ENOTDIR: "ENOTDIR",
	syscall.EMFILE:  "EMFILE",
	syscall.ENFILE:  "ENFILE",
	syscall.ELOOP:   "ELOOP",
}

// fifoLiveErrnoName returns a stable, platform-independent name. The fallback
// keeps the number rather than dropping it; the numeric value is carried in the
// outcome regardless.
func fifoLiveErrnoName(errno syscall.Errno) string {
	if name, ok := fifoLiveErrnoNames[errno]; ok {
		return name
	}
	return fmt.Sprintf("errno(%d)", int(errno))
}

// fifoLiveRecordErrno annotates an outcome with the errno behind err, if there
// is one. Deliberately separate from fifoLiveClassifyOpenErr: the classifier
// maps ENXIO onto no-reader, and that mapping must remain reachable only from
// the open.
func fifoLiveRecordErrno(out *fifoLiveOutcome, err error) {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		out.Errno = int(errno)
		out.ErrnoName = fifoLiveErrnoName(errno)
	}
}

// TestFIFOReaderLiveness_FlipsAcrossOneReaderLifetime proves the read FLIPS:
// one FIFO, one reader's lifetime, the write end held throughout.
//
// This is the criterion that catches an instrument which can only ever answer
// "reader present" — the dual of the indistinguishable-failure defect, and the
// one that actually breaks the consumer. A read that does not flip means every
// killed command reads as alive and the probe's liveness claim is unfalsifiable.
// Two differently constructed FIFOs would not prove it: the second FIFO's
// answer would say nothing about the first one's reader.
//
// No reader-exit cleanup is registered here, unlike
// TestProbeFIFOHold_HoldsReaderUntilCleanupRelease: this test reaps its
// reader mid-body on purpose, so such an assertion would be vacuous. The
// non-perturbation test owns that shape instead.
func TestFIFOReaderLiveness_FlipsAcrossOneReaderLifetime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fifoLiveFIFOName)

	// holdProbeFIFO creates the FIFO and holds the write end for the whole
	// test, so neither arm below can be explained by a missing writer.
	rendezvous := holdProbeFIFO(t, path)

	cmd := exec.Command(fifoLiveReaderCommand, path)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s %s: %v", fifoLiveReaderCommand, path, err)
	}
	select {
	case <-rendezvous:
		// Fires the instant the reader opens the read end — no poll lag.
	case <-time.After(fifoLiveRendezvousWait):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("rendezvous never fired within %s although a reader was started on %s",
			fifoLiveRendezvousWait, path)
	}

	present := fifoLiveRead(path)
	if present.Verdict != fifoLiveReaderPresent {
		t.Fatalf("liveness read with a live reader blocked on %s = %q (%s); want %q — an "+
			"instrument that cannot see a reader it is looking straight at reports every "+
			"live command as gone", path, present.Verdict, present.Detail, fifoLiveReaderPresent)
	}
	if !strings.HasPrefix(present.Mode, "p") {
		t.Errorf("reader-present outcome recorded mode %q; want the FIFO type char 'p' — "+
			"the evidence must show what the gate admitted", present.Mode)
	}

	// Kill AND reap. Kill alone does not flip the read: five consecutive samples
	// taken after Kill() and before Wait() were measured as reader-present, so a
	// test that skipped the Wait would fail every time and look like a broken
	// read rather than a missing reap.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill reader: %v", err)
	}
	// A killed process yields *exec.ExitError; that is the expected result here,
	// so nothing is asserted about it.
	_ = cmd.Wait()

	absent := fifoLiveRead(path)
	if absent.Verdict != fifoLiveNoReader {
		t.Fatalf("liveness read on the SAME path after the reader was killed and reaped = "+
			"%q (%s); want %q — the read does not flip, so a killed command is "+
			"indistinguishable from a live one and the liveness claim is unfalsifiable",
			absent.Verdict, absent.Detail, fifoLiveNoReader)
	}
	if absent.ErrnoName != "ENXIO" {
		t.Errorf("no-reader outcome recorded errno %d/%q; want ENXIO — ENXIO is the only "+
			"input allowed to produce %q", absent.Errno, absent.ErrnoName, fifoLiveNoReader)
	}

	// The write end is still held by holdProbeFIFO at this point, which is what
	// makes the flip attributable to the reader and not to the writer.
}

// TestFIFOReaderLiveness_NonPipePathsAreInstrumentFailures closes the inverting
// failure on the open's SUCCESS arm.
//
// The property under test is the gate, not the row list: every row fails if the
// Lstat mode check is removed, and the character-device row additionally fails
// if the check is inverted into a regular-file blacklist. That row measures the
// bare open on /dev/null rather than citing it — the open succeeds with no
// reader anywhere, which is precisely what a blacklist would report as
// "reader present".
//
// These rows construct their own paths: holdProbeFIFO creates a FIFO, which is
// the one thing none of them may be.
func TestFIFOReaderLiveness_NonPipePathsAreInstrumentFailures(t *testing.T) {
	tests := []struct {
		name string
		// setup returns the path to read.
		setup func(t *testing.T) string
		// wantDetail is a substring the outcome's Detail must carry, so the
		// evidence names which arm rejected the path.
		wantDetail string
		// wantMode is false only for the row where Lstat itself fails and there
		// is no mode to record.
		wantMode bool
		// bareOpenSucceeds marks the row that discriminates a positive
		// ModeNamedPipe gate from a regular-file blacklist.
		bareOpenSucceeds bool
	}{
		{
			name: "regular file",
			setup: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), fifoLiveRegularName)
				if err := os.WriteFile(p, []byte("not a fifo"), 0o600); err != nil {
					t.Fatalf("write regular file: %v", err)
				}
				return p
			},
			wantDetail: "not-a-fifo",
			wantMode:   true,
		},
		{
			name: "character device",
			setup: func(t *testing.T) string {
				return os.DevNull
			},
			wantDetail:       "not-a-fifo",
			wantMode:         true,
			bareOpenSucceeds: true,
		},
		{
			name: "directory",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			wantDetail: "not-a-fifo",
			wantMode:   true,
		},
		{
			name: "symlink to a fifo",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				target := filepath.Join(dir, fifoLiveSymlinkTarget)
				if err := syscall.Mkfifo(target, 0o600); err != nil {
					t.Fatalf("mkfifo %s: %v", target, err)
				}
				link := filepath.Join(dir, fifoLiveSymlinkName)
				if err := os.Symlink(target, link); err != nil {
					t.Fatalf("symlink %s -> %s: %v", link, target, err)
				}
				return link
			},
			wantDetail: "not-a-fifo",
			wantMode:   true,
		},
		{
			name: "missing path",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), fifoLiveMissingName)
			},
			// Naming lstat proves the mode gate ran FIRST: a missing path must
			// surface here and never as an errno from the open.
			wantDetail: "lstat",
			wantMode:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)

			if tt.bareOpenSucceeds {
				// Measured, not cited: this is the open a blacklist gate would
				// reach, and its success is what it would call reader-present.
				f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
				if err != nil {
					t.Fatalf("bare open(%s, O_WRONLY|O_NONBLOCK) = %v; the row is only "+
						"discriminating while this open SUCCEEDS with no reader anywhere", path, err)
				}
				_ = f.Close()
			}

			got := fifoLiveRead(path)
			if got.Verdict != fifoLiveInstrumentFailed {
				t.Fatalf("fifoLiveRead(%s) = %q (%s); want %q — a non-FIFO path can answer "+
					"neither arm of the reader-presence question",
					path, got.Verdict, got.Detail, fifoLiveInstrumentFailed)
			}
			// Both halves matter: the first catches the inverting failure (a
			// non-FIFO reported as a live command), the second catches a gate
			// that silently reports non-FIFOs as absent readers.
			if got.Verdict == fifoLiveReaderPresent || got.Verdict == fifoLiveNoReader {
				t.Fatalf("fifoLiveRead(%s) answered the liveness question with %q on a "+
					"non-FIFO path", path, got.Verdict)
			}
			if !strings.Contains(got.Detail, tt.wantDetail) {
				t.Errorf("detail = %q; want it to contain %q so the evidence names the arm "+
					"that rejected the path", got.Detail, tt.wantDetail)
			}
			if tt.wantMode {
				if got.Mode == "" {
					t.Errorf("mode not recorded for %s; the observed mode is the gate's own "+
						"input and must appear in the evidence", path)
				}
				if strings.HasPrefix(got.Mode, "p") {
					t.Errorf("mode = %q for %s; a named pipe here would mean the row is no "+
						"longer testing the gate", got.Mode, path)
				}
			} else if got.Mode != "" {
				t.Errorf("mode = %q although Lstat failed; there was no mode to observe", got.Mode)
			}
		})
	}
}

// TestFIFOReaderLiveness_OpenErrnoArms pins the invariant that makes the
// no-reader verdict mean something: ENXIO is the ONLY input that produces it.
// Every other errno, mapped or not, and every error carrying no errno at all,
// is instrument failure with the errno recorded numerically and by name.
//
// Driven with synthetic *os.PathError values rather than real opens: a genuine
// EACCES needs a mkfifo(…, 0o000) FIFO, which SUCCEEDS as uid 0 (so a container
// CI running as root would flip that row), and EINTR cannot be produced from a
// real open on demand. Synthetic errnos make the arm list exhaustive,
// root-independent, and skip-free.
func TestFIFOReaderLiveness_OpenErrnoArms(t *testing.T) {
	// An errno with no entry in fifoLiveErrnoNames, to exercise the fallback.
	const unmappedErrno = syscall.Errno(1 << 20)

	tests := []struct {
		name          string
		err           error
		wantVerdict   string
		wantErrno     int
		wantErrnoName string
		wantDetail    string
	}{
		{
			name:          "ENXIO is the only no-reader input",
			err:           &os.PathError{Op: "open", Path: "/probe/fifo", Err: syscall.ENXIO},
			wantVerdict:   fifoLiveNoReader,
			wantErrno:     int(syscall.ENXIO),
			wantErrnoName: "ENXIO",
			wantDetail:    "ENXIO",
		},
		{
			name:          "EACCES",
			err:           &os.PathError{Op: "open", Path: "/probe/fifo", Err: syscall.EACCES},
			wantVerdict:   fifoLiveInstrumentFailed,
			wantErrno:     int(syscall.EACCES),
			wantErrnoName: "EACCES",
			wantDetail:    "EACCES",
		},
		{
			name:          "EINTR",
			err:           &os.PathError{Op: "open", Path: "/probe/fifo", Err: syscall.EINTR},
			wantVerdict:   fifoLiveInstrumentFailed,
			wantErrno:     int(syscall.EINTR),
			wantErrnoName: "EINTR",
			wantDetail:    "EINTR",
		},
		{
			name:          "EPERM",
			err:           &os.PathError{Op: "open", Path: "/probe/fifo", Err: syscall.EPERM},
			wantVerdict:   fifoLiveInstrumentFailed,
			wantErrno:     int(syscall.EPERM),
			wantErrnoName: "EPERM",
			wantDetail:    "EPERM",
		},
		{
			name:          "EMFILE",
			err:           &os.PathError{Op: "open", Path: "/probe/fifo", Err: syscall.EMFILE},
			wantVerdict:   fifoLiveInstrumentFailed,
			wantErrno:     int(syscall.EMFILE),
			wantErrnoName: "EMFILE",
			wantDetail:    "EMFILE",
		},
		{
			name:          "ENFILE",
			err:           &os.PathError{Op: "open", Path: "/probe/fifo", Err: syscall.ENFILE},
			wantVerdict:   fifoLiveInstrumentFailed,
			wantErrno:     int(syscall.ENFILE),
			wantErrnoName: "ENFILE",
			wantDetail:    "ENFILE",
		},
		{
			name:          "ELOOP",
			err:           &os.PathError{Op: "open", Path: "/probe/fifo", Err: syscall.ELOOP},
			wantVerdict:   fifoLiveInstrumentFailed,
			wantErrno:     int(syscall.ELOOP),
			wantErrnoName: "ELOOP",
			wantDetail:    "ELOOP",
		},
		{
			name:          "unmapped errno keeps its number",
			err:           &os.PathError{Op: "open", Path: "/probe/fifo", Err: unmappedErrno},
			wantVerdict:   fifoLiveInstrumentFailed,
			wantErrno:     int(unmappedErrno),
			wantErrnoName: fmt.Sprintf("errno(%d)", int(unmappedErrno)),
			wantDetail:    "errno(",
		},
		{
			name:          "error carrying no errno",
			err:           errors.New("synthetic non-errno failure"),
			wantVerdict:   fifoLiveInstrumentFailed,
			wantErrno:     0,
			wantErrnoName: "",
			wantDetail:    "synthetic non-errno failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fifoLiveClassifyOpenErr(tt.err)

			if got.Verdict != tt.wantVerdict {
				t.Errorf("verdict = %q (%s); want %q", got.Verdict, got.Detail, tt.wantVerdict)
			}
			// The invariant, spelled out per row: no error path other than
			// ENXIO may reach the no-reader verdict. A classifier that leaks any
			// other errno into it turns a broken instrument into a liveness
			// claim — the defect class #1230 and #1235 each paid for once.
			if tt.wantVerdict != fifoLiveNoReader && got.Verdict == fifoLiveNoReader {
				t.Errorf("%v classified as %q; ENXIO is the only input allowed to produce it",
					tt.err, fifoLiveNoReader)
			}
			if got.Errno != tt.wantErrno {
				t.Errorf("errno = %d; want %d", got.Errno, tt.wantErrno)
			}
			if got.ErrnoName != tt.wantErrnoName {
				t.Errorf("errno name = %q; want %q — published evidence needs a stable name, "+
					"not the platform's message text", got.ErrnoName, tt.wantErrnoName)
			}
			if !strings.Contains(got.Detail, tt.wantDetail) {
				t.Errorf("detail = %q; want it to contain %q", got.Detail, tt.wantDetail)
			}
		})
	}
}

// TestFIFOReaderLiveness_RepeatedReadsDoNotPerturbReader demonstrates
// non-perturbation instead of quoting POSIX at it.
//
// Each read opens a transient SECOND write end and closes it. POSIX delivers
// EOF to a FIFO reader only when the LAST writer closes, so while holdProbeFIFO
// holds the first one the reader never notices — but a consumer taking this read
// mid-turn is entitled to see that measured, not asserted.
//
// Cleanup ordering follows TestProbeFIFOHold_HoldsReaderUntilCleanupRelease:
// the reader-exit assertion is registered BEFORE holdProbeFIFO so
// t.Cleanup's LIFO order puts it after the release. Registering it later would
// run it before the release and fail spuriously; killing the reader after
// holdProbeFIFO would make it pass vacuously.
func TestFIFOReaderLiveness_RepeatedReadsDoNotPerturbReader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fifoLiveFIFOName)

	var (
		cmd    *exec.Cmd
		exited = make(chan struct{})
	)

	// Registered FIRST → runs LAST, after holdProbeFIFO's release.
	t.Cleanup(func() {
		if cmd == nil {
			return
		}
		select {
		case <-exited:
		case <-time.After(fifoLiveReaderExitWait):
			_ = cmd.Process.Kill()
			t.Errorf("reader did not exit within %s of the cleanup release — the write end "+
				"was not the last one closed, so one of the liveness reads leaked its "+
				"transient write end", fifoLiveReaderExitWait)
		}
	})

	rendezvous := holdProbeFIFO(t, path)

	cmd = exec.Command(fifoLiveReaderCommand, path)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s %s: %v", fifoLiveReaderCommand, path, err)
	}
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	select {
	case <-rendezvous:
	case <-time.After(fifoLiveRendezvousWait):
		_ = cmd.Process.Kill()
		t.Fatalf("rendezvous never fired within %s although a reader was started on %s",
			fifoLiveRendezvousWait, path)
	}

	for i := 0; i < fifoLiveReadRepeats; i++ {
		got := fifoLiveRead(path)
		if got.Verdict != fifoLiveReaderPresent {
			t.Fatalf("liveness read %d/%d = %q (%s); want %q — the reader is still blocked "+
				"on the read end and nothing but a previous read could have disturbed it",
				i+1, fifoLiveReadRepeats, got.Verdict, got.Detail, fifoLiveReaderPresent)
		}
	}

	// The assertion that would fail if each read's Close had EOF'd the reader.
	select {
	case <-exited:
		t.Fatalf("reader exited after %d liveness reads while the write end was still held — "+
			"the read perturbs the very thing it observes", fifoLiveReadRepeats)
	case <-time.After(fifoLiveSettleWindow):
	}

	// The pre-registered cleanup now proves the reader exits on the release and
	// not before, so the reads left the hold intact.
}
