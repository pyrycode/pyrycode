package control

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

// startMisbehavingServer starts a Unix-socket listener on a per-test path and
// runs handler(conn) once for the first incoming connection. Used to exercise
// client-side defensive paths — e.g. "real server would never do this, but
// here's what the client does if it does."
func startMisbehavingServer(t *testing.T, handler func(conn net.Conn)) string {
	t.Helper()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		handler(conn)
	}()

	return sock
}

// TestStatus_EmptyResponseSurfaced confirms the client reports a clear error
// when the server returns a Response with neither Status nor Error set —
// possible only via a server bug, but the client must not treat the empty
// response as success and fabricate a zero-value StatusPayload.
func TestStatus_EmptyResponseSurfaced(t *testing.T) {
	t.Parallel()

	sock := startMisbehavingServer(t, func(conn net.Conn) {
		var req Request
		_ = json.NewDecoder(conn).Decode(&req)
		_ = json.NewEncoder(conn).Encode(Response{}) // empty success
	})

	_, err := Status(context.Background(), sock)
	if err == nil {
		t.Fatal("expected error on empty status response")
	}
	if !strings.Contains(err.Error(), "empty status response") {
		t.Errorf("error %q should mention 'empty status response'", err)
	}
}

func TestLogs_EmptyResponseSurfaced(t *testing.T) {
	t.Parallel()

	sock := startMisbehavingServer(t, func(conn net.Conn) {
		var req Request
		_ = json.NewDecoder(conn).Decode(&req)
		_ = json.NewEncoder(conn).Encode(Response{}) // empty success
	})

	_, err := Logs(context.Background(), sock)
	if err == nil {
		t.Fatal("expected error on empty logs response")
	}
	if !strings.Contains(err.Error(), "empty logs response") {
		t.Errorf("error %q should mention 'empty logs response'", err)
	}
}

func TestStop_OKMissingFlagSurfaced(t *testing.T) {
	t.Parallel()

	sock := startMisbehavingServer(t, func(conn net.Conn) {
		var req Request
		_ = json.NewDecoder(conn).Decode(&req)
		// Server returns success but without OK=true. Client should not
		// silently accept this — the OK flag is the contract.
		_ = json.NewEncoder(conn).Encode(Response{})
	})

	err := Stop(context.Background(), sock)
	if err == nil {
		t.Fatal("expected error when stop ack lacks OK flag")
	}
	if !strings.Contains(err.Error(), "missing ok flag") {
		t.Errorf("error %q should mention 'missing ok flag'", err)
	}
}

// TestRequest_ConnRefusedDuringEncode triggers the encoder-side error path
// in request(). A server that accepts but immediately closes the conn makes
// the json.NewEncoder(conn).Encode(req) call fail with a write error.
func TestRequest_ConnRefusedDuringEncode(t *testing.T) {
	t.Parallel()

	sock := startMisbehavingServer(t, func(conn net.Conn) {
		// Close immediately. Client encode might race with this — but
		// even if it succeeds, the subsequent decode will EOF.
		_ = conn.Close()
	})

	_, err := Status(context.Background(), sock)
	if err == nil {
		t.Fatal("expected error when server closes immediately")
	}
}
