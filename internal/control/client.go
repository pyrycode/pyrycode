package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

// ChannelNew asks the daemon to create a promoted conversation — a channel —
// rooted at cwd, and returns the minted conversation id. cwd is the absolute
// directory the caller is standing in, sent RAW: the daemon canonicalises,
// confines it to $HOME and trust-marks the realpath, so this side does no path
// handling and a client cannot bypass any of it by pre-resolving. An empty name
// means "derive it from the base name of the resolved path".
//
// Same one-shot dial → encode → decode → close lifecycle as SessionsNew, and
// the same empty-result guard. No typed ErrorCode is mapped: every refusal this
// verb produces is a static message the caller prints rather than a sentinel it
// must reconstruct, so a server error arrives as errors.New(resp.Error)
// verbatim. That distinction is what lets a caller tell a server refusal from a
// transport failure — the latter arrives wrapped from request — without a
// hand-maintained list of message prefixes.
func ChannelNew(ctx context.Context, socketPath, cwd, name string) (string, error) {
	resp, err := request(ctx, socketPath, Request{
		Verb:    VerbChannelNew,
		Channel: &ChannelPayload{Cwd: cwd, Name: name},
	})
	if err != nil {
		return "", err
	}
	if resp.Error != "" {
		return "", errors.New(resp.Error)
	}
	if resp.ChannelNew == nil || resp.ChannelNew.ConversationID == "" {
		return "", errors.New("control: empty channel.new response")
	}
	return resp.ChannelNew.ConversationID, nil
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
// It is bounded by LIVENESS, not by duration — the sole behavioural difference
// from the other client helpers, which are all sub-second round-trips. No read
// deadline is derived from the ctx: an approval is held for as long as the
// daemon holds it, and a client-side ceiling would not merely lose the race for
// the verdict message but TERMINATE the approval, since closing the conn is what
// the server reads as a lost caller (#1929). The only things that end the wait
// are the caller cancelling ctx, the connection ending, and the daemon
// answering. An undeadlined ctx is therefore genuinely patient here rather than
// truncated at DialTimeout.
//
// The dial itself still fails fast (<= dialRetryBudget, ~1.5s) on an unreachable
// socket, independently of the ctx's deadline or absence of one (see dial.go),
// so a missing daemon yields a bounded error rather than a hang. A daemon that
// is alive, holds the conn open and never resolves is knowingly NOT bounded
// here; the daemon owns that guarantee (the pending-approval registry's timer,
// plus watchApproveConn's disconnect and shutdown denies).
//
// Any error (dial/transport/decode, a server-side Response.Error, or an empty
// verdict) is returned verbatim: the subcommand fail-closes to a deny on any
// error, so no typed-sentinel mapping is warranted.
func Approve(ctx context.Context, socketPath string, req ApprovePayload) (*ApproveResult, error) {
	resp, err := requestPatient(ctx, socketPath, Request{Verb: VerbMCPApprove, Approve: &req})
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

// AttachFile asks the daemon to file the host file at req.Path under the
// conversation bound to req.SessionID, and returns the minted attachment id.
//
// Built on request, NOT requestPatient: this is a bounded round trip — a
// confinement check, a file read and a write — not a wait on a human, which is
// the one thing requestPatient exists for. It is therefore bounded by the
// ctx's deadline or DialTimeout when it carries none, like every other verb
// here; a caller filing a large file should pass a ctx whose deadline suits
// it rather than reaching for the patient helper, whose contract (a wait ended
// only by cancellation, disconnect or an answer) is wrong for this verb.
//
// Shipped beside the verb rather than left to its consumers: #2165's
// subcommand and #2166's e2e both dial this, and without it each would write
// its own framing of the same exchange.
//
// Any error — dial/transport/decode, a server-side Response.Error, or an empty
// result — is returned verbatim. No typed-sentinel mapping is warranted: the
// server's refusals are deliberately static prose for claude to act on, not
// tokens for a caller to branch on, which is why no ErrorCode accompanies
// them.
//
// The returned id is safe to log. Nothing in req is: Path is a host path whose
// leaf is a filename docs/protocol-mobile.md § Attachments bans logging.
func AttachFile(ctx context.Context, socketPath string, req AttachFilePayload) (*AttachFileResult, error) {
	resp, err := request(ctx, socketPath, Request{Verb: VerbAttachFile, AttachFile: &req})
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	if resp.AttachFile == nil {
		return nil, errors.New("control: empty attachment.file response")
	}
	return resp.AttachFile, nil
}

// request sends one Request and reads one Response over a fresh connection,
// bounded by the ctx's deadline or DialTimeout when it carries none. Used by
// every client verb except Approve — all of them sub-second round-trips, whose
// bound must not move when the approve path's does.
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

	return exchange(conn, req)
}

// requestPatient sends one Request and blocks for the Response with NO conn
// read deadline at all. Bounded by liveness rather than duration: the dial's own
// budget (dialRetryBudget, installed by dialWithRetry even for an undeadlined
// ctx), cancellation of ctx, and the conn ending. Approve is its only caller —
// see that helper's doc comment for why an approval must not carry a client-side
// ceiling.
//
// Cancellation reaches a parked read by poking the conn's deadline into the
// past, which expires both parked and future I/O immediately; an already-cancelled
// ctx makes the AfterFunc fire before the first write, which fails closed. The
// watcher deliberately does not Close the conn — that would race the deferred
// Close below into a double close on a conn still being read. SetDeadline and
// Close are safe to call concurrently, and the error from a poke that lands on
// an already-closing conn is discarded because there is nothing left to bound.
//
// No write deadline: the request is one small JSON object into a socket buffer,
// and a blocked write means a peer that has stopped reading — the wedged-daemon
// case this path knowingly does not bound, which the watcher wakes anyway.
func requestPatient(ctx context.Context, socketPath string, req Request) (*Response, error) {
	conn, err := dial(ctx, socketPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	return exchange(conn, req)
}

// exchange encodes req and decodes one Response on an already-dialled conn. The
// two request helpers differ only in the deadline policy they install before
// calling this, so that difference is the only thing that reads as different.
func exchange(conn net.Conn, req Request) (*Response, error) {
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return &resp, nil
}
