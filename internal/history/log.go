// Package history is the durable, append-only, per-conversation message log the
// daemon writes as it fans envelopes out, and the backward reader that serves a
// connecting client the messages that happened before it arrived.
//
// It is the storage floor only: nothing here produces or consumes entries.
// #2114 and #2115 append, #2116 serves a page, #2113 declares the wire verb.
//
// # Why a log rather than claude's own transcripts
//
// The daemon owns this log; nothing here reads a file claude wrote. Serving
// history from claude's transcripts was rejected because that format is not a
// contract this repo versions — old files are never rewritten, so a serving
// path over them means supporting every format that ever existed, permanently —
// because it binds history to one harness (internal/acp is a second front door
// that will never write those files), and because the daemon already produces
// the wire mapping every turn.
//
// # What one entry is
//
// One entry is one wire envelope: the triple internal/eventring retains — the
// wire type, the already-marshalled payload bytes and the timestamp — plus a
// durable id this package mints. Payload bytes are stored and returned
// OPAQUELY: this package never decodes one and never imports the wire-payload
// types, which is what keeps it the leaf eventring already is.
//
// # Why segments
//
// Retention is none, deliberately, and the access pattern is newest-first
// walking backwards on demand. Those two decisions are the storage design:
// the newest segment is small, older ones are opened only when the operator
// scrolls into them, segment boundaries ARE the index so no sidecar file
// exists, and a future retention policy becomes a file delete rather than a
// rewrite. Not because a plain append-only file cannot be opened at its end
// cheaply — it can, and that was never the reason.
//
// # Durability
//
// Process-restart durability, not crash consistency: a store opened over a
// directory an earlier store wrote reads back everything it wrote, and entry
// ids continue past every id on disk. Surviving a machine crash is out of
// scope, which is why no append fsyncs.
//
// A write that loses PARTWAY is not out of scope, and is not a crash: an
// ordinary ENOSPC or EIO leaves behind a headerless segment or a torn final
// line, because a failing write reports the bytes it already transferred. An
// append undoes what it can, and the reader is built to make progress past what
// it could not — see writeSegment, decodeSegment and load. The rule is that a
// failed append is a failed append: it never costs a conversation the entries
// that succeeded before it.
//
// # Concurrency
//
// Internally synchronised by one leaf mutex, the posture eventring establishes:
// more than one goroutine appends to a conversation (the envelope chokepoint
// and the delivery path) while reads run on a per-connection worker, and no
// producer takes a lock of its own. No goroutine is spawned and no file handle
// is held between calls, so there is nothing to shut down and no Close.
//
// # Logging
//
// Payload bytes are conversation content and this log is the one place they are
// written. The package takes no *slog.Logger and makes no log call — the
// structural form of that guarantee. No error it builds names a payload, a wire
// type or an entry's size; refusals name the conversation id, the segment file
// and the failing operation only.
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// MaxSegmentBytes is the named bound a segment rolls at. It is checked BEFORE a
// write, so a segment may exceed it by at most the one entry that crosses it —
// which, with Append's refusal of an entry line longer than the bound, is what
// gives a well-formed segment the structural ceiling the reader caps its
// allocation at (segmentCeiling).
//
// A tunable starting point, not load-tested, in the same spirit as
// eventring.MaxEventsPerConversation. Tests construct through newStore to vary
// it, which is how a segment-boundary crossing is provable with tens of entries
// instead of thousands.
const MaxSegmentBytes int64 = 1 << 20

// MaxPageEntries is the ceiling a requested page size is CLAMPED to — not
// refused above, since a page is "up to limit entries" and a clamped answer
// still returns a cursor, so an over-large ask pages rather than fails.
//
// It is what makes Page's work bounded by a constant in this package rather
// than by its caller's arithmetic. #2116 derives its page size from what fits
// in the 65519-byte v2 application envelope, so this ceiling is far above
// anything it can legitimately ask for; without it, a caller passing
// math.MaxInt would read and decode every segment of the log.
const MaxPageEntries int = 4096

