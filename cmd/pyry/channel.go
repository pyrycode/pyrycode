package main

import (
	"context"
	"encoding/json"
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
	"github.com/pyrycode/pyrycode/internal/protocol"
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

	// msgChannelPostRecordFailed covers a failure to write the message into the
	// conversation's durable log. Static for the reason above, and with an extra
	// one of its own: internal/history's errors format absolute filesystem paths
	// ("open segment %q", "resolve log directory %q"), so forwarding one would
	// put the daemon's layout on a wire the operator's own scripts read. The
	// content-free discriminant goes to the daemon's log instead, through the
	// historyAppendFailure the other history producers already share.
	//
	// Since #2498 it also covers a turn-id mint failure, which refuses BEFORE
	// anything is written. The message is accurate on that branch too — nothing
	// was recorded — and a second constant would split one operator-visible
	// outcome across two spellings for a distinction only the daemon's own log
	// can act on.
	msgChannelPostRecordFailed = "could not record the message"

	// fmtChannelPostAmbiguous is the one refusal in this file that is not a bare
	// constant, and the exception is bounded on purpose: the format is static and
	// the single interpolated value is an int counted off the daemon's own
	// registry. The requested label is deliberately absent. An operator who
	// mistyped a name learns that several channels answer to it, which is what
	// they can act on; echoing their input back would breach the same
	// no-verbatim-echo rule every message above exists to keep.
	fmtChannelPostAmbiguous = "%d channels share that name; rename all but one and retry"
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
//
// announce tells every connected client the channel now exists (#2156), so a
// conversation created from the host's shell reaches an open client without a
// reconnect. A bare func for the reason mint is one, and for the reason
// fileAttacher's own announce hook is: the value crossing out of the relay leg
// stays a closure over a wire payload, so this seam takes on no relay type.
//
// IT MAY BE NIL, and the nil is not defensive padding — it is fileAttacher's
// shape exactly. The hook is absent when the relay leg is: startRelay returns
// before any manager exists when no URL is configured, so that daemon has
// nobody to tell. A nil hook creates the channel and answers with its id.
func channelCreator(
	reg *conversations.Registry,
	mint func(label, spawnDir string) (string, error),
	registryPath string,
	announce func(protocol.ConversationUpdatedPayload),
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

		// Tell every connected client the channel exists (#2156). LAST, and only
		// on the success path: every refusal above has already returned, so a
		// refused create announces nothing. Its result is deliberately not
		// consulted and it returns no error of its own — a failed push must not
		// turn a created channel into a refusal, because the row is in the
		// registry and on disk whether or not anyone heard.
		//
		// That is also why a vanished read-back below is logged rather than
		// returned: SetChannelCreator forwards this function's error text to the
		// wire verbatim, which is what makes every refusal above a static
		// constant, and a new message here would breach that contract for a
		// channel that was in fact created.
		if announce != nil {
			// Read the row back rather than projecting the value constructed
			// above, the way promote_conversation feeds its own reply. The stored
			// Cwd is resolveSpawnDir's CONFINED, symlink-resolved output, so the
			// read-back is what keeps the caller's raw, unvalidated cwd off the
			// wire. Create→Get is two lock acquisitions, not atomic: a concurrent
			// delete between them yields a miss, which is the truthful current
			// state, so the announcement is skipped rather than faked.
			//
			// An if/else rather than this file's usual early return, because both
			// arms answer identically: a second `return string(id), nil` here would
			// read as though a vanished row changed what the verb replies, and it
			// does not.
			if got, ok := reg.Get(id); ok {
				// The workspace's label (#2210), keyed on the read-back row's own
				// confined cwd — the same value this record reports — so a client
				// patching its rows from the push renders the operator's chosen
				// name instead of the folder name. Presence comes from the
				// accessor's second return and never from a label != ""
				// comparison, which would collapse the registry's distinct
				// stored-empty and absent states. The three lines are inline
				// rather than shared: the relay handlers' workspaceLabelFor is
				// unexported in another package, and this is the only site here.
				//
				// Nothing below logs it. The value is operator-supplied text that
				// belongs on the wire and nowhere else.
				var workspaceLabel *string
				if label, ok := reg.WorkspaceLabel(got.Cwd); ok {
					workspaceLabel = &label
				}
				announce(protocol.ConversationUpdatedPayload{
					ID:             string(got.ID),
					IsPromoted:     got.IsPromoted,
					IsArchived:     got.IsArchived,
					IsMuted:        got.IsMuted,
					Name:           got.Name,
					Cwd:            got.Cwd,
					WorkspaceLabel: workspaceLabel,
					LastUsedAt:     got.LastUsedAt,
				})
			} else {
				log.Warn("control: channel.new vanished before announce read-back",
					"event", "channel_new.announce_row_vanished",
					"conversation_id", string(id))
			}
		}
		return string(id), nil
	}
}

