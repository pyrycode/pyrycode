package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// parseClientFlags handles the shared flags every control verb accepts:
// -pyry-name (instance name → ~/.pyry/<name>.sock) and -pyry-socket (explicit
// path that overrides the name). Returns the resolved socket path and any
// positionals after the recognised flags. Verbs that don't take positionals
// can bind rest to _ — same silent-ignore behaviour as before.
func parseClientFlags(name string, args []string) (socketPath string, rest []string, err error) {
	pyryArgs, rest := splitClientFlags(args)
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	nameFlag := fs.String("pyry-name", defaultName(), "instance name (socket: ~/.pyry/<name>.sock)")
	socketFlag := fs.String("pyry-socket", "", "explicit socket path (overrides -pyry-name)")
	if err := fs.Parse(pyryArgs); err != nil {
		return "", nil, err
	}
	return resolveSocketPath(*socketFlag, *nameFlag), rest, nil
}

// runStatus implements the `pyry status` subcommand: dial the control socket,
// fetch a status snapshot, pretty-print it.
func runStatus(args []string) error {
	socketPath, rest, err := parseClientFlags("pyry status", args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("status: unexpected arguments: %s", strings.Join(rest, " "))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := control.Status(ctx, socketPath)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}

	fmt.Printf("Phase:         %s\n", resp.Phase)
	if resp.ChildPID > 0 {
		fmt.Printf("Child PID:     %d\n", resp.ChildPID)
	}
	fmt.Printf("Restart count: %d\n", resp.RestartCount)
	if resp.LastUptime != "" {
		fmt.Printf("Last uptime:   %s\n", resp.LastUptime)
	}
	if resp.NextBackoff != "" {
		fmt.Printf("Next backoff:  %s\n", resp.NextBackoff)
	}
	fmt.Printf("Started at:    %s\n", resp.StartedAt)
	fmt.Printf("Uptime:        %s\n", resp.Uptime)
	return nil
}

