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
