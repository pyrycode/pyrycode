package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/relay/handlers"
)

// The three refusal reasons channelCreator can return. They are CONSTANTS, and
// that is the security-load-bearing part of this file rather than a style
// choice: control.Server.SetChannelCreator forwards the returned error's text
// to the wire verbatim, and the validator below it — resolveSpawnDir wrapping
// confineWorkdirToHomeCreating — produces messages that embed BOTH the resolved
// path and the operator's $HOME. Forwarding one unchanged would breach the
// ticket's "echoes neither the requested nor the resolved path". The wrapped
// detail is logged daemon-side, which is where the operator can already read
// their own paths, and never returned.
//
// They mirror msgCreateConversationCwdRejected / msgCreateConversationMintFailed
// in internal/relay/handlers, which solved the same problem for the wire verb.
const (
	// msgChannelCwdRejected covers a directory that escapes $HOME after symlink
	// resolution, or is unresolvable, or is empty. Deterministic: re-running in
	// the same directory fails identically.
	msgChannelCwdRejected = "working directory not allowed"

	// msgChannelWorkspaceFailed covers a transient failure to record the
	// workspace as trusted in ~/.claude.json. Distinct from the rejection above
	// because it is not a verdict on the directory — retrying can succeed — and
	// telling an operator their own project folder is "not allowed" when the
	// real fault is a write error would send them looking in the wrong place.
	msgChannelWorkspaceFailed = "could not prepare the channel workspace"

	// msgChannelMintFailed covers id generation or session-mint failure (the
	// pool not running, or its registry save failing).
	msgChannelMintFailed = "could not start channel session"
)

