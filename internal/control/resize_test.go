package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

// sendResizeRequest dials the server, sends a VerbResize request with the
// given payload, and decodes one response. Mirrors what SendResize does on
// the wire but lets each test inspect the raw response fields directly.
func sendResizeRequest(t *testing.T, sock string, payload *ResizePayload) Response {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	req := Request{Verb: VerbResize, Resize: payload}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

// TestServer_Resize_AppliesToSeam pins the wire-cols/rows → seam-rows/cols
// swap and verifies the server invokes Session.Resize on receipt.
func TestServer_Resize_AppliesToSeam(t *testing.T) {
	t.Parallel()

	sess := &fakeSession{}
	sock, stop := startServer(t, &fakeResolver{sess: sess})
	defer stop()

	resp := sendResizeRequest(t, sock, &ResizePayload{Cols: 120, Rows: 40})
	if resp.Error != "" {
		t.Fatalf("unexpected Error: %q", resp.Error)
	}
	if !resp.OK {
		t.Fatalf("expected OK ack, got %+v", resp)
	}

	got := sess.recordedResizeCalls()
	want := []resizeCall{{Rows: 40, Cols: 120}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("recordedResizeCalls() = %+v, want %+v", got, want)
	}
}

// TestServer_Resize_ZeroDimNoOp covers the omitempty sentinel: zero in
// either dimension means "no change" — the server still acks OK but does
// not invoke the seam.
func TestServer_Resize_ZeroDimNoOp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload *ResizePayload
	}{
		{"both zero", &ResizePayload{Cols: 0, Rows: 0}},
		{"zero cols", &ResizePayload{Cols: 0, Rows: 40}},
		{"zero rows", &ResizePayload{Cols: 120, Rows: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sess := &fakeSession{}
			sock, stop := startServer(t, &fakeResolver{sess: sess})
			defer stop()

			resp := sendResizeRequest(t, sock, tt.payload)
			if resp.Error != "" {
				t.Fatalf("unexpected Error: %q", resp.Error)
			}
			if !resp.OK {
				t.Fatalf("expected OK ack, got %+v", resp)
			}
			if calls := sess.recordedResizeCalls(); len(calls) != 0 {
				t.Errorf("recordedResizeCalls() = %+v, want none", calls)
			}
		})
	}

	t.Run("missing payload", func(t *testing.T) {
		t.Parallel()
		sess := &fakeSession{}
		sock, stop := startServer(t, &fakeResolver{sess: sess})
		defer stop()

		resp := sendResizeRequest(t, sock, nil)
		if resp.OK {
			t.Errorf("expected error response, got OK ack")
		}
		if resp.Error != "resize: missing payload" {
			t.Errorf("Error = %q, want \"resize: missing payload\"", resp.Error)
		}
		if calls := sess.recordedResizeCalls(); len(calls) != 0 {
			t.Errorf("recordedResizeCalls() = %+v, want none", calls)
		}
	})
}

// TestServer_Resize_UnknownSessionError surfaces the lookup-failure path
// — pre-seam, so the client gets an error response and the seam is not
// invoked.
func TestServer_Resize_UnknownSessionError(t *testing.T) {
	t.Parallel()

	sess := &fakeSession{}
	resolver := &fakeResolver{sess: sess, lookupErr: errors.New("no such session")}
	sock, stop := startServer(t, resolver)
	defer stop()

	resp := sendResizeRequest(t, sock, &ResizePayload{SessionID: "abc", Cols: 120, Rows: 40})
	if resp.OK {
		t.Errorf("expected error response, got OK ack")
	}
	if resp.Error != "resize: no such session" {
		t.Errorf("Error = %q, want \"resize: no such session\"", resp.Error)
	}
	if calls := sess.recordedResizeCalls(); len(calls) != 0 {
		t.Errorf("recordedResizeCalls() = %+v, want none", calls)
	}
}

