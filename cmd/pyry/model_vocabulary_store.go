package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// modelVocabularyStore is the daemon's LAST-SEEN model vocabulary, kept in one
// file per daemon instance so it survives a restart (#2450). It is the THIRD and
// final source `retainedModelVocabulary` reads, after the bound session's hold and
// the bootstrap's.
//
// # Why a file at all
//
// The vocabulary reaches a client only as the `model_list` frame, built from
// claude's `initialize` reply once per child spawn and retained in memory on
// `sessionModelHold`. #2124 added the daemon-wide fallback on the premise that the
// bootstrap child asks at daemon start, which #2085 had already removed: nothing
// spawns at start, so a restarted daemon holds NO vocabulary anywhere until a turn
// runs in the conversation bound to the bootstrap session — and since every minted
// conversation gets its own session, on a daemon whose bootstrap-bound conversation
// is idle that is never. #2084 named this cold-daemon gap and offered caching the
// last reply as one of two shapes; #2124 took the in-memory half and this takes the
// other.
//
// ONE FILE PER DAEMON INSTANCE is the right grain because the vocabulary is a
// property of the MACHINE AND ACCOUNT rather than of a conversation — the same fact
// that licenses #2124's cross-conversation read, and the reason this file carries no
// conversation id, no session id and no timestamp. It is a sibling of sessions.json
// and conversations.json under ~/.pyry/<name>/; see `resolveModelVocabularyPath`.
//
// # Why it is not a fifth sessionRetentions member
//
// It is per-DAEMON, where every hold is per-SESSION, and it is fed by a non-retaining
// sink decorator chained inside `newStreamRunnerFactory` rather than by
// `newSessionParser` — `sessionResetFollower`'s argument, applied once more. Minting
// it inside `newSessionParser` would force that function to know about a daemon-wide
// object it has no business knowing about, and would thread a persist parameter
// through every hold constructor for no behaviour gained.
//
// SECURITY: it has no *slog.Logger field and its constructor takes none, so there is
// no path by which a model value can reach a log record — the #833 posture
// `sessionModelHold` takes for the same reason, enforced by construction rather than
// by care. The LOAD path has a second, independent reason to stay silent: encoding/json
// quotes the offending input into its error text, so a "could not decode
// model_list.json" line would put claude-authored file bytes into a record. Do not give
// this type a logger, at any level, for either half.
type modelVocabularyStore struct {
	// path is the destination. Fixed at construction and never derived from anything
	// a caller or a child supplies.
	path string

	mu sync.Mutex
	// list is the last vocabulary this daemon saw — restored by Load, replaced by
	// Retain. Replaced by ASSIGNMENT and never mutated in place, which is what lets
	// the writer goroutine snapshot the struct header under the mutex and marshal
	// outside it.
	list turnevent.ModelList
	have bool
	// dirty says list has not yet reached the file; writing says a writer goroutine
	// is draining. The pair is read and written inside ONE critical section, so there
	// is no check-then-mutate gap.
	dirty   bool
	writing bool
	closed  bool
	// lastWritten is the encoded body of the most recent successful write, and the
	// whole of the write-amplification defence: a retention encoding to the same bytes
	// writes nothing. It is what makes a respawn re-reporting the same menu — the
	// common case — cost zero writes, with no rate limiter and no clock. Load seeds it
	// so a restart does not rewrite what it just read.
	lastWritten []byte
	// wg joins the writer goroutine. Every Add is taken under mu before closed is set,
	// and Close sets closed under mu before waiting, so an Add can never race the Wait.
	wg sync.WaitGroup
}