// channelCreator builds the dependency control.Server.SetChannelCreator
// installs: given the directory the calling CLI is standing in and an optional
// name, create a promoted conversation — a channel — rooted there, and return
// its id.
//
// The sequence mirrors handlers.CreateConversation step for step (validate,
// mint, record, eagerly persist) because the ticket's contract is that the row
// this leaves is indistinguishable from a client-created one. There is exactly
// one deliberate difference: the stored Cwd is the RESOLVED real path, where
// the wire handler stores the raw string its client sent. Everything else —
// the confinement, the eager save, the bound session, the absence of any
// uniqueness rule — is the same.
//
// mint narrows *sessions.Pool's Mint to the two values this needs, so the
// creator unit-tests without standing up a pool. It must NOT re-validate:
// spawnDir arrives already confined and trust-marked.
//
// SECURITY: cwd is caller-authored and arrives unvalidated — internal/control
// deliberately does no path handling — so confinement is owned here in full.
// It is owned by DELEGATION: resolveSpawnDir is called whole rather than
// inlined or re-sequenced, because its confine-then-trust order is what keeps a
// path outside $HOME from being auto-trusted (trustMark carries no $HOME bound
// of its own). Nothing in this function may take that call apart.
//
// The empty-cwd guard is the second half of a two-sided pair whose first half
// is handleChannelNew's. It is not redundant: resolveSpawnDir reads the empty
// string as "spawn in the shared trusted workdir" and returns success WITHOUT
// confining or trust-marking anything, so an empty cwd reaching it would mint a
// channel rooted nowhere the caller named. The guard closes that seam against
// any future caller that does not come through the handler — the same reasoning
// fileAttacher's empty-sessionID guard records, where the seam resolved to the
// bootstrap session instead.
func channelCreator(
	reg *conversations.Registry,
	mint func(label, spawnDir string) (string, error),
	registryPath string,
	log *slog.Logger,
) func(cwd, name string) (string, error) {
	return func(cwd, name string) (string, error) {
		if cwd == "" {
			log.Warn("control: channel.new rejected an empty cwd",
				"event", "channel_new.cwd_rejected")
			return "", errors.New(msgChannelCwdRejected)
		}

		// Confine to $HOME, resolve symlinks, then trust-mark the realpath —
		// in that order, and as one call. The returned path is what claude
		// will chdir into, so it is also what the row records and what the
		// name is derived from.
		resolved, err := resolveSpawnDir(cwd)
		if err != nil {
			// The wrapped detail names the resolved path and $HOME; it goes to
			// the daemon's log and no further. errors.Is rather than a string
			// match so the classification survives future wrapping.
			log.Warn("control: channel.new spawn dir rejected",
				"event", "channel_new.spawn_dir_rejected",
				"err", err)
			if errors.Is(err, handlers.ErrSpawnDirRejected) {
				return "", errors.New(msgChannelCwdRejected)
			}
			return "", errors.New(msgChannelWorkspaceFailed)
		}

		id, err := conversations.NewID()
		if err != nil {
			log.Error("control: channel.new id generation failed",
				"event", "channel_new.id_failed", "err", err)
			return "", errors.New(msgChannelMintFailed)
		}

		// Mint BEFORE recording the row, so a crash between the two leaves an
		// orphan session rather than a row pointing at a session that never
		// existed. That ordering is what makes "no half-bound row" true, and it
		// is CreateConversation's for the same reason. The label is the
		// conversation id — a session↔conversation breadcrumb in the session
		// registry; it never reaches claude's argv.
		sessionID, err := mint(string(id), resolved)
		if err != nil {
			log.Warn("control: channel.new session mint failed",
				"event", "channel_new.session_mint_failed",
				"conversation_id", string(id), "err", err)
			return "", errors.New(msgChannelMintFailed)
		}

		// Default the name from the RESOLVED path, so a symlinked entry
		// directory names the real folder. An explicitly empty --name takes
		// this default too: nothing asks for a channel whose name is
		// deliberately blank, and `pyry sessions new` reads an empty --name the
		// same way. Conversation.Name's "explicitly empty" state stays
		// reachable only where it already was.
		if name == "" {
			name = filepath.Base(resolved)
		}

		now := time.Now().UTC()
		reg.Create(conversations.Conversation{
			ID:               id,
			Name:             &name,
			Cwd:              resolved,
			CurrentSessionID: sessionID,
			IsPromoted:       true,
			LastUsedAt:       now,
		})

		// Eager best-effort persist so the channel survives a daemon restart:
		// the sweep loop saves lazily, so without this the row would be absent
		// on the next start. A Save failure is non-fatal and deliberately not
		// returned — the row is live in memory and immediately usable, and
		// durability is best-effort exactly as CreateConversation treats its
		// own. Returning an error here would instead report a failure for a
		// channel that does exist.
		if err := reg.Save(registryPath); err != nil {
			log.Error("control: channel.new persist failed",
				"event", "channel_new.persist_failed",
				"conversation_id", string(id), "err", err)
		}

		log.Info("control: channel.new created",
			"event", "channel_new.created",
			"conversation_id", string(id),
			"session_id", sessionID)
		return string(id), nil
	}
}

// channelUsage is the one-line usage banner both parse-failure paths print
// before exiting 2.
const channelUsage = "usage: pyry channel new [-pyry-name=<instance>] [-pyry-socket=<path>] [--name <label>]"

