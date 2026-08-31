package main

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// TestWriteVerdictResponse asserts writeVerdictResponse emits the right needle in the
// assistant line + a result line per verdict, verified through the REAL
// streamsup.Parser (parseEmitted, from stream_detect_test.go) — the same daemon-side
// consumer, so a shape bug is caught on the emit side (different fabric). The full
// dial→verdict loop needs a live daemon socket and is covered by the e2e
// (relay_v2_stream_modal_test.go), not here.
func TestWriteVerdictResponse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		verdict approveVerdict
		needle  string
	}{
		{"allow", verdictAllow, approveAllowNeedle},
		{"deny", verdictDeny, approveDenyNeedle},
		{"error", verdictError, approveErrorNeedle},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			if err := writeVerdictResponse(&buf, "m1", tc.verdict); err != nil {
				t.Fatalf("writeVerdictResponse: %v", err)
			}
			events := parseEmitted(t, buf.Bytes())
			if len(events) != 2 {
				t.Fatalf("got %d events, want 2 (TextChunk + TurnEnd): %+v", len(events), events)
			}
			tc0, ok := events[0].(turnevent.TextChunk)
			if !ok {
				t.Fatalf("event[0] = %T, want turnevent.TextChunk", events[0])
			}
			if tc0.Text != tc.needle {
				t.Errorf("TextChunk.Text = %q, want the %s needle %q", tc0.Text, tc.name, tc.needle)
			}
			if _, ok := events[1].(turnevent.TurnEnd); !ok {
				t.Fatalf("event[1] = %T, want turnevent.TurnEnd", events[1])
			}
		})
	}
}

// TestRunStreamJSONApprove_EmitsToolUseAheadOfTheVerdict pins the rider's emitted
// SHAPE (#1918): one user turn produces the gated call's assistant tool_use block, then
// the verdict echo, then the result — the order every committed permission capture
// shows and the one real claude drives, where the deny-default boundary sits BETWEEN
// the tool_use emission and its tool_result.
//
// Offline by construction: an empty socketFile makes readApproveSocket refuse
// immediately, so dialApproval returns verdictError with no dial and no timeout. That
// also makes this the fail-closed row — an approval failure still maps to the error
// needle, never to allow, with the block now written ahead of it.
//
// Asserted through the REAL streamsup.Parser (parseEmitted), TestWriteVerdictResponse's
// discipline, which is what makes this pin the block's shape rather than its bytes: a
// block missing `name`, or nested under the wrong envelope, produces no ToolStart at all.
func TestRunStreamJSONApprove_EmitsToolUseAheadOfTheVerdict(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	runStreamJSONApprove(strings.NewReader(userTurnLine("gate this")+"\n"), &buf, "")

	events := parseEmitted(t, buf.Bytes())
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3 (ToolStart, TextChunk, TurnEnd): %+v", len(events), events)
	}
	start, ok := events[0].(turnevent.ToolStart)
	if !ok {
		t.Fatalf("event[0] = %T, want turnevent.ToolStart — the tool_use block must precede the verdict", events[0])
	}
	// The rider's per-turn id scheme, first turn.
	if start.ToolCallID != "tu-1139-1" {
		t.Errorf("ToolStart.ToolCallID = %q, want %q", start.ToolCallID, "tu-1139-1")
	}
	if start.Title != approveToolName {
		t.Errorf("ToolStart.Title = %q, want %q", start.Title, approveToolName)
	}
	if !sameJSONObject(t, start.RawInput, []byte(approveToolInput)) {
		t.Errorf("ToolStart.RawInput = %s, want the same object as approveToolInput (%s)", start.RawInput, approveToolInput)
	}

	text, ok := events[1].(turnevent.TextChunk)
	if !ok {
		t.Fatalf("event[1] = %T, want turnevent.TextChunk", events[1])
	}
	// Fail-closed with the block in front of it: a client-side approval failure still
	// reflects the error needle, never approve-allow.
	if text.Text != approveErrorNeedle {
		t.Errorf("TextChunk.Text = %q, want the error needle %q", text.Text, approveErrorNeedle)
	}
	if _, ok := events[2].(turnevent.TurnEnd); !ok {
		t.Fatalf("event[2] = %T, want turnevent.TurnEnd", events[2])
	}
}