// maxModelVocabularyFile caps how many bytes Load will read, and it is the ONE bound
// this type applies to the file's contents.
//
// It is a STRUCTURAL AGGREGATE bound and deliberately not a restatement of the
// producer's. streamsup bounds all three of the list's dimensions at construction —
// its maxModelListEntries, the three string caps, maxModelEffortLevel and
// maxModelEffortLevelCount — before anything is retained or written, and those
// constants are unexported there. Re-applying them here would mean either exporting
// six constants or spelling them a second time in this package, and a second spelling
// of a bound is a second place it can disagree; that is the argument
// `resolveBoundModelList` makes about its own refusal rules. So the file is read as
// DAEMON-WRITTEN and this cap bounds only the aggregate.
//
// The number: the producer's worst case is maxModelListEntries (10) entries of
// maxModelResolved + maxModelValue + maxModelDisplayName + maxModelEffortLevelCount ×
// maxModelEffortLevel = 256 + 256 + 256 + 8 × 32 = 1024 bytes of claude-derived text,
// so about 10 KiB. 64 KiB is roughly six times that, covers JSON structure and key
// names, and keeps a restored frame the same order of magnitude as a live one.
const maxModelVocabularyFile = 64 << 10

// modelVocabularyFile is the on-disk shape: the daemon-wide list and NOTHING else —
// no conversation id, no session id, no timestamp, no provenance.
//
// It has its own record types rather than marshalling turnevent.ModelList directly,
// because the on-disk format is a contract of its own and must not follow an internal
// type's field renames. protocol.ModelOption is equally unsuitable in the other
// direction: it is conversation-scoped and its MarshalJSON normalises nil to [], which
// is a WIRE collapse this file has no reason to bake in.
type modelVocabularyFile struct {
	Models []modelVocabularyOption `json:"models"`
	// DroppedModels rides through so a truncated list does not come back reading as a
	// complete one. `resolveBoundModelList` forbids recomputing it from len(Models);
	// this is the same rule at the other end of the round trip.
	DroppedModels int `json:"dropped_models,omitempty"`
}

// modelVocabularyOption is turnevent.ModelOption's six fields, all of them. The five
// claude-authored ones carry the menu; TruncatedFields is how a client learns an entry's
// text was cut, so dropping it would turn a cut entry into one that reads as whole.
type modelVocabularyOption struct {
	ResolvedModel    string   `json:"resolved_model"`
	Value            string   `json:"value"`
	DisplayName      string   `json:"display_name"`
	EffortLevels     []string `json:"effort_levels,omitempty"`
	SupportsAutoMode bool     `json:"supports_auto_mode,omitempty"`
	TruncatedFields  []string `json:"truncated_fields,omitempty"`
}

// newModelVocabularyStore mints a store for path. It performs NO I/O: the read is
// Load's, once at start, and the first write is a retention's.
func newModelVocabularyStore(path string) *modelVocabularyStore {
	return &modelVocabularyStore{path: path}
}