// ErrInvalidID reports a conversation id that is not of canonical shape. It is
// raised before the filesystem is touched, so a refusal leaves nothing behind.
var ErrInvalidID = errors.New("history: conversation id is not of canonical shape")

// ErrNotContained reports a conversation's log directory whose symlink-resolved
// path is not the one its id maps to beneath the resolved instance directory —
// whether it lands outside that directory entirely, or inside it under another
// conversation. It is deliberately distinguishable from ErrInvalidID: an id
// refusal means no path was ever built.
var ErrNotContained = errors.New("history: conversation log directory resolves outside the destination it was built for")

// ErrInvalidPayload reports an entry this package will not store: bytes that are
// not valid JSON, or an entry whose encoded line exceeds the segment bound.
//
// ONE sentinel covers both because neither is repairable by anyone who could
// see the difference — both producers hand over the output of a json.Marshal of
// a wire payload, so either refusal is a daemon bug rather than something a
// client can re-send its way out of.
var ErrInvalidPayload = errors.New("history: entry payload is not storable")

// ErrInvalidPageSize reports a page request for fewer than one entry. An
// over-large request is clamped rather than refused; only a non-positive one is
// an error, because there is no page it could describe.
var ErrInvalidPageSize = errors.New("history: page size must be at least one entry")

// ErrInvalidCursor reports a cursor that will not be followed: malformed, of an
// unknown version, minted for another conversation, naming a segment that is
// not in this conversation's log, or an offset that is not an entry boundary in
// it.
//
// ONE sentinel covers all of those on attachments.ErrNotFound's reasoning. The
// distinctions are exactly what a probe would want — "does this segment exist",
// "is this a real boundary" — so a consumer that cannot branch on them cannot
// leak them, and the decision survives a careless dispatch site rather than
// depending on one.
var ErrInvalidCursor = errors.New("history: cursor was not minted for this conversation's log")

// ErrUnknownVersion reports a segment whose first written bytes are not a header
// this build recognises. The reader returns it having decoded NO entry from
// that segment, rather than reading it as the version it does know.
var ErrUnknownVersion = errors.New("history: segment carries a schema version this build does not recognise")

// ErrCorruptSegment reports a segment that IS of a recognised version but does
// not decode: a complete line that is not an entry, or a file past the ceiling a
// well-formed segment can reach. Deliberately distinct from ErrUnknownVersion —
// one says "not mine to read", the other "mine, and damaged".
//
// An UNTERMINATED final line is deliberately not in that list: it is the residue
// of a write that lost partway, it is dropped rather than refused, and
// decodeSegment carries the reasoning.
var ErrCorruptSegment = errors.New("history: segment does not decode")

// Entry is one retained wire envelope: the durable id this package mints plus
// the three fields eventring.Event retains. The per-connection envelope id is
// deliberately absent, exactly as it is there — it is meaningless across
// connections.
//
// Payload is stored and returned opaquely; nothing in this package decodes it.
// It round-trips through encoding/json, so JSON-insignificant whitespace does
// not survive, and both producers hand over already-compact json.Marshal output
// so in practice the bytes are unchanged. HTML escaping is off, so '<', '>' and
// '&' survive as written.
type Entry struct {
	ID      uint64          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	TS      time.Time       `json:"ts"`
}

// Page is one answer from the backward walk.
//
// AtStart is what distinguishes "the log begins here" from "a full page that
// may have more behind it". A page that fills EXACTLY at the log's first entry
// reports AtStart false and a usable Cursor; the call after it returns no
// entries with AtStart true. So a walk terminates on AtStart, never on an empty
// Entries — which is also why Cursor is empty whenever AtStart is set.
type Page struct {
	Entries []Entry // newest-first
	Cursor  string  // opaque; hand back to Page for the next-older page
	AtStart bool    // the start of the log was reached while filling this page
}

// convLog is one conversation's append cursor, recovered from disk the first
// time this process touches the conversation. It holds no resolved path: the
// log directory is re-resolved on every call, deliberately (see resolveDir).
type convLog struct {
	loaded   bool
	seg      uint64 // active segment number, 0 before the first append
	segBytes int64  // size of the active segment on disk
	nextID   uint64 // the next entry id for this conversation
}

