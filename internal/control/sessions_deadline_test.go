package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

// TestServer_SessionsNew_OpOutlastsHandshake is the #865 regression guard for
// sessions.new: an op that runs past the handshake deadline must still deliver
// the minted UUID, not an error or EOF. The handshake is shrunk to 200ms and
// Create is delayed 500ms, so the op reliably outlasts the pre-fix deadline
// (non-vacuous — the response write dies at 200ms without the extend) yet
// finishes far under the extended deadline.
func TestServer_SessionsNew_OpOutlastsHandshake(t *testing.T) {
	t.Parallel()

	const cannedID sessions.SessionID = "99999999-8888-7777-6666-555555555555"
	sessioner := &fakeSessioner{returnID: cannedID, opDelay: 500 * time.Millisecond}

	sock, stop := startServerWithSessionerHandshake(t, &fakeResolver{sess: &fakeSession{}}, sessioner, 200*time.Millisecond)
	defer stop()

	got, err := SessionsNew(context.Background(), sock, "slow-label")
	if err != nil {
		t.Fatalf("SessionsNew: %v", err)
	}
	if got != string(cannedID) {
		t.Errorf("SessionID = %q, want %q", got, cannedID)
	}
}

// TestServer_SessionsRm_OpOutlastsHandshake is the #865 regression guard for
// sessions.rm: a Remove that runs past the handshake deadline must still
// deliver the OK ack. Same shape and timing rationale as the sessions.new
// sibling.
func TestServer_SessionsRm_OpOutlastsHandshake(t *testing.T) {
	t.Parallel()

	sessioner := &fakeSessioner{opDelay: 500 * time.Millisecond}

	sock, stop := startServerWithSessionerHandshake(t, &fakeResolver{sess: &fakeSession{}}, sessioner, 200*time.Millisecond)
	defer stop()

	if err := SessionsRm(context.Background(), sock, "11111111-2222-3333-4444-555555555555", JSONLPolicyLeave); err != nil {
		t.Fatalf("SessionsRm: %v", err)
	}
	if got := len(sessioner.recordedRemoves()); got != 1 {
		t.Errorf("Remove calls = %d, want 1", got)
	}
}

// TestServer_SessionsVerbs_SlowClientStillTimedOut is the #865 AC3 guard:
// extending the conn deadline for the session verbs must not weaken the
// handshake-read bound. A client that connects and sends nothing is still cut
// off promptly, because the extend happens only after the request is decoded
// — a silent client never reaches it.
//
// Non-vacuous by construction: the read is timed and asserted to return well
// under the 30s op budget. A client read deadline sits far above the 200ms
// handshake but far below 30s, so if the server ever failed to time the
// silent client out (e.g. the extend were moved before the decode) the read
// would block until that client deadline and the elapsed check would fail.
func TestServer_SessionsVerbs_SlowClientStillTimedOut(t *testing.T) {
	t.Parallel()

	sock, stop := startServerWithSessionerHandshake(t, &fakeResolver{sess: &fakeSession{}}, &fakeSessioner{}, 200*time.Millisecond)
	defer stop()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Send no request. The 200ms handshake deadline must fire server-side.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 256)
	start := time.Now()
	n, readErr := conn.Read(buf)
	elapsed := time.Since(start)

	// The read must return promptly (server-side handshake close), well under
	// both the 30s op budget and the 10s client read deadline.
	if elapsed > 5*time.Second {
		t.Fatalf("read took %v; server did not time out the silent client promptly", elapsed)
	}
	// Whatever came back must not be a success response. Commonly it is EOF
	// (n==0) because the expired handshake deadline also kills the server's
	// decode-error write; if bytes did arrive they must carry an Error.
	if readErr == nil && n > 0 {
		var resp Response
		if err := json.Unmarshal(bytes.TrimSpace(buf[:n]), &resp); err == nil && resp.Error == "" {
			t.Fatalf("silent client received a non-error response: %+v", resp)
		}
	}
}