// runLogs implements `pyry logs`: fetch the recent supervisor log lines from
// the daemon's in-memory ring buffer and print them.
func runLogs(args []string) error {
	socketPath, rest, err := parseClientFlags("pyry logs", args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("logs: unexpected arguments: %s", strings.Join(rest, " "))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := control.Logs(ctx, socketPath)
	if err != nil {
		return fmt.Errorf("logs: %w", err)
	}
	for _, line := range resp.Lines {
		fmt.Println(line)
	}
	return nil
}

// sessionsVerbList is the displayed verb list in `pyry sessions` usage
// errors. Update in lockstep with the switch in runSessions — Phase
// 1.1b/c/d/e each append one verb here in the same edit that adds the
// case.
const sessionsVerbList = "new, rm, rename, list"

// errSessionsUsage formats a help-style error listing the implemented
// `pyry sessions` verbs. Mapped to a non-zero exit by main's top-level
// error printer.
func errSessionsUsage(detail string) error {
	return fmt.Errorf("sessions: %s\nverbs: %s", detail, sessionsVerbList)
}

// runSessions implements `pyry sessions <verb>`: peel the global pyry
// flags via parseClientFlags, then dispatch on the first positional.
//
// Convention (matches the top-level CLI: "pyry flags must come before
// claude args"): -pyry-socket / -pyry-name must precede the sub-verb.
// Sub-verb flags (e.g. --name on `new`) come after.
//
// New verbs in this family (1.1b list, 1.1c rename, 1.1d rm, 1.1e
// attach refactor) each add one switch case + one runSessions<Verb>
// helper.
func runSessions(args []string) error {
	socketPath, rest, err := parseClientFlags("pyry sessions", args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errSessionsUsage("missing subcommand")
	}
	sub, subArgs := rest[0], rest[1:]
	switch sub {
	case "new":
		return runSessionsNew(socketPath, subArgs)
	case "rm":
		return runSessionsRm(socketPath, subArgs)
	case "rename":
		return runSessionsRename(socketPath, subArgs)
	case "list":
		return runSessionsList(socketPath, subArgs)
	default:
		return errSessionsUsage(fmt.Sprintf("unknown verb %q", sub))
	}
}

// parseSessionsNewArgs is the flag-parse + arity check for
// `pyry sessions new [--name LABEL]`. Extracted from runSessionsNew so
// the parsing rules can be unit-tested without dialling the control
// socket. Mirrors attachSelectorFromArgs's split.
func parseSessionsNewArgs(args []string) (label string, err error) {
	fs := flag.NewFlagSet("pyry sessions new", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	labelFlag := fs.String("name", "", "human-friendly label for the new session")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("unexpected positional %q", fs.Arg(0))
	}
	return *labelFlag, nil
}

// runSessionsNew implements `pyry sessions new [--name LABEL]`: dial
// the daemon's control socket, ask it to mint a session, print the
// UUID. Empty label maps to a no-label session per AC#1.
func runSessionsNew(socketPath string, args []string) error {
	label, err := parseSessionsNewArgs(args)
	if err != nil {
		return fmt.Errorf("sessions new: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	id, err := control.SessionsNew(ctx, socketPath, label)
	if err != nil {
		return fmt.Errorf("sessions new: %w", err)
	}
	fmt.Println(id)
	return nil
}

// errSessionsRmUsage marks every parse-time failure of `pyry sessions rm`
// as a usage error. runSessionsRm matches via errors.Is and exits 2 with
// the wrapped message printed verbatim (no `pyry:` prefix). One sentinel
// covers arity, mutually-exclusive flags, and any other handler-side
// usage rule — the wire-call path is reached only on parse-success, so
// runSessionsRm doesn't need to discriminate further.
var errSessionsRmUsage = errors.New("usage")

// errAmbiguousPrefix carries the formatted multi-line "ambiguous prefix"
// message produced by resolveSessionIDViaList. The unexported sentinel
// exists so runSessionsRm can branch with errors.Is rather than
// string-matching the message text. Mirrors sessions.ErrAmbiguousSessionID
// in spirit — Pool.ResolveID's server-side equivalent — but lives at the
// CLI layer because prefix resolution here is client-side via
// control.SessionsList.
var errAmbiguousPrefix = errors.New("ambiguous session id prefix")

// parseSessionsRmArgs parses `[--archive|--purge] <id>`. Returns
// (id, policy, err); policy is the wire enum (control.JSONLPolicy) —
// empty when neither --archive nor --purge was set, which the server
// treats as JSONLPolicyLeave.
//
// Mirrors parseSessionsNewArgs's shape: extracted from runSessionsRm
// so flag-parsing rules are unit-testable without dialling the
// control socket. Every error returned wraps errSessionsRmUsage so
// runSessionsRm can map the whole class to exit 2 with a single
// errors.Is check.
func parseSessionsRmArgs(args []string) (id string, policy control.JSONLPolicy, err error) {
	fs := flag.NewFlagSet("pyry sessions rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	archive := fs.Bool("archive", false, "archive the on-disk JSONL transcript")
	purge := fs.Bool("purge", false, "delete the on-disk JSONL transcript (default: leave)")
	if err := fs.Parse(args); err != nil {
		return "", "", fmt.Errorf("%w: %v", errSessionsRmUsage, err)
	}
	if *archive && *purge {
		return "", "", fmt.Errorf("%w: --archive and --purge are mutually exclusive", errSessionsRmUsage)
	}
	if fs.NArg() != 1 {
		return "", "", fmt.Errorf("%w: expected <id>, got %d positional args", errSessionsRmUsage, fs.NArg())
	}
	switch {
	case *archive:
		policy = control.JSONLPolicyArchive
	case *purge:
		policy = control.JSONLPolicyPurge
	default:
		// Empty policy — wire layer normalises to JSONLPolicyLeave.
		policy = ""
	}
	return fs.Arg(0), policy, nil
}

// resolveSessionIDViaList resolves a user-supplied UUID-or-prefix to a
// canonical SessionID by listing every session via the wire and
// filtering client-side. Mirrors Pool.ResolveID's resolution order:
// exact match wins outright; otherwise scan with strings.HasPrefix —
// one match returns its ID; zero returns sessions.ErrSessionNotFound;
// multiple returns errAmbiguousPrefix wrapping a sorted "<uuid> <label>"
// list (one per line, matching AC#3's space-separated form).
//
// Empty arg is rejected at parse time; callers may assume arg != "".
func resolveSessionIDViaList(ctx context.Context, socketPath, arg string) (string, error) {
	list, err := control.SessionsList(ctx, socketPath)
	if err != nil {
		return "", err
	}
	for _, s := range list {
		if s.ID == arg {
			return s.ID, nil
		}
	}
	var matches []control.SessionInfo
	for _, s := range list {
		if strings.HasPrefix(s.ID, arg) {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return "", sessions.ErrSessionNotFound
	case 1:
		return matches[0].ID, nil
	default:
		sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
		var b strings.Builder
		for i, m := range matches {
			label := m.Label
			if m.Bootstrap && label == "" {
				label = "bootstrap"
			}
			if i > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "%s %s", m.ID, label)
		}
		return "", fmt.Errorf("%w:\n%s", errAmbiguousPrefix, b.String())
	}
}

// runSessionsRm implements `pyry sessions rm [--archive|--purge] <id>`:
// resolve the (possibly-prefix) <id> via sessions.list, dial the
// daemon's control socket, ask it to terminate the named session,
// remove its registry entry, and apply the JSONL disposition policy.
//
// Exit codes match the rest of cmd/pyry:
//
//	0 — removal succeeded.
//	1 — runtime error (ambiguous prefix, unknown id, bootstrap
//	    rejection, server-side error, or no-daemon dial failure).
//	2 — usage error (parse failure, mutually-exclusive flags, or
//	    wrong arity).
//
// The three AC-prescribed messages (ambiguous, unknown, bootstrap) are
// printed to stderr without the `pyry:` outer-error prefix; other
// errors flow through `fmt.Errorf("sessions rm: %w", err)`, which
// main's top-level error printer prepends with `pyry: `.
func runSessionsRm(socketPath string, args []string) error {
	id, policy, err := parseSessionsRmArgs(args)
	if err != nil {
		if errors.Is(err, errSessionsRmUsage) {
			fmt.Fprintln(os.Stderr, "pyry sessions rm:", err)
		}
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	canonical, err := resolveSessionIDViaList(ctx, socketPath, id)
	if err != nil {
		switch {
		case errors.Is(err, errAmbiguousPrefix):
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		case errors.Is(err, sessions.ErrSessionNotFound):
			fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
			os.Exit(1)
		}
		return fmt.Errorf("sessions rm: %w", err)
	}

	if err := control.SessionsRm(ctx, socketPath, canonical, policy); err != nil {
		switch {
		case errors.Is(err, sessions.ErrCannotRemoveBootstrap):
			fmt.Fprintln(os.Stderr, "cannot remove bootstrap session")
			os.Exit(1)
		case errors.Is(err, sessions.ErrSessionNotFound):
			// Race window: list returned the canonical UUID, then
			// another caller removed it before our SessionsRm landed.
			// Surface the original (typed) <id> — that's the string
			// the operator typed.
			fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
			os.Exit(1)
		}
		return fmt.Errorf("sessions rm: %w", err)
	}
	return nil
}

// errSessionsRenameUsage marks every parse-time failure of
// `pyry sessions rename` as a usage error. runSessionsRename matches via
// errors.Is and exits 2 with the wrapped message printed verbatim (no
// `pyry:` prefix). One sentinel covers arity and any future handler-side
// usage rule. Mirrors errSessionsRmUsage's shape.
var errSessionsRenameUsage = errors.New("usage")

// parseSessionsRenameArgs parses `<id> <new-label>`. Returns
// (id, newLabel, err). Both positionals are required; the empty string is
// a valid value for <new-label> (Pool.Rename treats it as "clear the
// on-disk label" per #62), so the arity check counts positionals (must
// be exactly 2) rather than testing for non-empty strings.
//
// No flags today — the FlagSet exists for symmetry with `new` and `rm`
// and so a future flag slots in mechanically.
func parseSessionsRenameArgs(args []string) (id, newLabel string, err error) {
	fs := flag.NewFlagSet("pyry sessions rename", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return "", "", fmt.Errorf("%w: %v", errSessionsRenameUsage, err)
	}
	if fs.NArg() != 2 {
		return "", "", fmt.Errorf("%w: expected <id> <new-label>, got %d positional args", errSessionsRenameUsage, fs.NArg())
	}
	return fs.Arg(0), fs.Arg(1), nil
}

// runSessionsRename implements `pyry sessions rename <id> <new-label>`:
// resolve the (possibly-prefix) <id> via sessions.list, dial the daemon's
// control socket, ask it to update the named session's human-friendly
// label.
//
// Exit codes match the rest of cmd/pyry:
//
//	0 — rename succeeded.
//	1 — runtime error (ambiguous prefix, unknown id, server-side
//	    error, or no-daemon dial failure).
//	2 — usage error (parse failure or wrong arity).
//
// The AC-prescribed messages (ambiguous, unknown) are printed to stderr
// without the `pyry:` outer-error prefix; other errors flow through
// `fmt.Errorf("sessions rename: %w", err)`, which main's top-level error
// printer prepends with `pyry: `.
func runSessionsRename(socketPath string, args []string) error {
	id, newLabel, err := parseSessionsRenameArgs(args)
	if err != nil {
		if errors.Is(err, errSessionsRenameUsage) {
			fmt.Fprintln(os.Stderr, "pyry sessions rename:", err)
		}
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	canonical, err := resolveSessionIDViaList(ctx, socketPath, id)
	if err != nil {
		switch {
		case errors.Is(err, errAmbiguousPrefix):
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		case errors.Is(err, sessions.ErrSessionNotFound):
			fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
			os.Exit(1)
		}
		return fmt.Errorf("sessions rename: %w", err)
	}

	if err := control.SessionsRename(ctx, socketPath, canonical, newLabel); err != nil {
		if errors.Is(err, sessions.ErrSessionNotFound) {
			// Race window: resolver returned the canonical UUID, then
			// another caller removed it before our wire call landed.
			// Surface the operator's original <id> — the string they typed.
			fmt.Fprintf(os.Stderr, "no session with id %q\n", id)
			os.Exit(1)
		}
		return fmt.Errorf("sessions rename: %w", err)
	}
	return nil
}

// parseSessionsListArgs parses `[--json]`. Returns (jsonOut, err). No
// positional arguments accepted — `pyry sessions list` lists every session
// in one shot. Mirrors parseSessionsNewArgs's shape: extracted so flag
// rules are unit-testable without dialling the control socket. Errors are
// returned verbatim (no usage sentinel) — runSessionsList wraps via
// fmt.Errorf("sessions list: %w", err) and exits 1, matching
// runSessionsNew's exit-1-on-parse-error precedent.
func parseSessionsListArgs(args []string) (jsonOut bool, err error) {
	fs := flag.NewFlagSet("pyry sessions list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonFlag := fs.Bool("json", false, "emit JSON instead of a human table")
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	if fs.NArg() > 0 {
		return false, fmt.Errorf("unexpected positional %q", fs.Arg(0))
	}
	return *jsonFlag, nil
}

// sortSessionsForDisplay applies the renderer's deterministic order in
// place: LastActive descending (most recent first), ID ascending as the
// tiebreak. Pool.List already returns this order today, but the AC says
// the renderer enforces it — defence against future wire changes that
// would otherwise reshuffle every operator's table. time.Time.Equal (not
// ==) handles JSON-roundtripped values that have lost their monotonic
// component (see lessons.md § "JSON roundtrip strips monotonic-clock
// state").
func sortSessionsForDisplay(list []control.SessionInfo) {
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].LastActive.Equal(list[j].LastActive) {
			return list[i].LastActive.After(list[j].LastActive)
		}
		return list[i].ID < list[j].ID
	})
}

// writeSessionsTable renders the snapshot as a tabwriter-aligned table to
// w. Columns: UUID, LABEL, STATE, LAST-ACTIVE. UUIDs render in their full
// 36-character canonical form (no truncation — operators copy/paste them).
// LAST-ACTIVE is rendered as RFC3339; jq consumers wanting nanos use
// --json. Empty Label renders as the empty cell — the wire substitutes
// the bootstrap entry's empty on-disk label with "bootstrap" before
// returning, so this layer renders verbatim.
func writeSessionsTable(w io.Writer, list []control.SessionInfo) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "UUID\tLABEL\tSTATE\tLAST-ACTIVE"); err != nil {
		return err
	}
	for _, s := range list {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			s.ID, s.Label, s.State, s.LastActive.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// writeSessionsJSON encodes the snapshot as a single JSON object with a
// top-level "sessions" array. Envelope is intentionally NOT a bare array —
// leaves room for future top-level fields (e.g. "generated_at") without a
// breaking change. Per-element shape is whatever encoding/json produces
// from control.SessionInfo (id, label, state, last_active, optional
// bootstrap). Encoder.Encode appends a single \n — what jq pipelines
// expect.
func writeSessionsJSON(w io.Writer, list []control.SessionInfo) error {
	payload := struct {
		Sessions []control.SessionInfo `json:"sessions"`
	}{Sessions: list}
	enc := json.NewEncoder(w)
	return enc.Encode(payload)
}

// runSessionsList implements `pyry sessions list [--json]`: dial the
// daemon's control socket, fetch the session snapshot, render it as
// either a human-readable table or a single JSON object. Empty pool
// (would only ever contain bootstrap) renders a one-row table or a
// one-element sessions array.
func runSessionsList(socketPath string, args []string) error {
	jsonOut, err := parseSessionsListArgs(args)
	if err != nil {
		return fmt.Errorf("sessions list: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	list, err := control.SessionsList(ctx, socketPath)
	if err != nil {
		return fmt.Errorf("sessions list: %w", err)
	}

	sortSessionsForDisplay(list)

	if jsonOut {
		if err := writeSessionsJSON(os.Stdout, list); err != nil {
			return fmt.Errorf("sessions list: %w", err)
		}
		return nil
	}
	if err := writeSessionsTable(os.Stdout, list); err != nil {
		return fmt.Errorf("sessions list: %w", err)
	}
	return nil
}

// runStop implements `pyry stop`: dial the control socket and ask the daemon
// to shut down. Returns when the server has acknowledged — the daemon may
// still be unwinding its child.
func runStop(args []string) error {
	socketPath, rest, err := parseClientFlags("pyry stop", args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("stop: unexpected arguments: %s", strings.Join(rest, " "))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := control.Stop(ctx, socketPath); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	fmt.Println("pyry: stop requested")
	return nil
}