// Store is the per-conversation log rooted at one instance directory. The zero
// value is not usable — construct with New. All methods are safe for concurrent
// use.
type Store struct {
	mu              sync.Mutex
	instanceDir     string
	maxSegmentBytes int64
	convs           map[conversations.ConversationID]*convLog

	// AC-4 instrumentation: the two numbers "serving the newest page does work
	// bounded by the requested page size, not by the log size" is stated in.
	// Bumped at the single place a segment file is read, and read back by
	// in-package tests through readStats.
	readBytes      int64
	segmentsOpened int
}

// New returns a Store writing beneath instanceDir, with segments bounded at
// MaxSegmentBytes.
//
// instanceDir is an ARGUMENT and is never resolved from configuration inside
// this package, matching attachments.EnsureDir. The layout beneath it is
// conversations/<conversation-id>/history/, a sibling of the attachments
// directory under the same per-conversation root.
func New(instanceDir string) *Store { return newStore(instanceDir, MaxSegmentBytes) }

// newStore is New with the segment bound exposed. It is unexported because only
// tests need to vary it: AC 1's boundary crossing and AC 4's many-segment log
// would otherwise need fixtures of thousands of entries to reach a second
// segment. Keeping it unexported means the bound is settable without the
// exported surface growing a knob no production caller should turn.
func newStore(instanceDir string, maxSegmentBytes int64) *Store {
	return &Store{
		instanceDir:     instanceDir,
		maxSegmentBytes: maxSegmentBytes,
		convs:           make(map[conversations.ConversationID]*convLog),
	}
}

