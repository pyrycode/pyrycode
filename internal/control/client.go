package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// DialTimeout is the default timeout for client connections to the control
// socket. Short — the server is local and a slow response means something is
// wrong, not slow.
const DialTimeout = 5 * time.Second

// Status connects to the control socket, requests a status snapshot, and
// returns the payload. The context's deadline is honored if set; otherwise
// DialTimeout applies.
func Status(ctx context.Context, socketPath string) (*StatusPayload, error) {
	resp, err := request(ctx, socketPath, Request{Verb: VerbStatus})
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	if resp.Status == nil {
		return nil, errors.New("control: empty status response")
	}
	return resp.Status, nil
}

// Logs fetches the recent supervisor log lines from the daemon. Lines are
// returned oldest first.
func Logs(ctx context.Context, socketPath string) (*LogsPayload, error) {
	resp, err := request(ctx, socketPath, Request{Verb: VerbLogs})
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	if resp.Logs == nil {
		return nil, errors.New("control: empty logs response")
	}
	return resp.Logs, nil
}

// Stop asks the daemon to shut down. Returns when the server has acknowledged
// the request — the supervisor may still be unwinding its child process and
// removing the socket file. Callers that need to wait for full shutdown can
// poll Status until it returns a dial error.
func Stop(ctx context.Context, socketPath string) error {
	resp, err := request(ctx, socketPath, Request{Verb: VerbStop})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if !resp.OK {
		return errors.New("control: stop response missing ok flag")
	}
	return nil
}

// SendResize asks the daemon to apply a window-size update to the named
// session. Empty sessionID selects the bootstrap session. cols/rows are
// the client's local terminal dimensions; either being zero is treated by
// the server as "no change". A successful return means the server received
// and dispatched the request — the seam's own success is best-effort and
// not visible to the client.
//
// Callers (e.g. a SIGWINCH handler) should not retry on transient failure;
// the next SIGWINCH will re-emit a fresh resize.
func SendResize(ctx context.Context, socketPath, sessionID string, cols, rows int) error {
	resp, err := request(ctx, socketPath, Request{
		Verb:   VerbResize,
		Resize: &ResizePayload{SessionID: sessionID, Cols: cols, Rows: rows},
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if !resp.OK {
		return errors.New("control: resize response missing ok flag")
	}
	return nil
}

// SessionsNew asks the daemon to mint a new session with the given
// (possibly empty) label and returns the new session's UUID. In-process Go
// callers (the future cmd/pyry sessions new) consume this directly. Same
// one-shot dial → encode → decode → close lifecycle as Status/Logs/Stop/
// SendResize.
//
// Empty label sends {"verb":"sessions.new","sessions":{}}; the inner
// SessionsPayload is non-nil so the field is present, but Label's
// omitempty drops the empty string. The server treats nil and empty-Label
// identically.
func SessionsNew(ctx context.Context, socketPath, label string) (string, error) {
	resp, err := request(ctx, socketPath, Request{
		Verb:     VerbSessionsNew,
		Sessions: &SessionsPayload{Label: label},
	})
	if err != nil {
		return "", err
	}
	if resp.Error != "" {
		return "", errors.New(resp.Error)
	}
	if resp.SessionsNew == nil || resp.SessionsNew.SessionID == "" {
		return "", errors.New("control: empty sessions.new response")
	}
	return resp.SessionsNew.SessionID, nil
}

// request sends one Request and reads one Response over a fresh connection.
// Used by all client verbs.
func request(ctx context.Context, socketPath string, req Request) (*Response, error) {
	conn, err := dial(ctx, socketPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(DialTimeout)
	}
	_ = conn.SetDeadline(deadline)

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return &resp, nil
}

func dial(ctx context.Context, socketPath string) (net.Conn, error) {
	var d net.Dialer
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DialTimeout)
		defer cancel()
	}
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", socketPath, err)
	}
	return conn, nil
}