// Load reads the file ONCE, at daemon start, and is the only read this type ever
// does — never on the request path (#2450 AC 5), so an inbound request_model_list
// costs no syscall and no decode.
//
// EVERY failure answers the same way: nothing retained, and the daemon starts. Absent,
// unreadable, past the cap, undecodable, and a list carrying no models are five
// situations with one answer, and the last is a CONTRACT check rather than a bound —
// turnevent.ModelList.Models is documented "Never empty", so a list with no models is
// not a value any reader may be handed and cannot stand in for the unreported state.
// os.IsNotExist is deliberately not special-cased: a cold daemon and a corrupt file are
// the same amount of vocabulary.
//
// Nil-receiver-safe, so a daemon built without a store needs no guard at the call site.
func (s *modelVocabularyStore) Load() {
	if s == nil {
		return
	}
	f, err := os.Open(s.path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	// One byte past the cap is read so an oversized file is DETECTED rather than
	// silently truncated into a valid-looking prefix.
	body, err := io.ReadAll(io.LimitReader(f, maxModelVocabularyFile+1))
	if err != nil || len(body) > maxModelVocabularyFile {
		return
	}
	var file modelVocabularyFile
	if err := json.Unmarshal(body, &file); err != nil {
		return
	}
	if len(file.Models) == 0 {
		return
	}
	list := turnevent.ModelList{
		Models:        make([]turnevent.ModelOption, 0, len(file.Models)),
		DroppedModels: file.DroppedModels,
	}
	for _, m := range file.Models {
		list.Models = append(list.Models, turnevent.ModelOption{
			ResolvedModel:    m.ResolvedModel,
			Value:            m.Value,
			DisplayName:      m.DisplayName,
			EffortLevels:     nilIfEmpty(m.EffortLevels),
			SupportsAutoMode: m.SupportsAutoMode,
			TruncatedFields:  nilIfEmpty(m.TruncatedFields),
		})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = list
	s.have = true
	// Seed the write-skip comparison with what was just read, so a child re-reporting
	// the menu this daemon restored writes nothing.
	s.lastWritten = body
}

// ModelList returns the last vocabulary this daemon saw and true, or the zero value
// and false when it has seen none. It is the SAME comma-ok contract
// `sessionModelHold.ModelList` answers, deliberately: that is the one-method interface
// `sessionRetainedModelList` asserts for, so the third source is the same shape as the
// first two rather than a new one, and the bool stays the only spelling of "no list".
//
// The returned list is a DEEP copy the caller solely owns, for the hold's reason and
// more so: this value lives for the whole process and is read repeatedly by different
// consumers.
//
// Nil-receiver-safe, mirroring the hold: a daemon with no store is two sources, not a
// special case at every arm.
func (s *modelVocabularyStore) ModelList() (turnevent.ModelList, bool) {
	if s == nil {
		return turnevent.ModelList{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.have {
		return turnevent.ModelList{}, false
	}
	return cloneModelList(s.list), true
}

// Retain records a vocabulary a child just reported and schedules it for the file.
//
// IT NEVER FAILS, BLOCKS OR DELAYS THE EVENT PATH (#2450 AC 1), and that is the whole
// reason the write is asynchronous. This runs on claude's stdout forwarder goroutine —
// the goroutine that produces every later event of the session — so an fsync and a
// rename here would stall the parse of whatever claude says next. The mutex is a LEAF
// and is never held across the goroutine start or across any I/O, so this adds no edge
// to the daemon's lock order; it participates in no ordering with Pool.mu,
// Session.lcMu or capMu.
//
// SINGLE-FLIGHT AND COALESCING. At most one writer goroutine exists per store, and it
// drains until the value is clean, so a burst of spawns costs one write per drain
// rather than one per event and the value that lands is always the newest. A retention
// arriving while a write is in flight therefore supersedes it rather than queueing
// behind it — correct, because a vocabulary is a snapshot and no consumer wants the
// older one.
//
// It stores its OWN COPY. `sessionModelHold.Sink` takes what it is handed without
// copying and documents itself as sole owner of it; with a second retainer on the same
// chain that sentence would be true of neither, so this one clones and the hold's claim
// stays true. Once stored the value is replaced by assignment and never mutated.
//
// Nil-receiver-safe for the reason ModelList is.
func (s *modelVocabularyStore) Retain(list turnevent.ModelList) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.list = cloneModelList(list)
	s.have = true
	s.dirty = true
	if s.closed || s.writing {
		s.mu.Unlock()
		return
	}
	s.writing = true
	s.wg.Add(1)
	s.mu.Unlock()
	go s.drain()
}

// Close stops scheduling further writes and joins the writer goroutine. It is the
// store's shutdown path — the reason the goroutine above cannot leak — and the
// deterministic join point tests use instead of polling for the file.
//
// It takes no context and does no I/O of its own: what it waits for is one bounded file
// write, already in flight.
//
// Nil-receiver-safe, and safe to call twice.
func (s *modelVocabularyStore) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.wg.Wait()
}

// drain is the writer goroutine. It writes the newest value until nothing is dirty,
// then clears `writing` and returns — that self-termination IS its shutdown path, and
// it is why this goroutine needs no context: its work is one bounded file write, not a
// loop waiting on the world.
//
// The snapshot is taken UNDER the mutex and encoded OUTSIDE it, which is safe because
// `list` is replaced by assignment and never mutated in place, so the slices a snapshot
// shares are immutable for as long as it holds them.
//
// EVERY ERROR IS DISCARDED — no return value, no retry, no record. A daemon that cannot
// write this file keeps serving live vocabularies exactly as it does today, which is
// AC 1's "a write failure never fails the event path" in its strongest form: there is no
// path by which a failure can be observed. See the type's doc for why there is no log
// line either.
func (s *modelVocabularyStore) drain() {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		if !s.dirty {
			s.writing = false
			s.mu.Unlock()
			return
		}
		s.dirty = false
		snapshot := s.list
		previous := s.lastWritten
		s.mu.Unlock()

		body, err := encodeModelVocabulary(snapshot)
		if err != nil {
			continue
		}
		if bytes.Equal(body, previous) {
			continue
		}
		if err := writeModelVocabularyFile(s.path, body); err != nil {
			continue
		}
		s.mu.Lock()
		s.lastWritten = body
		s.mu.Unlock()
	}
}

// sinkFor mints this store's per-runner sink decorator: `streamTurnSink.sinkFor`'s
// shape — a per-daemon object handing out a closure bound to one runner's downstream —
// and `sessionModelHold.Sink`'s behaviour.
//
// IT RETAINS NOTHING OF ITS OWN and forwards EVERY event of EVERY variant unchanged,
// ModelList included: swallowing one here would change what the fan-in and the drain
// observe, which this ticket has no reason to do. A nil next forwards nothing — the
// test convenience the hold chain already offers.
//
// WHERE IT SITS IN THE CHAIN IS IMMATERIAL; that it sits on the PARSER'S SIDE of the
// channel is not. `turnMarkFor` answers turnMarkNone for ModelList, so `sinkFor`'s
// downstream refuses it at droppableCap under load, and one initialize exchange per
// child produces exactly one of these with no later event to replace it. A persist
// point below the fan-in send could lose the list for the whole life of the child —
// `sessionModelHold`'s own argument, and the reason `newStreamRunnerFactory` chains this
// beside the two existing non-retaining decorators rather than anywhere downstream.
func (s *modelVocabularyStore) sinkFor(next func(turnevent.Event)) func(turnevent.Event) {
	return func(ev turnevent.Event) {
		if list, ok := ev.(turnevent.ModelList); ok {
			s.Retain(list)
		}
		if next != nil {
			next(ev)
		}
	}
}

// encodeModelVocabulary renders a list as the file's body. Separated from the write so
// the write-skip comparison above can be made on the ENCODED BYTES, which is the one
// comparison that is exact without a deep-equal walk and is computed on the way to the
// write regardless.
func encodeModelVocabulary(list turnevent.ModelList) ([]byte, error) {
	file := modelVocabularyFile{
		Models:        make([]modelVocabularyOption, 0, len(list.Models)),
		DroppedModels: list.DroppedModels,
	}
	for _, m := range list.Models {
		file.Models = append(file.Models, modelVocabularyOption{
			ResolvedModel:    m.ResolvedModel,
			Value:            m.Value,
			DisplayName:      m.DisplayName,
			EffortLevels:     m.EffortLevels,
			SupportsAutoMode: m.SupportsAutoMode,
			TruncatedFields:  m.TruncatedFields,
		})
	}
	body, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// writeModelVocabularyFile commits body to path by CODING-STYLE's persistent-data
// recipe, which is `conversations.Registry.Save`'s verbatim: temp file in the
// DESTINATION directory, mode 0600, sync, close, rename. Rename is the commit point, so
// a process killed mid-write leaves the previous file intact rather than a truncated
// one.
//
// The parent directory is created 0700 when absent, and that is reachable rather than
// defensive: `writeMCPSettings` documents that the instance data dir is created by the
// pool's registry save, which need not have run by the first write.
//
// The scratch pattern is dotted so a file left by a SIGKILL inside the write cannot be
// mistaken for the real name, matching `.conversations-*.json.tmp` and
// `.sessions-*.json.tmp`.
func writeModelVocabularyFile(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".model-list-*.json.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// nilIfEmpty collapses a zero-length decoded slice to nil, preserving turnevent's
// convention that these fields are nil when there is nothing to report and never an
// empty non-nil slice (see turnevent.ModelOption.EffortLevels, where an absent key, a
// JSON null and a published empty array are ONE reading spelled nil). Without it a "[]"
// in the file would reach a consumer as a shape the producer never emits.
func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