// Append records one envelope in convID's log and returns the durable id it
// minted. Ids are per-conversation, start at 1, strictly increase in append
// order, and are recovered from disk on the first touch of a conversation, so
// they continue past every id already written rather than restarting.
//
// That is the one thing eventring's ids cannot do: its counter is in memory and
// restarts at 1 on every daemon start, which is exactly why its event_id cannot
// be the durable identifier here. The counter is per-conversation rather than
// store-wide — the inverse of eventring's #2022 choice — because the defect
// that drove #2022 cannot arise: every cursor this package mints names its own
// conversation and is refused on any other, so there is no shared scalar for a
// watermark taken in one conversation to mute another with.
//
// PRECONDITION, and it carries the whole authorisation property: convID MUST be
// the conversation the authenticated session is already on, never one a client
// asserted. conversations.ValidID is a SHAPE predicate, not an authorisation
// check — a client-supplied id of canonical shape genuinely resolves inside the
// conversation it names and defeats every check below. This is
// attachments.ResolvePath's precondition, for the same reason; Intake's
// resolver-callback shape (reach the conversation through the session, not off
// the wire) is what a consumer should copy.
//
// payload must be valid JSON and its encoded line must fit the segment bound;
// both are ErrInvalidPayload. The JSON check and the payload's own length are
// tested before the filesystem is touched at all; the encoded line carries the
// id, which is only known once the conversation has been resolved, so that
// refusal lands after the log directory exists. Neither stores an entry or
// creates a segment file — an empty directory is the most a refusal leaves.
//
// Not durable against a machine crash: the bytes reach the page cache, not the
// platter, because the ticket rules crash consistency out of scope rather than
// buying it with an fsync per append.
func (s *Store) Append(convID conversations.ConversationID, typ string, payload json.RawMessage, ts time.Time) (uint64, error) {
	if !conversations.ValidID(string(convID)) {
		return 0, fmt.Errorf("%w: conversation id %q", ErrInvalidID, string(convID))
	}
	if !json.Valid(payload) {
		return 0, fmt.Errorf("%w: conversation %q: not valid JSON", ErrInvalidPayload, string(convID))
	}
	// The cheap half of the size bound, before the filesystem is touched. The
	// encoded line is checked again below, once the id and timestamp are on it.
	if int64(len(payload)) > s.maxSegmentBytes {
		return 0, fmt.Errorf("%w: conversation %q: larger than the segment bound", ErrInvalidPayload, string(convID))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dir, err := s.resolveDir(convID, true)
	if err != nil {
		return 0, err
	}
	c, err := s.load(convID, dir)
	if err != nil {
		return 0, err
	}

	line, err := encodeEntry(Entry{ID: c.nextID, Type: typ, Payload: payload, TS: ts})
	if err != nil {
		return 0, err
	}
	if int64(len(line)) > s.maxSegmentBytes {
		return 0, fmt.Errorf("%w: conversation %q: larger than the segment bound", ErrInvalidPayload, string(convID))
	}

	// Roll on the size the segment ALREADY has, so the decision never depends
	// on the entry about to be written and a segment's entry count is a
	// function of the bound alone.
	seg, buf := c.seg, line
	if seg == 0 || c.segBytes >= s.maxSegmentBytes {
		seg = c.seg + 1
		buf = append([]byte(segmentHeaderLine), line...)
	}
	written, err := s.writeSegment(filepath.Join(dir, segmentName(seg)), buf, seg != c.seg)
	if err != nil {
		// A write that lost leaves this process's belief about where the log
		// ends unverified — how much of buf landed is exactly what the error
		// does not say. Drop the belief rather than append against it: the next
		// call re-derives the active segment, its size and the next id from
		// what is actually on disk. That is also what gets a conversation
		// moving again when writeSegment's undo could not run and a residue
		// remains: load rolls past a segment it cannot safely append to.
		c.loaded = false
		return 0, err
	}

	id := c.nextID
	c.nextID++
	if seg != c.seg {
		c.seg, c.segBytes = seg, written
	} else {
		c.segBytes += written
	}
	return id, nil
}

// Page returns up to limit of convID's entries, newest-first, ending at cursor's
// position when one is given.
//
// Hand back the Cursor of the previous page to get the next older one; a walk
// terminates when a page reports AtStart. An empty cursor starts at the newest
// entry, and a conversation with no log on disk reads as empty-and-at-the-start
// rather than as an error.
//
// PRECONDITION: convID MUST be the conversation the authenticated session is
// already on — see Append, which carries the full reasoning. This is the method
// a remote request reaches, so the precondition is load-bearing here first.
//
// cursor arrives from an untrusted client: the wire declares it opaque and the
// handler above passes it through without parsing, so THIS is the only place a
// cursor is ever validated. A cursor this package did not mint is refused
// rather than followed, and a refusal reads nothing.
//
// limit below 1 is ErrInvalidPageSize; above MaxPageEntries it is clamped. The
// result slice is never pre-allocated from it, so an over-large ask pins no
// memory of its own.
//
// The work is bounded by the page, not by the log: the number of segments
// opened and the bytes read from them are a function of limit and the segment
// bound, never of how many entries the conversation holds. The one cost that
// does grow with the log is the single directory listing, which opens no
// segment and decodes no entry — the price of tolerating a future retention
// delete, see listSegments.
func (s *Store) Page(convID conversations.ConversationID, cursor string, limit int) (Page, error) {
	if !conversations.ValidID(string(convID)) {
		return Page{}, fmt.Errorf("%w: conversation id %q", ErrInvalidID, string(convID))
	}
	if limit < 1 {
		return Page{}, fmt.Errorf("%w: asked for %d", ErrInvalidPageSize, limit)
	}
	if limit > MaxPageEntries {
		limit = MaxPageEntries
	}

	// Structurally refused before the lock and before any path is built, so a
	// forged cursor costs nothing and touches nothing.
	var pos cursorPos
	if cursor != "" {
		var err error
		if pos, err = parseCursor(cursor, convID); err != nil {
			return Page{}, err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dir, err := s.resolveDir(convID, false)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if cursor != "" {
			// Nothing was ever written for this conversation, so no cursor can
			// name a position in it.
			return Page{}, cursorRefusal(convID, "names a segment that is not in this log")
		}
		return Page{AtStart: true}, nil
	case err != nil:
		return Page{}, err
	}
	segs, err := listSegments(dir)
	if err != nil {
		return Page{}, err
	}

	start := len(segs) - 1
	if cursor != "" {
		start = -1
		for i := range segs {
			if segs[i].num == pos.segment {
				start = i
				break
			}
		}
		if start < 0 {
			return Page{}, cursorRefusal(convID, "names a segment that is not in this log")
		}
	}

	var out []Entry
	var oldest cursorPos
	full := false
	for i := start; i >= 0 && !full; i-- {
		entries, _, _, err := s.readSegment(filepath.Join(dir, segs[i].name))
		if err != nil {
			return Page{}, err
		}

		// How far into this segment the page reaches. Everything, unless the
		// cursor names a position inside it — then only what is strictly older
		// than that position.
		end := len(entries)
		if cursor != "" && i == start {
			end = -1
			for k := range entries {
				if entries[k].offset == pos.offset {
					end = k
					break
				}
			}
			// The offset is compared against boundaries read off the bytes,
			// never derived from one. That is the eventring #2022 lesson: a
			// boundary computed from what a sequence implies is only as sound
			// as the assumption behind it, and arithmetic on an untrusted
			// number wraps. Here math.MaxUint64 is simply not in this list.
			if end < 0 {
				return Page{}, cursorRefusal(convID, "offset is not an entry boundary")
			}
		}

		for k := end - 1; k >= 0; k-- {
			// Nothing on the entry aliases the segment buffer, so a page pins
			// its own content rather than every byte of the segments it walked:
			// json.RawMessage.UnmarshalJSON is documented to set its receiver to
			// a COPY of the input, and a decoded string is a fresh allocation
			// too. No defensive clone here — one would be an allocation per
			// entry per page, bought against a premise that does not hold.
			out = append(out, entries[k].entry)
			oldest = cursorPos{segment: segs[i].num, offset: entries[k].offset}
			if len(out) == limit {
				full = true
				break
			}
		}
	}

	if full {
		return Page{Entries: out, Cursor: mintCursor(convID, oldest.segment, oldest.offset)}, nil
	}
	return Page{Entries: out, AtStart: true}, nil
}

// load recovers convID's append cursor from disk the first time this process
// touches the conversation: the active segment, its size, and the next entry id.
// Callers hold s.mu.
//
// nextID comes from the last entry of the newest segment that holds one. In the
// ordinary case that is the newest segment and the loop reads nothing extra;
// walking further back covers a newest segment left holding only a header,
// which a crash mid-append can produce and which would otherwise restart the id
// space at 1 while older entries still carry higher ids.
//
// A newest segment that decodeSegment reports as INCOMPLETE is the same
// tolerance one step further, and it covers both residues a write that lost can
// leave when writeSegment's undo could not run: a file carrying no header, so
// that an entry appended to it would sit where the version belongs; and a file
// whose last line is torn, so that an appended entry would be concatenated onto
// it and neither line would decode. Both are reported as FULL instead, so the
// next append rolls past — listSegments tolerates the gap that leaves by
// construction, which is the same property that lets a retention policy delete
// segments out of the middle.
func (s *Store) load(convID conversations.ConversationID, dir string) (*convLog, error) {
	c := s.convs[convID]
	if c == nil {
		c = &convLog{}
		s.convs[convID] = c
	}
	if c.loaded {
		return c, nil
	}

	segs, err := listSegments(dir)
	if err != nil {
		return nil, err
	}
	c.nextID = 1
	for i := len(segs) - 1; i >= 0; i-- {
		entries, size, complete, err := s.readSegment(filepath.Join(dir, segs[i].name))
		if err != nil {
			return nil, err
		}
		if i == len(segs)-1 {
			c.seg, c.segBytes = segs[i].num, size
			if !complete {
				c.segBytes = s.maxSegmentBytes // not appendable: roll past it
			}
		}
		if n := len(entries); n > 0 {
			c.nextID = entries[n-1].entry.ID + 1
			break
		}
	}
	c.loaded = true
	return c, nil
}

// writeSegment appends buf to one segment file and reports how many bytes
// landed. Callers hold s.mu, so each append is one uninterrupted write.
//
// No handle is kept open between calls and nothing is fsynced: the file is
// append-only so there is no partial state to make atomic, and durability here
// is process-restart durability, which the page cache already provides.
//
// O_NOFOLLOW because containment of the DIRECTORY is not containment of the
// file inside it: a segment name replaced by a symlink must be refused, not
// written through. On a fresh segment O_EXCL says the same thing more strongly —
// it refuses an existing path of any kind, so a header can never be appended
// into a file that already holds entries.
//
// A write or a close that loses — ENOSPC, EDQUOT, EIO; ordinary failures, not a
// machine crash — leaves a residue that differs only by which file it lands in,
// because os.File.Write reports the bytes it DID transfer alongside the error:
// a fresh segment holding no header or a partial one, or an active segment
// whose last line is torn. Both are undone here, the fresh one by removing the
// file and the active one by truncating it back to the length it had before this
// call, read off the file rather than taken from this process's belief about it.
// That is what keeps a failed append a failed append.
//
// The undo is best-effort BY DESIGN and the guarantee does not rest on it:
// os.Remove and Truncate can themselves fail, and a machine crash between the
// create and the write leaves the same residue with no undo to run at all. What
// carries the guarantee — a conversation is never permanently unreadable — is
// that load reports either residue as full and rolls past it, and decodeSegment
// drops a torn tail rather than refusing the segment that holds it.
func (s *Store) writeSegment(path string, buf []byte, fresh bool) (int64, error) {
	flags := os.O_WRONLY | os.O_APPEND | syscall.O_NOFOLLOW
	if fresh {
		flags |= os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return 0, fmt.Errorf("history: open segment %q: %w", path, err)
	}
	// The length to undo an active segment back to. Stat, not c.segBytes: a
	// truncate to a length this process merely believes destroys entries that
	// did land. -1 says no undo is available, and the residue is then the
	// reader's tolerance to carry. Mutually exclusive with fresh, whose undo is
	// to remove the file whole.
	pre := int64(-1)
	if !fresh {
		if info, err := f.Stat(); err == nil {
			pre = info.Size()
		}
	}
	n, err := f.Write(buf)
	if err != nil {
		// Through the descriptor this call already opened, before it is closed:
		// truncating by name would re-resolve a path the O_NOFOLLOW open has
		// vouched for once already.
		if pre >= 0 {
			_ = f.Truncate(pre)
		}
		_ = f.Close()
		if fresh {
			_ = os.Remove(path)
		}
		return 0, fmt.Errorf("history: write segment %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		// Some filesystems report a deferred write error here, so the same
		// residue is possible; the descriptor is gone, so the undo goes by name.
		if pre >= 0 {
			_ = os.Truncate(path, pre)
		}
		if fresh {
			_ = os.Remove(path)
		}
		return 0, fmt.Errorf("history: close segment %q: %w", path, err)
	}
	return int64(n), nil
}

// readSegment reads one segment whole and decodes it, reporting the entries,
// the file's size, whether a new entry may be appended to it (decodeSegment's
// completeness), and the two AC-4 counters' worth of work it did. Callers hold
// s.mu.
//
// The read is capped at segmentCeiling: a well-formed segment cannot exceed it,
// so a file that reaches it was not written by this package — enlarged out of
// band, or a symlink that slipped past listSegments' leaf check — and is
// refused rather than allocated. Reading the segment whole is what bounds the
// work: the bound is the segment constant, never the log's size.
func (s *Store) readSegment(path string) ([]segEntry, int64, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("history: open segment %q: %w", path, err)
	}
	defer func() { _ = f.Close() }() // read-only; nothing to report on close

	ceiling := segmentCeiling(s.maxSegmentBytes)
	data, err := io.ReadAll(io.LimitReader(f, ceiling+1))
	s.segmentsOpened++
	s.readBytes += int64(len(data))
	if err != nil {
		return nil, 0, false, fmt.Errorf("history: read segment %q: %w", path, err)
	}
	if int64(len(data)) > ceiling {
		return nil, 0, false, fmt.Errorf("%w: %q exceeds the size a segment can reach", ErrCorruptSegment, path)
	}

	entries, complete, err := decodeSegment(data)
	if err != nil {
		return nil, 0, false, fmt.Errorf("%q: %w", path, err)
	}
	return entries, int64(len(data)), complete, nil
}

// resolveDir returns the symlink-resolved path of convID's log directory,
// creating it when create is set. Callers hold s.mu.
//
// It is attachments.EnsureDir's and ResolvePath's discipline minus the
// attachment-id level. The resolved INSTANCE directory is the anchor: anchoring
// on the resolved conversation directory would defeat the check, since a
// conversation directory symlinked out resolves first and everything beneath it
// is then trivially "contained". The destination is built textually beneath
// that anchor and the comparison is full-path EQUALITY, not a filepath.Rel
// "is it under the root" test — equality is strictly stronger, and it is what
// refuses a history directory symlinked at a SIBLING conversation, which stays
// inside the instance directory and passes any containment test.
//
// The resolved path is deliberately NOT cached across calls. Caching it would
// save a few lstat calls and would widen EnsureDir's accepted check-then-use
// window from within one call to the daemon's whole lifetime: one symlink
// planted after the first resolve would redirect every later append and read.
// EnsureDir's bound — exploiting the window needs write access inside the
// daemon's own 0o700 state directory — justifies the narrow window, not a
// process-long one.
//
// On the read path a destination that does not exist is NOT an error: the
// wrapped fs.ErrNotExist is how Page tells "this conversation has no log" from
// a real failure, and it creates nothing, so a lookup never leaves a directory
// behind for a probe it refused.
func (s *Store) resolveDir(convID conversations.ConversationID, create bool) (string, error) {
	abs, err := filepath.Abs(s.instanceDir)
	if err != nil {
		return "", fmt.Errorf("history: resolve instance directory %q: %w", s.instanceDir, err)
	}
	if create {
		// Created lazily rather than required, matching every other registry
		// under the instance directory, and created before it is resolved
		// because EvalSymlinks fails on a path that does not exist.
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return "", fmt.Errorf("history: create instance directory %q: %w", abs, err)
		}
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("history: resolve instance directory %q: %w", abs, err)
	}
	want := filepath.Join(root, "conversations", string(convID), "history")

	if !create {
		resolved, err := filepath.EvalSymlinks(want)
		if err != nil {
			return "", fmt.Errorf("history: resolve log directory %q: %w", want, err)
		}
		if resolved != want {
			return "", fmt.Errorf("%w: %q resolves to %q", ErrNotContained, want, resolved)
		}
		return want, nil
	}

	// Split want into the longest leading ancestor that exists and the
	// not-yet-existing suffix, so what is about to be created can be resolved
	// BEFORE anything is created beneath an offending symlink. The probe is
	// Lstat, not Stat, so a symlink counts as existing and is resolved below
	// rather than stepped over.
	existing, rest := want, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break // reached the filesystem root; guard against looping
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	existingReal, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("history: resolve log directory %q: %w", existing, err)
	}
	candidate := existingReal
	if rest != "" {
		candidate = filepath.Join(existingReal, rest)
	}
	if candidate != want {
		return "", fmt.Errorf("%w: %q resolves to %q", ErrNotContained, want, candidate)
	}

	if err := os.MkdirAll(want, 0o700); err != nil {
		return "", fmt.Errorf("history: create log directory %q: %w", want, err)
	}
	// Post-creation: the path now exists, so EvalSymlinks canonicalises it
	// fully. It catches a destination that became escaping during creation.
	final, err := filepath.EvalSymlinks(want)
	if err != nil {
		return "", fmt.Errorf("history: resolve log directory %q: %w", want, err)
	}
	if final != want {
		return "", fmt.Errorf("%w: %q resolves to %q", ErrNotContained, want, final)
	}
	return final, nil
}

// readStats reports the AC-4 counters: bytes read from segment files, and
// segment files opened. Unexported — it exists so a test can assert that
// serving the newest page does work bounded by the page rather than by the log,
// which is not observable from the answer alone.
func (s *Store) readStats() (int64, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readBytes, s.segmentsOpened
}

// resetReadStats zeroes the counters so a test can measure one operation.
func (s *Store) resetReadStats() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readBytes, s.segmentsOpened = 0, 0
}
