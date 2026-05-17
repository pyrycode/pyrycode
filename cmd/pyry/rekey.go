package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
)

// rekeyArgs is the parsed shape of `pyry rekey <conn_id>`'s flag set.
type rekeyArgs struct {
	connID string // sole positional
}

// parseRekeyArgs parses the flag set for `pyry rekey`. No flags are
// accepted on this FlagSet — the shared client flags (-pyry-name,
// -pyry-socket) are peeled off upstream by parseClientFlags. Exactly one
// positional (the v2 conn id) is required. Zero, two, or more
// positionals — or any unknown flag — is an error propagated to the
// caller; runRekey maps these to exit 2.
//
// Mirrors parsePairRevokeArgs verbatim.
func parseRekeyArgs(args []string) (rekeyArgs, error) {
	fs := flag.NewFlagSet("pyry rekey", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return rekeyArgs{}, err
	}
	switch fs.NArg() {
	case 0:
		return rekeyArgs{}, errors.New("missing conn_id")
	case 1:
		return rekeyArgs{connID: fs.Arg(0)}, nil
	default:
		return rekeyArgs{}, fmt.Errorf("unexpected positional %q", fs.Arg(1))
	}
}

// rekeyVerdict returns (exitCode, stderrLine) for a control.Rekey result.
// exitCode == 0 means success; stderrLine is "". exitCode == 1 means
// failure; stderrLine is the one-line operator-readable message to print
// to stderr before os.Exit(1). Pure: deterministic on (connID, err).
//
// Mirrors preflightVerdict in cmd/pyry/pair.go — the same "extract the
// formatter so the unit test never has to intercept os.Exit" idiom.
//
// The conn-id is rendered with %q so operator-supplied strings containing
// newlines, ANSI escapes, or other terminal-control bytes are Go-quoted
// rather than written raw to stderr.
func rekeyVerdict(connID string, err error) (exitCode int, stderrLine string) {
	if err == nil {
		return 0, ""
	}
	if errors.Is(err, control.ErrConnNotFound) {
		return 1, fmt.Sprintf("pyry rekey: conn_id %q not found", connID)
	}
	return 1, fmt.Sprintf("pyry rekey: %s", err.Error())
}

// runRekey implements `pyry rekey <conn_id>`: dial the control socket and
// trigger an immediate Noise re-key on the named v2 conn (the "manual"
// rekey path in docs/protocol-mobile.md § Re-key).
//
// Returns nil on success (exit 0). Calls os.Exit(2) directly for usage
// failures (flag parse, missing or extra positional) — bypasses main's
// `pyry: ` prefix. Calls os.Exit(1) directly for typed and server-side
// rejects (unknown conn-id, no rekeyer configured, missing connID) —
// bypasses main's `pyry: ` prefix so the operator sees a clean
// `pyry rekey: <message>` line rather than `pyry: rekey: <message>`.
// Returns a wrapped `fmt.Errorf("rekey: %w", err)` for transport errors
// (dial, encode, decode) — main.run prefixes with `pyry: ` to give the
// full `pyry: rekey: …` chain. Same shape runSessionsRm uses for
// transport-shaped errors.
func runRekey(args []string) error {
	socketPath, rest, err := parseClientFlags("pyry rekey", args)
	if err != nil {
		return err
	}

	parsed, err := parseRekeyArgs(rest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pyry rekey:", err)
		fmt.Fprintln(os.Stderr, "usage: pyry rekey [-pyry-name=<instance>] [-pyry-socket=<path>] <conn_id>")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err = control.Rekey(ctx, socketPath, parsed.connID)
	if err == nil {
		return nil
	}

	// Transport errors (dial / encode / decode) flow through the wrapped-
	// return path. The typed not-found and server-side rejects flow
	// through rekeyVerdict + os.Exit(1) so the operator sees the verb's
	// own prefix without main's outer `pyry: `.
	if !errors.Is(err, control.ErrConnNotFound) && !isServerReject(err) {
		return fmt.Errorf("rekey: %w", err)
	}

	exitCode, stderrLine := rekeyVerdict(parsed.connID, err)
	fmt.Fprintln(os.Stderr, stderrLine)
	os.Exit(exitCode)
	return nil // unreachable
}

// isServerReject reports whether err is a server-side reject of a known
// shape — the slice A guard ("rekey: no rekeyer configured"), the slice
// A missing-connID guard ("rekey: missing connID"), or slice B1's
// session-not-open variant. Used by runRekey to decide whether to route
// through rekeyVerdict + os.Exit(1) (clean `pyry rekey:` prefix) or
// through the wrapped-return path (transport errors).
//
// The detection is by message-prefix because control.Rekey reconstructs
// these errors as plain errors.New(resp.Error) — slice A intentionally
// reserves the typed-sentinel slot for ErrConnNotFound only. If slice B
// later introduces typed sentinels for these guards, this helper
// collapses to a couple of errors.Is calls.
func isServerReject(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	switch msg {
	case "rekey: no rekeyer configured", "rekey: missing connID":
		return true
	}
	return false
}