// parseChannelNewArgs is the flag-parse + arity check for
// `pyry channel new [--name LABEL]`. Extracted from runChannelNew so the
// parsing rules unit-test without dialling the control socket. Mirrors
// parseSessionsNewArgs, whose --name flag this one matches by name and meaning.
//
// The FlagSet discards its own output (io.Discard) so the caller owns every
// byte on stderr — runChannelNew prints the error and the usage banner itself,
// which is what keeps the exit-2 path to exactly two lines.
func parseChannelNewArgs(args []string) (name string, err error) {
	fs := flag.NewFlagSet("pyry channel new", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	nameFlag := fs.String("name", "", "display name for the new channel (default: the directory's base name)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("unexpected positional %q", fs.Arg(0))
	}
	return *nameFlag, nil
}

// channelNewVerdict returns (exitCode, stderrLine) for a `pyry channel new`
// result. exitCode == 0 means success and stderrLine is ""; exitCode == 1 means
// failure and stderrLine is the one-line operator-readable message to print
// before os.Exit(1). Pure: deterministic on err, so the unit test never has to
// intercept os.Exit. Mirrors rekeyVerdict, the same idiom.
//
// Unlike rekeyVerdict it takes no second argument to quote, and that is the
// point: every message it can format either originates in the daemon — where
// SetChannelCreator's contract has already made it static — or is a local
// syscall error. Nothing caller-supplied passes through, so there is nothing
// here to escape.
//
// It also does NOT sort server refusals from transport failures the way
// runRekey's isServerReject does. That split buys a cosmetic prefix
// (`pyry channel new:` versus main's `pyry: channel new:`) at the cost of a
// hand-maintained list of message prefixes that goes stale silently the first
// time a message is reworded. Both classes are one stderr line and exit 1, so
// this verb routes every failure through here and keeps the prefix uniform.
func channelNewVerdict(err error) (exitCode int, stderrLine string) {
	if err == nil {
		return 0, ""
	}
	return 1, fmt.Sprintf("pyry channel new: %s", err.Error())
}

// runChannel implements `pyry channel <verb>`: peel the global pyry flags via
// parseClientFlags, then dispatch on the first positional. `new` is the only
// verb; a missing or unknown one is a usage error.
//
// Convention matches `pyry sessions` — -pyry-socket / -pyry-name precede the
// sub-verb, sub-verb flags follow it — but the exit code does not: usage
// failures here exit 2 (the rekey idiom the ticket asks for) rather than
// returning an error for main to map to 1.
func runChannel(args []string) error {
	socketPath, rest, err := parseClientFlags("pyry channel", args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return channelUsageExit("missing subcommand")
	}
	sub, subArgs := rest[0], rest[1:]
	switch sub {
	case "new":
		return runChannelNew(socketPath, subArgs)
	default:
		return channelUsageExit(fmt.Sprintf("unknown verb %q", sub))
	}
}

// channelUsageExit prints detail plus the usage banner to stderr and exits 2.
// It returns error only so the callers above can `return` it and keep the
// switch's shape uniform; it never actually returns.
func channelUsageExit(detail string) error {
	fmt.Fprintln(os.Stderr, "pyry channel:", detail)
	fmt.Fprintln(os.Stderr, channelUsage)
	os.Exit(2)
	return nil // unreachable
}

// runChannelNew implements `pyry channel new [--name LABEL]`: send the
// directory this process is standing in to the daemon, which canonicalises,
// confines and trust-marks it, and print the created conversation's id.
//
// The cwd is sent RAW. This side deliberately does not clean, absolutise or
// resolve it — the daemon owns that in one place, and a client that
// pre-resolved would just be a second, divergent implementation of the same
// rule.
//
// Exit codes: 0 with the id on stdout; 2 for usage failures (flag parse, stray
// positional, missing or unknown sub-verb), printed without main's `pyry: `
// prefix; 1 for everything else — a failed os.Getwd, a daemon refusal, or a
// transport failure — as a single `pyry channel new: …` line on stderr.
func runChannelNew(socketPath string, args []string) error {
	name, err := parseChannelNewArgs(args)
	if err != nil {
		return channelUsageExit(err.Error())
	}

	// A Getwd failure is the "the shell's directory was deleted underneath it"
	// case. It is refused here, before dialling: the daemon has no way to learn
	// where this process was standing, and the confining validator it would
	// otherwise reach CREATES a missing directory rather than refusing one, so
	// there is no daemon-side check this could fall through to.
	cwd, err := os.Getwd()
	if err != nil {
		return channelExit(fmt.Errorf("resolve current directory: %w", err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	id, err := control.ChannelNew(ctx, socketPath, cwd, name)
	if err != nil {
		return channelExit(err)
	}
	fmt.Println(id)
	return nil
}

// channelExit prints channelNewVerdict's line and exits with its code. Split
// from runChannelNew so the formatting stays in the pure verdict function and
// only this three-line wrapper touches os.Exit.
func channelExit(err error) error {
	exitCode, stderrLine := channelNewVerdict(err)
	fmt.Fprintln(os.Stderr, stderrLine)
	os.Exit(exitCode)
	return nil // unreachable
}