// channelPoster builds the dependency control.Server.SetChannelPoster installs:
// given a channel's display label and a message body, record that content in the
// named conversation's durable log, creating the channel first when the label
// matches nothing.
//
// It takes create — channelCreator's own return value, wired from the same
// composition-root call — rather than minting a second create path. That is what
// keeps the confinement order, the eager persist, the bound session and the
// announcement in one named place, and it is why a mistyped label stays
// diagnosable: the row it creates carries channelCreator's existing
// channel_new.created log line, which is what lets the happy path print nothing.
//
// appendEntry is history.Store.Append narrowed to a func, and it is narrowed for
// the reason mint is: the poster unit-tests without an instance directory or a
// segment layout between the test and the one property under test, which is what
// bytes this hands the log.
//
// IT DOES NOT ROUTE THROUGH appendConversationHistory, and the divergence is the
// design rather than an oversight. That seam returns nothing by contract — a
// failed append must never suppress the caller's wire emit, which is right for
// its two stream producers, whose frame has already gone out by then. Here the
// order is the other way round: the durable record IS what this verb delivers and
// it is written FIRST, so an append that failed has to be a post that failed, and
// a post that failed pushes nothing. A cron exiting 0 having delivered nothing is
// the exact failure that would otherwise ship.
//
// announce carries the recorded content to every connected client (#2498), so a
// message a cron posts appears in an open app without a reconnect. THE SAME
// PAYLOAD VALUE goes to both halves — the log and the wire — which is what makes
// "one post is one rendering" a property rather than two derivations that agree
// today. A bare func for the reason channelCreator's own announce hook is one,
// and IT MAY BE NIL for that hook's reason exactly: startRelay returns before any
// manager exists when no URL is configured, so that daemon has nobody to tell. A
// nil hook records the post and answers success.
//
// The entry type is assistant_delta and NOT the message/role-assistant shape
// #2497 shipped. That shape reaches a client and draws nothing — desktop's
// translateTimelineEvent returns a row only for role "user" and null for
// "assistant", on the live path and the served-history path both — so the record
// was invisible on the very path it existed for. assistant_delta is what the
// current clients draw as assistant text, and writing the shape that is pushed is
// also what keeps a served page from carrying a second record of one post.
//
// SECURITY: name is caller-authored text arriving unvalidated past
// handleChannelPost's shape checks, and it is used for exactly one thing — an
// equality comparison against stored names. It never reaches a filesystem path
// (the log keys on the daemon-minted conversation id), never reaches an argv,
// and never appears in a refusal or a log line. text is conversation content and
// the durable log is the one place it may be written, so it is never logged
// either — appendConversationHistory's discipline, kept here by hand because
// this path does not share its body.
//
// The registry enforces NO uniqueness rule on names — channelCreator's doc block
// says so outright — which is why two or more matches need an answer rather than
// a silent pick. List and create are two lock acquisitions and not atomic, so
// concurrent posts naming one absent channel can each create a row; the outcome
// is a loud ambiguity refusal on the next post rather than a silent
// misdelivery, and bounding it would mean a uniqueness rule the registry
// deliberately does not have.
//
// carry records the content as pending carry-forward state (#2499), so the next user
// turn the daemon delivers for this conversation reaches claude with the post ahead
// of the operator's reply. It runs AFTER the durable record and BEFORE announce: the
// carry is a durable registry write and belongs with the other one, while announce
// is the fire-and-forget push that is deliberately last. It returns nothing and
// cannot fail the post, for announce's reason turned around — the post's own
// deliverable is the log entry, which has already landed, and a cron reads the exit
// code. Like announce it MAY BE NIL, which is the PTY posture and every unit test
// that wires no registry; a nil hook records and answers exactly as before.
func channelPoster(
	reg *conversations.Registry,
	create func(cwd, name string) (string, error),
	defaultCwd string,
	appendEntry func(conversations.ConversationID, string, json.RawMessage, time.Time) (uint64, error),
	announce func(protocol.AssistantDeltaPayload),
	carry func(conversations.ConversationID, string),
	log *slog.Logger,
) func(name, text string) error {
	return func(name, text string) error {
		// BOTH filter fields are set. They are pointers that AND, and a nil field
		// means "no filter on this field", so a filter naming only IsPromoted
		// would silently include channels the operator archived — and post into
		// one.
		promoted, archived := true, false
		var matches []conversations.Conversation
		for _, c := range reg.List(conversations.ListFilter{IsPromoted: &promoted, IsArchived: &archived}) {
			// Name is a *string: absent and stored-empty are distinct states, and
			// neither can match a label handleChannelPost has already refused to
			// let through empty.
			if c.Name != nil && *c.Name == name {
				matches = append(matches, c)
			}
		}

		var convID conversations.ConversationID
		switch len(matches) {
		case 1:
			convID = matches[0].ID
		case 0:
			// The workspace is the daemon's own resolved default, never a
			// caller-supplied path — this verb carries none across its wire.
			// channelCreator still confines it, because it confines whatever it is
			// given and this caller is not an exception to that.
			id, err := create(defaultCwd, name)
			if err != nil {
				// Forwarded verbatim: every reason create can return is already a
				// static constant for the reason its own doc block records, so
				// re-wrapping would either double a prefix or trade a precise reason
				// for a vague one. It has already logged the detail.
				return err
			}
			convID = conversations.ConversationID(id)
		default:
			log.Warn("control: channel.post refused an ambiguous name",
				"event", "channel_post.ambiguous_name",
				"matches", len(matches))
			return fmt.Errorf(fmtChannelPostAmbiguous, len(matches))
		}

		// ONE turn per post, minted before anything is written, because every chunk
		// below shares it and a client coalesces assistant_delta on exactly this
		// key. A post that reused another post's turn id would draw as part of that
		// message instead of as its own.
		turnID, err := newChannelPostTurnID()
		if err != nil {
			log.Error("control: channel.post turn-id mint failed",
				"event", "channel_post.rand_err",
				"conversation_id", string(convID))
			return errors.New(msgChannelPostRecordFailed)
		}

		// SPLIT BEFORE MARSHALLING. control.MaxChannelPostBytes admits 64 KiB and
		// the v2 application envelope caps at 65519 B, which encoding/json's
		// six-bytes-per-escaped-byte expansion puts far out of reach for a
		// single-frame post — so this is a precondition of delivery at the size the
		// verb already accepts, not a refinement. maxDeltaTextBytes is read, never
		// re-derived: interactiveTurnEmitterV2's own flushDelta bounds its frames
		// with the same call, and a second constant here would be a second place
		// the cap is decided. Every chunk is a substring, so concatenation
		// reproduces the post byte-for-byte and the split never cuts a rune.
		//
		// Empty text cannot reach here — handleChannelPost refuses an empty message
		// — and would emit nothing if it did.
		chunks := splitDeltaText(text, maxDeltaTextBytes)
		payloads := make([]protocol.AssistantDeltaPayload, 0, len(chunks))
		raws := make([]json.RawMessage, 0, len(chunks))
		for i, chunk := range chunks {
			// The conversation id is daemon-derived in both arms above — a registry
			// match or a freshly minted one — never a value a caller asserted. That is
			// Store.Append's precondition, and it is the whole of this call's
			// authorisation property: conversations.ValidID is a shape predicate, so a
			// canonical-shaped id from a caller would resolve genuinely inside the
			// conversation it named.
			p := protocol.AssistantDeltaPayload{
				ConversationID: string(convID),
				TurnID:         turnID,
				Seq:            i,
				// The main lane. A post is the host's own text rather than a
				// subagent's, so naming a parent tool call would claim an attribution
				// nothing produced. The key carries no omitempty, so the empty string
				// is emitted rather than omitted.
				ParentToolUseID: "",
				Text:            chunk,
			}
			raw, err := json.Marshal(p)
			if err != nil {
				// Defensive, matching both #2114 producers: AssistantDeltaPayload is
				// three strings and an int and cannot fail to marshal in practice.
				// Never echo the payload or err.Error() — encoding/json quotes invalid
				// input bytes into its error, which would put conversation content in a
				// log line.
				log.Error("control: channel.post payload marshal failed",
					"event", "channel_post.marshal_err",
					"conversation_id", string(convID))
				return errors.New(msgChannelPostRecordFailed)
			}
			payloads = append(payloads, p)
			raws = append(raws, raw)
		}

		// Stamped ONCE for the whole post, at the confirmed write —
		// newOperatorMessageHistory's rule, so a served page orders these entries by
		// when they landed rather than by when anything upstream was composed. One
		// stamp rather than one per chunk because one post is one message: a client
		// showing a time for it should not have to pick among N. UTC matches what
		// every other producer hoists, so entries from all of them are orderable by
		// the stored field.
		//
		// EVERY CHUNK IS RECORDED BEFORE ANY IS PUSHED. A post that cannot be
		// recorded must not appear on a screen, because a frame drawn for a message
		// the log does not hold vanishes on the next connect. The cost is that a
		// failure partway through a multi-chunk post leaves a PREFIX on disk and
		// still refuses: history.Store is append-only, so this is not transactional
		// and cannot be made so here. It is flushDelta's existing exposure, and a
		// prefix is at least not a mixture.
		ts := time.Now().UTC()
		for _, raw := range raws {
			if _, err := appendEntry(convID, protocol.TypeAssistantDelta, raw, ts); err != nil {
				log.Warn("control: channel.post history append failed",
					"event", "channel_post.history_append_err",
					"conversation_id", string(convID),
					"reason", historyAppendFailure(err))
				return errors.New(msgChannelPostRecordFailed)
			}
		}

		// Carry the content into claude's next turn for this conversation (#2499).
		// AFTER the durable record, because a post that could not be recorded is a
		// post that did not happen and must not reach claude either; the return above
		// is what enforces that ordering. THE WHOLE TEXT, not the chunks: the split
		// above exists because a v2 application envelope is byte-capped, and claude's
		// stdin is not — so re-joining what was only ever split for the wire would be
		// a round trip through a constraint this side does not have.
		if carry != nil {
			carry(convID, text)
		}

		// Tell every connected client (#2498). LAST, and only once the whole post is
		// on disk. Its result is deliberately not consulted and it returns no error
		// of its own — a failed push must not turn a recorded post into a refusal,
		// because the content is in the durable log whether or not anyone heard, and
		// a cron reads the exit code.
		if announce != nil {
			for _, p := range payloads {
				announce(p)
			}
		}

		// Neither the chunk count nor any per-chunk field is logged: across a
		// chunked post both are proxies for the message's length, and this verb's
		// content stays out of the daemon's log entirely.
		log.Info("control: channel.post recorded",
			"event", "channel_post.posted",
			"conversation_id", string(convID),
			"created", len(matches) == 0)
		return nil
	}
}