// TestServer_Resize_SeamErrorReturnsOK pins the best-effort posture: the
// seam returned an error, but the client still gets OK (matches the
// handshake-geometry path in handleAttach).
func TestServer_Resize_SeamErrorReturnsOK(t *testing.T) {
	t.Parallel()

	sess := &fakeSession{resizeErr: errors.New("synthetic setsize failure")}
	sock, stop := startServer(t, &fakeResolver{sess: sess})
	defer stop()

	resp := sendResizeRequest(t, sock, &ResizePayload{Cols: 120, Rows: 40})
	if resp.Error != "" {
		t.Errorf("unexpected Error: %q (seam errors must be swallowed)", resp.Error)
	}
	if !resp.OK {
		t.Errorf("expected OK ack, got %+v", resp)
	}
	got := sess.recordedResizeCalls()
	want := []resizeCall{{Rows: 40, Cols: 120}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("recordedResizeCalls() = %+v, want %+v (seam must be invoked)", got, want)
	}
}

// TestServer_Resize_ForegroundSessionSilent verifies that
// sessions.ErrAttachUnavailable is swallowed silently — same posture as
// the handshake-geometry path.
func TestServer_Resize_ForegroundSessionSilent(t *testing.T) {
	t.Parallel()

	sess := &fakeSession{resizeErr: sessions.ErrAttachUnavailable}
	sock, stop := startServer(t, &fakeResolver{sess: sess})
	defer stop()

	resp := sendResizeRequest(t, sock, &ResizePayload{Cols: 120, Rows: 40})
	if resp.Error != "" {
		t.Errorf("unexpected Error: %q", resp.Error)
	}
	if !resp.OK {
		t.Errorf("expected OK ack, got %+v", resp)
	}
}

// TestServer_Resize_ClampsOversizeDims pins the int → uint16 clamp at the
// server boundary. Dimensions over math.MaxUint16 saturate rather than
// wrap.
func TestServer_Resize_ClampsOversizeDims(t *testing.T) {
	t.Parallel()

	sess := &fakeSession{}
	sock, stop := startServer(t, &fakeResolver{sess: sess})
	defer stop()

	resp := sendResizeRequest(t, sock, &ResizePayload{Cols: 70000, Rows: 40})
	if !resp.OK {
		t.Fatalf("expected OK ack, got %+v", resp)
	}
	got := sess.recordedResizeCalls()
	want := []resizeCall{{Rows: 40, Cols: 65535}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("recordedResizeCalls() = %+v, want %+v", got, want)
	}
}

// TestSendResize_RoundTrip pins the wire shape produced by the client-side
// SendResize helper. Mirrors TestAttach_ClientSendsSessionID's
// hand-rolled-listen pattern: the server side decodes the raw request and
// asserts every field arrived intact.
func TestSendResize_RoundTrip(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	gotReq := make(chan Request, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req Request
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			return
		}
		gotReq <- req
		_ = json.NewEncoder(conn).Encode(Response{OK: true})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := SendResize(ctx, sock, "abc", 100, 30); err != nil {
		t.Fatalf("SendResize: %v", err)
	}

	select {
	case req := <-gotReq:
		if req.Verb != VerbResize {
			t.Errorf("Verb = %q, want %q", req.Verb, VerbResize)
		}
		want := &ResizePayload{SessionID: "abc", Cols: 100, Rows: 30}
		if !reflect.DeepEqual(req.Resize, want) {
			t.Errorf("Resize = %+v, want %+v", req.Resize, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive a request")
	}
}

// TestSendResize_ServerError covers the error-response path: the server
// returned an Error string, SendResize must surface it verbatim.
func TestSendResize_ServerError(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req Request
		_ = json.NewDecoder(conn).Decode(&req)
		_ = json.NewEncoder(conn).Encode(Response{Error: "resize: synthetic"})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = SendResize(ctx, sock, "", 80, 24)
	if err == nil {
		t.Fatal("expected error from SendResize, got nil")
	}
	if err.Error() != "resize: synthetic" {
		t.Errorf("err = %q, want \"resize: synthetic\"", err.Error())
	}
}
