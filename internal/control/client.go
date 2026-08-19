package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
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

// SessionsNew asks the daemon to mint a new session with the given
// (possibly empty) label and returns the new session's UUID. In-process Go
// callers (the future cmd/pyry sessions new) consume this directly. Same
// one-shot dial → encode → decode → close lifecycle as Status/Logs/Stop.
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

// SessionsRm asks the daemon to remove the named session and apply the
// JSONL disposition policy. Empty policy is treated by the server as
// JSONLPolicyLeave (matches sessions.JSONLLeave's zero-value default).
//
// Typed errors propagate via Response.ErrorCode — a server response
// carrying ErrCodeSessionNotFound returns sessions.ErrSessionNotFound
// directly so callers can errors.Is against it; same for
// ErrCodeCannotRemoveBootstrap. Other server errors (no sessioner
// configured, missing id, evict failures, ...) return as
// errors.New(resp.Error) verbatim.
func SessionsRm(ctx context.Context, socketPath, id string, policy JSONLPolicy) error {
	resp, err := request(ctx, socketPath, Request{
		Verb:     VerbSessionsRm,
		Sessions: &SessionsPayload{ID: id, JSONLPolicy: policy},
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		switch resp.ErrorCode {
		case ErrCodeSessionNotFound:
			return sessions.ErrSessionNotFound
		case ErrCodeCannotRemoveBootstrap:
			return sessions.ErrCannotRemoveBootstrap
		}
		return errors.New(resp.Error)
	}
	if !resp.OK {
		return errors.New("control: sessions.rm response missing ok flag")
	}
	return nil
}

// SessionsRename asks the daemon to update the named session's
// human-friendly label. Empty newLabel is a valid argument meaning "clear
// the label" — Pool.Rename treats it as such (per #62) and the wire
// forwards it unchanged via SessionsPayload.NewLabel's omitempty tag (an
// empty string elides the field, and the server decodes the absent field
// as "").
//
// Typed errors propagate via Response.ErrorCode — a server response
// carrying ErrCodeSessionNotFound returns sessions.ErrSessionNotFound
// directly so callers can errors.Is against it. Other server errors (no
// sessioner configured, missing id, registry persist failures, ...)
// return as errors.New(resp.Error) verbatim.
func SessionsRename(ctx context.Context, socketPath, id, newLabel string) error {
	resp, err := request(ctx, socketPath, Request{
		Verb:     VerbSessionsRename,
		Sessions: &SessionsPayload{ID: id, NewLabel: newLabel},
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		if resp.ErrorCode == ErrCodeSessionNotFound {
			return sessions.ErrSessionNotFound
		}
		return errors.New(resp.Error)
	}
	if !resp.OK {
		return errors.New("control: sessions.rename response missing ok flag")
	}
	return nil
}

// SessionsList asks the daemon for a snapshot of every session in the
// pool and returns the result. In-process Go callers (the future
// cmd/pyry sessions list) consume this directly. Same one-shot dial →
// encode → decode → close lifecycle as Status/Logs/Stop/SessionsNew/
// SessionsRm/SessionsRename.
//
// Snapshot ordering is whatever the server returned (Pool.List's
// LastActiveAt desc, SessionID asc tiebreak); callers needing a
// different order are responsible for re-sorting.
//
// A nil SessionsList payload on a non-error response is treated as a
// malformed response. An explicit zero-length slice
// ({"sessionsList":{"sessions":[]}}) decodes to a non-nil payload with
// len(Sessions) == 0 and is returned as a well-formed empty result.
//
// No typed-sentinel mapping (Pool.List does not return errors). All
// server errors flow through Response.Error verbatim.
func SessionsList(ctx context.Context, socketPath string) ([]SessionInfo, error) {
	resp, err := request(ctx, socketPath, Request{Verb: VerbSessionsList})
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	if resp.SessionsList == nil {
		return nil, errors.New("control: empty sessions.list response")
	}
	return resp.SessionsList.Sessions, nil
}

// SessionsHasID asks the daemon whether a session is currently
// registered under the given UUID. Returns true for a known UUID,
// false for a well-formed UUID that is absent, and an error for
// empty / malformed input or transport failure.
//
// In-process Go callers (the future 1.3c-2 foreground auto-attach
// path) consume this directly. Same one-shot dial → encode → decode →
// close lifecycle as Status/Logs/Stop/SessionsNew/SessionsRm/
// SessionsRename/SessionsList.
//
// No typed-sentinel mapping. Server-side validation errors flow
// through Response.Error verbatim.
func SessionsHasID(ctx context.Context, socketPath, id string) (bool, error) {
	resp, err := request(ctx, socketPath, Request{
		Verb:     VerbSessionsHasID,
		Sessions: &SessionsPayload{ID: id},
	})
	if err != nil {
		return false, err
	}
	if resp.Error != "" {
		return false, errors.New(resp.Error)
	}
	if resp.SessionsHasID == nil {
		return false, errors.New("control: empty sessions.has-id response")
	}
	return resp.SessionsHasID.Has, nil
}

// Rekey asks the daemon to trigger an immediate Noise re-key on the
// named v2 conn. Returns nil on a successful enqueue (the underlying
// handshake runs asynchronously on the conn's state machine — the
// helper does not wait for it).
//
// ErrConnNotFound is reconstructed from Response.ErrorCode so callers
// can errors.Is against it. Other server errors (no rekeyer configured,
// missing connID, manager-internal failures) return as
// errors.New(resp.Error) verbatim.
//
// No production caller exists in slice A (#459) — wired now so slice B
// (#460) is a one-file change in cmd/pyry/. Same one-shot dial → encode
// → decode → close lifecycle as the other client helpers.
func Rekey(ctx context.Context, socketPath, connID string) error {
	resp, err := request(ctx, socketPath, Request{
		Verb:  VerbRekey,
		Rekey: &RekeyPayload{ConnID: connID},
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		if resp.ErrorCode == ErrCodeConnNotFound {
			return ErrConnNotFound
		}
		return errors.New(resp.Error)
	}
	if !resp.OK {
		return errors.New("control: rekey response missing ok flag")
	}
	return nil
}

// Approve forwards a claude tool-approval request to the daemon over the
// control socket (the mcp.approve verb, #1104) and returns the daemon's
// allow/deny verdict. It is the client half consumed by the `pyry mcp-approve`
// subcommand, which re-frames the verdict as the MCP tool result claude blocks
// on.
//
// Callers MUST pass a ctx whose deadline is >= the daemon's approval window.
// request installs that deadline as the conn read deadline, so the read stays
// patient while the daemon holds the conn open for the (human) decision. An
// undeadlined ctx falls back to DialTimeout (5s) and would prematurely time out
// a live approval — a safe but premature deny. This patient-read requirement is
// the sole behavioural difference from the other client helpers, which are all
// sub-second round-trips. The dial itself still fails fast (<= dialRetryBudget,
// ~1.5s) on an unreachable socket even under a long-deadline ctx (see dial.go),
// so a missing daemon yields a bounded error rather than a hang.
//
// Any error (dial/transport/decode, a server-side Response.Error, or an empty
// verdict) is returned verbatim: the subcommand fail-closes to a deny on any
// error, so no typed-sentinel mapping is warranted.
func Approve(ctx context.Context, socketPath string, req ApprovePayload) (*ApproveResult, error) {
	resp, err := request(ctx, socketPath, Request{Verb: VerbMCPApprove, Approve: &req})
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	if resp.Approve == nil {
		return nil, errors.New("control: empty mcp.approve response")
	}
	return resp.Approve, nil
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