// newChannelPostTurnID mints the turn this verb addresses its assistant_delta
// frames to.
//
// It borrows conversations.NewID, whose output is a UUIDv4 from crypto/rand, for
// the reason fileAttacher borrows it for an attachment id: it is the repo's one
// id mint and the shape is what a client already expects in this field.
//
// AN RNG FAILURE FAILS THE POST, where #2497's message-id mint fell back to the
// empty string. That fallback was right for what it addressed — a message id is
// an identity a client dedupes on, so refusing delivery over an rng hiccup would
// have traded the deliverable for a cosmetic. A turn id is not that. It is the
// ADDRESS that decides which bubble the content lands in, and every post minting
// "" would coalesce into one bubble with every other, so the empty value silently
// breaks the one-post-one-message property rather than degrading a field nobody
// reads. startTurnIfNeeded is the precedent in this package: it declines to emit
// without a turn id. It can retry on the next event where a post has none, so
// refusing is the only form that precedent can take here — and a cron reads a
// non-zero exit.
func newChannelPostTurnID() (string, error) {
	id, err := conversations.NewID()
	if err != nil {
		return "", err
	}
	return string(id), nil
}

// channelUsage is the usage banner every parse-failure path prints before
// exiting 2 — one line per sub-verb, so an operator who typed the wrong one
// learns the other exists.
const channelUsage = "usage: pyry channel new [-pyry-name=<instance>] [-pyry-socket=<path>] [--name <label>]\n" +
	"       pyry channel post [-pyry-name=<instance>] [-pyry-socket=<path>] --name <label> (--text <string> | --file <path>)"

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