// TestRunStreamJSONApprove_ToolUsePrecedesTheDial is the ORDERING pin, and it
// deliberately observes from the SOCKET side rather than from the emitted bytes. Byte
// order alone cannot pin "before it dials": a rider that dialled first, then wrote the
// block, then the verdict, emits identical bytes. What separates the two is what had
// already been written when the approval ARRIVED — so a stub server snapshots the
// rider's output at request time, and a snapshot carrying no ToolStart is the mutant's
// signature.
//
// The same handler pins AC 1's other half: the block's id, name and input must equal
// the ones the approve payload carries, so the two paths describe ONE call rather than
// two that merely both exist.
//
// Concurrency: the rider writes `out` from this goroutine while the server reads it
// from its own, so the writer is mutex-guarded with the snapshot taken under the same
// lock, and the server goroutine is JOINED before any assertion reads what it recorded.
func TestRunStreamJSONApprove_ToolUsePrecedesTheDial(t *testing.T) {
	t.Parallel()

	dir := shortSocketDir(t)
	sock := filepath.Join(dir, "p.sock")
	socketFile := filepath.Join(dir, "socket.txt")
	if err := os.WriteFile(socketFile, []byte(sock), 0o600); err != nil {
		t.Fatalf("write approve socket file: %v", err)
	}

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	out := &lockedBuffer{}
	var (
		snapshot []byte
		payload  control.ApprovePayload
		gotDial  bool
	)
	// Closed unconditionally, so a failed accept or decode ends the join instead of
	// hanging the test; gotDial then reports the approval never arrived.
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// FIRST, before decoding anything: what the rider had already written when its
		// approval reached the socket is the entire question this test asks.
		snapshot = out.snapshot()
		var req control.Request
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			return
		}
		if req.Approve == nil {
			return
		}
		payload = *req.Approve
		gotDial = true
		_ = json.NewEncoder(conn).Encode(control.Response{
			Approve: &control.ApproveResult{Behavior: "allow"},
		})
	}()

	runStreamJSONApprove(strings.NewReader(userTurnLine("gate this")+"\n"), out, socketFile)
	<-done

	if !gotDial {
		t.Fatal("the rider never delivered an mcp.approve request to the stub socket")
	}

	events := parseEmitted(t, snapshot)
	if len(events) != 1 {
		t.Fatalf("output at dial time mapped to %d events, want exactly 1 (the tool_use block): %+v\n%s",
			len(events), events, snapshot)
	}
	start, ok := events[0].(turnevent.ToolStart)
	if !ok {
		t.Fatalf("output at dial time mapped to %T, want turnevent.ToolStart — the block must be written BEFORE the dial", events[0])
	}
	// One call, described once: the stream feed and the approval must join on the id,
	// and agree on what the call IS.
	if start.ToolCallID != payload.ToolUseID {
		t.Errorf("ToolStart.ToolCallID = %q, approve payload ToolUseID = %q; the two paths describe different calls",
			start.ToolCallID, payload.ToolUseID)
	}
	if start.Title != payload.ToolName {
		t.Errorf("ToolStart.Title = %q, approve payload ToolName = %q", start.Title, payload.ToolName)
	}
	if !sameJSONObject(t, start.RawInput, payload.Input) {
		t.Errorf("ToolStart.RawInput = %s, approve payload Input = %s", start.RawInput, payload.Input)
	}

	// The verdict path still works with the block in front of it.
	full := parseEmitted(t, out.snapshot())
	if len(full) != 3 {
		t.Fatalf("full output mapped to %d events, want 3 (ToolStart, TextChunk, TurnEnd): %+v", len(full), full)
	}
	text, ok := full[1].(turnevent.TextChunk)
	if !ok {
		t.Fatalf("full event[1] = %T, want turnevent.TextChunk", full[1])
	}
	if text.Text != approveAllowNeedle {
		t.Errorf("TextChunk.Text = %q, want the allow needle %q", text.Text, approveAllowNeedle)
	}
}

// lockedBuffer is an io.Writer whose accumulated bytes can be snapshotted from
// another goroutine. Both halves take the same mutex, which is what gives the
// ordering test its happens-before edge: the rider's write completes under the lock
// before the dial it precedes, and the server's snapshot takes the lock after the
// accept that dial caused.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// snapshot returns a COPY of what has been written so far. A copy, not
// buf.Bytes(): the returned slice is read after the lock is released and a later
// Write may reallocate or overwrite the buffer's backing array.
func (b *lockedBuffer) snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}

// shortSocketDir returns a tempdir short enough to hold a unix socket path.
// t.TempDir() embeds the test name under /var/folders/… on macOS, which blows past
// the 104-byte sun_path limit; /tmp is short. Same recipe as control's shortTempDir.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fakeclaudeapprove")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// sameJSONObject reports whether two raw JSON blobs decode to the same value —
// compared as decoded objects rather than as bytes, so the assertion is about the
// call being described identically and not about incidental encoding.
func sameJSONObject(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("unmarshal %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return reflect.DeepEqual(av, bv)
}

// TestApproveVerdictNeedlesDistinct guards the fail-closed oracle: the three needles
// must be pairwise distinct AND not substring-confusable, because the e2e discriminates
// them with strings.Contains — a daemon deny (approve-deny) must never read as a client
// error (approve-error) or a fail-open (approve-allow), which is what makes the timeout
// case's fail-closed proof airtight.
func TestApproveVerdictNeedlesDistinct(t *testing.T) {
	t.Parallel()
	needles := []string{approveAllowNeedle, approveDenyNeedle, approveErrorNeedle}
	for i := range needles {
		for j := i + 1; j < len(needles); j++ {
			if strings.Contains(needles[i], needles[j]) || strings.Contains(needles[j], needles[i]) {
				t.Errorf("needles %q and %q are substring-confusable; the e2e discriminates via strings.Contains", needles[i], needles[j])
			}
		}
	}
}