// parseChannelPostArgs is the flag-parse + arity check for
// `pyry channel post --name LABEL (--text STRING | --file PATH)`. Extracted from
// runChannelPost for parseChannelNewArgs' reason: the parsing rules unit-test
// without dialling the control socket.
//
// Every failure it returns is a USAGE failure — the exit-2 class. Nothing here
// touches the filesystem and nothing here inspects content; the read and the
// size bound live in channelPostContent, so the exit-code split is a function
// boundary rather than a condition inside one.
//
// --text and --file are mutually exclusive and one is required. Both rules are
// checked against how many flags the caller actually SET, via fs.Visit, not
// against whether the values came back empty: `--text ""` is a caller who chose
// an empty message and must be told so by the daemon's empty-message refusal,
// not silently re-read as "no --text given" and rejected here as a usage error.
//
// The FlagSet discards its own output (io.Discard) so the caller owns every byte
// on stderr — runChannelPost prints the error and the usage banner itself.
func parseChannelPostArgs(args []string) (name, text, file string, err error) {
	fs := flag.NewFlagSet("pyry channel post", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	nameFlag := fs.String("name", "", "display name of the channel to post into")
	textFlag := fs.String("text", "", "message content, given inline")
	fileFlag := fs.String("file", "", "message content, read from this file")
	if err := fs.Parse(args); err != nil {
		return "", "", "", err
	}
	if fs.NArg() > 0 {
		return "", "", "", fmt.Errorf("unexpected positional %q", fs.Arg(0))
	}
	if *nameFlag == "" {
		return "", "", "", errors.New("--name is required")
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch {
	case set["text"] && set["file"]:
		return "", "", "", errors.New("--text and --file are mutually exclusive")
	case !set["text"] && !set["file"]:
		return "", "", "", errors.New("one of --text or --file is required")
	}
	return *nameFlag, *textFlag, *fileFlag, nil
}

// channelPostContent resolves the message body from whichever source
// parseChannelPostArgs accepted, and bounds it. Exactly one of text and file is
// meaningful; the caller has already enforced that.
//
// Every failure here is the exit-1 class: an unreadable file and an over-long
// message are verdicts on the operator's input, not on their command line.
//
// The file is OPENED ONCE and read under an io.LimitReader, never stat-ed for a
// size and then opened. A check-then-use pair reports the size of one inode and
// reads another, and the bytes actually read are the only value that matters
// here; bounding the read is also what turns `--file /dev/zero` into a refusal
// rather than a hang.
//
// The bound is control.MaxChannelPostBytes, the same constant handleChannelPost
// refuses past — read, not copied. This check is not a duplicate of the
// daemon's: that one is the contract, which holds for any process that dials the
// socket, and this one is a bound on this process's own read, which the daemon
// cannot perform on its behalf. The empty-content rule has no such second job
// and therefore lives daemon-side only.
func channelPostContent(text, file string) (string, error) {
	if file == "" {
		if len(text) > control.MaxChannelPostBytes {
			return "", fmt.Errorf("message is larger than the %d-byte limit", control.MaxChannelPostBytes)
		}
		return text, nil
	}

	f, err := os.Open(file)
	if err != nil {
		return "", fmt.Errorf("read message file: %w", err)
	}
	defer func() { _ = f.Close() }()

	// One byte past the cap, so a file that sits exactly on it is accepted and
	// the byte after it is detected without reading the rest of the file.
	body, err := io.ReadAll(io.LimitReader(f, control.MaxChannelPostBytes+1))
	if err != nil {
		return "", fmt.Errorf("read message file: %w", err)
	}
	if len(body) > control.MaxChannelPostBytes {
		return "", fmt.Errorf("message is larger than the %d-byte limit", control.MaxChannelPostBytes)
	}
	return string(body), nil
}

// channelVerdict returns (exitCode, stderrLine) for a `pyry channel <sub>`
// result. exitCode == 0 means success and stderrLine is ""; exitCode == 1 means
// failure and stderrLine is the one-line operator-readable message to print
// before os.Exit(1). Pure: deterministic on err, so the unit test never has to
// intercept os.Exit. Mirrors rekeyVerdict, the same idiom.
//
// sub is the sub-verb, carried only so the prefix names the command the operator
// typed. It is the reason this is one shared formatter rather than one per verb:
// a twin would be four copied lines that drift the first time either message is
// reworded.
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
func channelVerdict(sub string, err error) (exitCode int, stderrLine string) {
	if err == nil {
		return 0, ""
	}
	return 1, fmt.Sprintf("pyry channel %s: %s", sub, err.Error())
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
	case "post":
		return runChannelPost(socketPath, subArgs)
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
		return channelExit("new", fmt.Errorf("resolve current directory: %w", err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	id, err := control.ChannelNew(ctx, socketPath, cwd, name)
	if err != nil {
		return channelExit("new", err)
	}
	fmt.Println(id)
	return nil
}

// runChannelPost implements
// `pyry channel post --name LABEL (--text STRING | --file PATH)`: record one
// message in the named channel, creating that channel under the daemon's default
// workspace when the label matches nothing.
//
// IT PRINTS NOTHING ON SUCCESS, and that is a contract rather than a style
// choice: the first consumer is a cron on the operator's box, where any byte on
// stdout or stderr becomes mail. There is deliberately no created-id echo of the
// kind `channel new` writes — a caller who needs the id asks for a conversation
// list, and a caller who is a cron needs an exit code.
//
// Unlike runChannelNew it sends NO cwd. The create-on-miss workspace is resolved
// daemon-side, so this side has nothing to resolve and no os.Getwd to fail on.
//
// Exit codes: 0 and silence; 2 for usage failures (flag parse, stray positional,
// missing --name, neither or both of --text/--file), printed without main's
// `pyry: ` prefix; 1 for everything else — an unreadable --file, an over-long
// message, a daemon refusal, or a transport failure — as a single
// `pyry channel post: …` line on stderr.
func runChannelPost(socketPath string, args []string) error {
	name, text, file, err := parseChannelPostArgs(args)
	if err != nil {
		return channelUsageExit(err.Error())
	}

	body, err := channelPostContent(text, file)
	if err != nil {
		return channelExit("post", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := control.ChannelPost(ctx, socketPath, name, body); err != nil {
		return channelExit("post", err)
	}
	return nil
}

// channelExit prints channelVerdict's line and exits with its code. Split from
// the run functions so the formatting stays in the pure verdict function and
// only this three-line wrapper touches os.Exit.
func channelExit(sub string, err error) error {
	exitCode, stderrLine := channelVerdict(sub, err)
	fmt.Fprintln(os.Stderr, stderrLine)
	os.Exit(exitCode)
	return nil // unreachable
}
