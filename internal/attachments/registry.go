package attachments

import (
	"errors"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// uploadKey identifies one in-flight upload by the conn its chunks arrived on
// TOGETHER WITH the attachment_id the transfer declared, never by the id alone.
// docs/protocol-mobile.md § Attachments states that attachment_id is "Not a
// capability — not secret, not unguessable, and never the only thing standing
// between a caller and a file"; keyed by it alone it would be exactly that,
// since two conns colliding on one id would share one *Accumulator, which
// carries no mutex by design — a data race AND a path for one conn's bytes into
// another conn's file. A phone that drops mid-upload and reconnects re-sends the
// same id while its old session may not have torn down, so the collision is
// routine rather than hypothetical. A struct and not a concatenated string,
// which would reintroduce the collision it closes ("ab"+"c" and "a"+"bc" are one
// key) and make this package a parser of two values it stores and compares and
// deliberately parses neither of.
//
// MEMBERSHIP CERTIFIES NOTHING. That a pair is held says only that something
// inserted it — not that the id is canonical, bounded, or safe to make a path
// component. The canonical-shape check that must run before it becomes one is
// #1781's, and what bounds the attacker-influenced half of these bytes is
// maxInFlightUploads, not anything here.
type uploadKey struct {
	connID       string
	attachmentID string
}

// entry is one in-flight upload: the pair's accumulator TOGETHER WITH the time
// a chunk last arrived for it. lastChunkAt is set when the pair is admitted and
// moved forward by every chunk Deliver routes to it, and by NOTHING ELSE — not a
// Lookup, not a diagnostic, not a repeat admission under a pair already held.
//
// The field is named for the event and not "last activity", because the name is
// the guard rail: "activity" invites a later reader to stamp on a look-up, and a
// read that moved the time would let any diagnostic or dispatch-site look-up
// keep a dead upload alive indefinitely — re-opening at this layer the
// slot-exhaustion path uploadIdleTimeout closes. That window makes the hazard
// MORE live rather than less: the stamp is now the reap's only input, so a
// stamping read would not merely blur a diagnostic, it would defeat expiry.
//
// Named entry and not upload because upload is already a named return in
// insertLocked and insert and a local in Admit and Deliver, so a package-level
// type by that name would be shadowed at the very store site that needs it.
type entry struct {
	acc         *Accumulator
	lastChunkAt time.Time
}

// Registry holds the attachment uploads currently in flight, one entry per
// conn-and-attachment_id pair — that pair's *Accumulator together with the time
// a chunk last arrived for it — and SYNCHRONISES ITSELF rather than documenting
// a caller obligation the way Accumulator does.
//
// The map's value is a struct held BY VALUE rather than a *entry. A pointer
// would let any in-package caller keep the entry after a look-up returned and
// read or write lastChunkAt with mu released; a value forecloses that
// structurally, since the stamp can then only change by storing into the map and
// the map can only be written under mu. That is uploadKey's own argument for
// being a struct rather than a concatenated string — foreclose it in the type
// instead of documenting a caller obligation — and the cost is one small copy
// per map read, bounded by maxInFlightUploads entries.
//
// mu is taken once per operation and held for that whole operation, never once
// to look up and again to write. That is more than race-freedom: a registry
// splitting the two still tells two concurrent inserts of one pair that both
// were fresh, and it is the exact shape the count-then-admit gate (#1796)
// cannot be built on, whose "the registry holds exactly the uploads it held
// before the chunk arrived" needs look-up-then-count-then-insert to be
// indivisible. The precedent is sessions' Pool.capMu, whose doc names the
// sequence it serializes so a later caller re-uses it rather than wrapping it;
// the sequence this one serializes is
// check-the-declaration-then-check-the-pair-then-write-the-map, and the
// maxInFlightUploads gate re-uses that one acquisition rather than wrapping it
// in a second lock of its own.
//
// mu is a LEAF: never held across a call into Accumulator — not Add, not
// Assemble — and never nested with another lock. NO METHOD TAKES mu AND THEN
// CALLS ANOTHER THAT TAKES IT, because sync.Mutex is not reentrant: the seven
// locked methods — Admit, insert, Lookup, lookupAndStamp, Release, count and
// lastChunkAt — take mu for their whole body and never call one another. THREE
// methods here take no lock, and the third is a second member of the first of
// the two OPPOSITE reasons rather than a third reason. insertLocked and
// reapExpiredLocked both run with mu held BY THEIR CALLERS: that is what lets
// Admit and insert run ONE critical section rather than two copies free to
// drift, and what lets the reap share the acquisition insertLocked's capacity
// gate and lookupAndStamp's map read already hold, which a reap taking mu itself
// could not. The Locked suffix is this repo's signal for a caller-holds-the-lock
// body, as in sessions' saveLocked. Deliver
// holds mu at NO POINT: it composes lookupAndStamp and Release, each of which
// takes it for its own whole body, and feeds the accumulator between them
// off-lock. That composer breaches nothing above —
// "no method takes mu and then calls another that takes it" is satisfied by a
// method that takes it never — and it is what keeps the feed off-lock while the
// entry accounting stays inside this type.
// The maxInFlightUploads gate keeps it that way: a count read under one
// acquisition followed by an insert under another does not deadlock, it is the
// split lock above, which is why the enforcing check lives inside the section
// Admit already holds and reads len(uploads) directly rather than calling
// count.
//
// Feeding happens OFF-LOCK: the caller takes the *Accumulator back and feeds it
// with mu released. That is sound BECAUSE the key carries the conn — relay
// spawns exactly one appFrameWorker per session, whose own doc records that no
// two handlers for one conn run concurrently — so exactly one goroutine can ever
// reach any one accumulator, which preserves Accumulator's mutex-free contract
// rather than quietly widening it.
//
// THE CLOCK IS A SEAM, and it is read WITH mu HELD at every site: the admission
// store in insertLocked, the delivery stamp in lookupAndStamp, and once per pass
// in reapExpiredLocked, which each of those two bodies runs inside its own
// acquisition. So the reading and the store it lands in are one critical section
// and two stampers of one pair cannot commit out of order. That is the type's only call out to a
// caller-supplied function under its only mutex, and the three constraints it
// puts on that function are stated where one can be supplied:
// newRegistryWithClock. The single lock order is Registry.mu → whatever the
// clock locks, never the reverse, which is a total order only because the clock
// never calls back in. The stamp itself is never read off-lock, and holding the
// map value by value rather than by pointer is what makes that a property of the
// type instead of a caller obligation.
type Registry struct {
	mu      sync.Mutex
	uploads map[uploadKey]entry

	// now is the clock every stamp is read from, never nil after construction:
	// newRegistryWithClock substitutes time.Now. Assigned once there and never
	// written again, which is why reading it needs no separate rule.
	now func() time.Time
}

// NewRegistry returns an empty Registry reading the REAL WALL CLOCK, and is the
// only construction path outside this package's tests. The clock-taking way in
// is newRegistryWithClock, which this delegates to with a nil clock rather than
// building a Registry of its own, so the nil substitution exists in one place.
func NewRegistry() *Registry {
	return newRegistryWithClock(nil)
}

// newRegistryWithClock returns an empty Registry reading now as its clock,
// substituting time.Now for a nil one. That nil-tolerance is the whole reason
// the clock is not a parameter of NewRegistry: production supplies nothing and
// every existing construction site is left untouched, which is the shape
// streamsup's newStallTracker and streamjson's emitter constructor already take
// for the same seam.
//
// Unexported because every site that would supply a clock is a test in this
// package; count is the precedent for that. Exporting it would publish a seam
// with no production caller and let a caller outside this package install a
// clock this type's own invariants read.
//
// THREE CONSTRAINTS ON now, because it is called with mu held. It must not
// block, or it stalls every conn's admissions and deliveries behind it. It must
// not call back into the Registry: mu is not reentrant, so a callback reaching
// any locked method deadlocks rather than races. And it must be safe for
// concurrent use, because mu serialises only the calls THIS type makes — a test
// advancing the same clock from another goroutine does not go through mu.
// time.Now satisfies all three.
func newRegistryWithClock(now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{uploads: make(map[uploadKey]entry), now: now}
}

// reapExpiredLocked deletes every entry whose last chunk arrived longer ago than
// uploadIdleTimeout and DELETES NOTHING ELSE. Caller MUST hold r.mu. It is the
// whole of this package's expiry: there is no goroutine, no ticker and no
// shutdown path, and the window's own doc says so where a reader meets the name.
//
// THE COMPARISON IS STRICT. An entry idle for EXACTLY the window is still in
// flight, because the window bounds how long silence may LAST rather than how
// long it may be approached — the polarity CheckDeclaredSize argues for its own
// >.
//
// ONE CLOCK READING FOR THE WHOLE PASS, not one per entry, for two reasons. It
// keeps this to a single call out to the caller-supplied now under mu, which
// Registry's clock paragraph accounts for. And it makes every entry answer
// against ONE instant, so the outcome cannot depend on Go's randomised map
// iteration order — a per-entry read against an advancing clock could reap A and
// spare B on one ordering and the reverse on another. Deleting from a map while
// ranging over it is defined: an entry deleted before the iteration reaches it
// is simply not produced, so one pass suffices and no key-collection slice is
// needed.
//
// The Locked suffix is this repo's caller-holds-the-lock signal, as in
// insertLocked and sessions' saveLocked, and taking no lock is what lets the two
// bodies that call this share their own single acquisition with it. It is the
// THIRD method here that takes none — see Registry's type doc, which names all
// three and the opposite reasons.
//
// IT CALLS NOTHING ON Accumulator, and never reject. Dropping the map entry
// drops the registry's last reference and the bytes become collectable; reject
// is an UNLOCKED mutator on a type that carries no mutex by design, and mu does
// not cover an accumulator — a pointer to one may already be held off-lock by a
// goroutine that took it from Admit or Lookup, so a reject from here would be a
// data race and not merely a leaf breach. That is the rule Deliver's doc states
// from the other side, and this method must not be the first to break it.
//
// IT CALLS NOTHING ON Registry EITHER, Release included. Release takes mu and
// sync.Mutex is not reentrant, so building this on it would deadlock rather than
// race; it deletes directly, exactly as Release does under its own acquisition.
//
// IT IS SILENT, deliberately. The map key holds the client-chosen attachment_id,
// one of the four strings protocol.AttachmentChunkPayload's SECURITY block bans
// from ever reaching a log or an error string, so there is no logger and no
// format string in this body — the rule stays structural here rather than a
// discipline. It returns nothing for the same posture: nothing needs a count,
// and a return value would invite a caller to branch on it.
//
// REMOVE-ONLY IS WHAT MAKES A MID-DELIVER REAP BENIGN. A goroutine sitting
// between Deliver's look-up and its release holds a pointer this may drop from
// the map; its Assemble still returns integrity-verified bytes or none, and its
// Release is a no-op on an already-absent key. That rests on the one-goroutine-
// per-conn precondition Admit and Deliver already hand accumulators back under,
// because that late Release deletes BY KEY and would take a successor's entry
// had the pair been re-admitted in the gap. This does not weaken that
// precondition, but it does introduce a THIRD PARTY — another conn's Admit — that
// can now remove your entry where before only your own conn could, so a later
// change that widens the one-appFrameWorker-per-conn assumption must revisit it
// here.
func (r *Registry) reapExpiredLocked() {
	now := r.now()
	for key, u := range r.uploads {
		if now.Sub(u.lastChunkAt) > uploadIdleTimeout {
			delete(r.uploads, key)
		}
	}
}

// insertLocked stores a as the upload in flight for the pair, stamped with the
// clock's reading, and answers (a, true, nil); or — when the pair is already
// held — stores NOTHING and answers the incumbent with false; or — when the
// registry is already at maxInFlightUploads — stores nothing and answers
// capacityRefusal. Caller MUST
// hold r.mu. It takes no lock itself and is the one method here callable with
// mu held, which is what keeps mu a leaf while two methods share this body.
//
// THE CAPACITY GATE IS THE LAST OF THE THREE STEPS THAT CAN REFUSE and sits
// BEHIND the incumbent look-up, which is what exempts a held pair from a
// capacity refusal without any early return in front of Admit's declaration
// checks: a pair already held occupies an entry it already occupies, so it
// reaches its incumbent at capacity like anywhere else.
//
// Never replacing is a security property rather than hygiene: a second
// first-chunk for a live pair, declaring a different size or sha256, has its
// freshly built accumulator dropped instead of installed, so the numbers a
// transfer was admitted under cannot be swapped underneath the bytes it already
// holds, and the chunks in flight are not dropped on the floor.
//
// inserted is the INVERSE of sync.Map.LoadOrStore's loaded: true means FRESH. A
// caller that ports the intuition and inverts the branch double-admits.
//
// IT IS THE ADMISSION STAMP: the entry it stores carries r.now(), read inside
// the acquisition its caller already holds. BOTH REFUSAL BRANCHES RETURN AHEAD
// OF THAT STORE, so NO REFUSAL STAMPS — a repeat under a held pair answers the
// incumbent and leaves that incumbent's lastChunkAt exactly where it was, and a
// capacity refusal writes nothing at all. Admit and insert inherit the stamp
// from this one shared core, which is what keeps an insert-inserted entry from
// carrying a zero stamp that reapExpiredLocked would read as infinitely idle
// and take on the very next pass.
//
// PRECONDITION: a is non-nil, unguarded.
//
// It is a shared core rather than one method's body copied into the other so
// that TestRegistry_ConcurrentSamePairInsert_TellsExactlyOneItIsFresh, which
// drives insert, pins the exact critical section Admit executes. It is also the
// cheaper seam the entry cap (#1796) took: a gate HERE, between the look-up and
// the store, bounds both callers at once for the price of an error return on an
// unexported helper, where a gate in Admit's body alone would leave insert
// ungated. What that buys is that "the registry never holds more than
// maxInFlightUploads" is a property of the CONTAINER rather than of one entry
// point, which is what makes count's meaning uniform.
func (r *Registry) insertLocked(connID, attachmentID string, a *Accumulator) (upload *Accumulator, inserted bool, err error) {
	// AHEAD OF BOTH the incumbent look-up and the capacity gate, and HERE rather
	// than in Admit: this is the shared core, so one placement covers both
	// callers at one reap per call where a reap in Admit as well would run twice
	// per admission. The gate below reads len(r.uploads) inside this same
	// acquisition, and that is a security property rather than tidiness — a reap
	// under one acquisition of mu followed by a capacity check under another is
	// the split-lock shape
	// TestRegistry_ConcurrentAdmitAtTheBound_AdmitsExactlyTheFreeSlots exists to
	// redden, in which two admissions each observe the same reclaimed slot and
	// both take it.
	//
	// A repeat first chunk under an EXPIRED pair is therefore a FRESH admission,
	// the incumbent having been reaped before the look-up runs. That is the right
	// semantics and it agrees with Deliver's "A RE-ADMITTED PAIR IS A NEW
	// TRANSFER": reaping after the look-up would instead hand back a dead
	// accumulator whose every later chunk Add refuses, worse for the client and
	// for the slot alike.
	r.reapExpiredLocked()

	key := uploadKey{connID: connID, attachmentID: attachmentID}
	if held, ok := r.uploads[key]; ok {
		return held.acc, false, nil
	}
	// len(r.uploads) DIRECTLY, never r.count(): that one takes mu, this body
	// runs with mu held, and sync.Mutex is not reentrant — the call would
	// deadlock rather than race. >= and not ==, which is equivalent only
	// because this gate runs ahead of every store in the package; the safe form
	// is written anyway, the same discipline Add's subtraction records.
	if len(r.uploads) >= maxInFlightUploads {
		return nil, false, capacityRefusal(len(r.uploads))
	}
	r.uploads[key] = entry{acc: a, lastChunkAt: r.now()}
	return a, true, nil
}

// insert takes mu for the whole of insertLocked and is otherwise that function:
// the same never-replace answer, the same inverted inserted, the same capacity
// refusal, the same non-nil precondition on a.
//
// Its callers are this package's tests. Admit does not call it — it holds mu
// across a decision wider than one insert and reaches insertLocked directly, and
// calling this one under mu would deadlock rather than race — so this wrapper is
// an in-package insertion path that runs no declaration check. It stays
// unexported for that reason: #1788's admission entry point, which runs
// CheckDeclaration and CheckDeclaredSize before constructing an Accumulator, is
// meant to be the only exported way an upload enters, and exporting the raw
// insert would ship the bypass around admission that Admit exists to close.
func (r *Registry) insert(connID, attachmentID string, a *Accumulator) (upload *Accumulator, inserted bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.insertLocked(connID, attachmentID, a)
}

// Admit is the registry's only exported way in: it runs BOTH declaration checks
// on a first chunk and, only on a nil answer from both, constructs the
// Accumulator from the declared numbers and offers it to insertLocked, which
// stores it unless the pair is held or the registry is at maxInFlightUploads.
// EVERY refusal stores nothing, so the registry is left exactly as it was and
// nothing about the refused pair is remembered — no id is blacklisted, and the
// same attachment_id is admitted as soon as it carries an admissible
// declaration and there is a free slot: immediately for a declaration refusal,
// which never took one, and on the next Release for a capacity refusal.
//
// THE WHOLE DECISION RUNS UNDER ONE ACQUISITION of mu — declaration verdict,
// construction, the idle reap, incumbent look-up, capacity, store, in the order
// the bodies run them — so the entry cap reads the
// count and admits without releasing it in between, which is what a cap needs
// and what a split acquisition cannot give it. mu stays a LEAF: both checks are
// pure and stateless per their own docs, insertLocked takes no lock, and
// NewAccumulator allocates and returns. That last one is bounded work only
// BECAUSE its chunk map takes no capacity hint — a rule stated there on
// independent grounds that this critical section now depends on, since a hint
// read from the attacker-chosen totalChunks would put an attacker-scaled
// allocation inside this package's only mutex. All four return paths release mu
// through the one defer: three of them are refusals a client triggers at will —
// two with a single malformed declaration, one by holding the bound's worth of
// transfers open — and an explicit unlock missed on any would wedge the registry
// for every conn rather than race.
//
// AT CAPACITY NewAccumulator STILL RUNS and its result is dropped, deliberately
// and cheaply. Checking capacity here, ahead of the construction, would have to
// redo insertLocked's incumbent look-up to keep a held pair exempt, reaching
// into r.uploads from a method that does not touch it today; the dropped
// allocation is bounded for the reason above, and the whole decision stays in
// one acquisition.
//
// It CONSTRUCTS NO ERROR OF ITS OWN. Each refusal is another function's error
// verbatim, neither wrapped nor annotated here — a check's, or insertLocked's
// capacityRefusal. That remains the never-log rule
// protocol.AttachmentChunkPayload's SECURITY block states at a function that
// necessarily holds two of the four strings that rule bans: there is no format
// string in this body for attachmentID or sha256 to enter, and the one format
// string the admission path now has lives in capacityRefusal, whose only
// parameter is an int and which therefore cannot see either. The other two,
// Filename and Data, are excluded structurally instead, by taking the
// declaration as scalars the way NewAccumulator and CheckDeclaration do —
// decoding a payload into them is the dispatch site's job (#1744). errors.Is
// therefore reaches ErrInvalidDeclaration and ErrUploadTooLarge unchanged
// through here, and the numbers each check wrapped survive verbatim.
//
// WHICH CHECK RUNS FIRST IS NOT A CONTRACT. A doubly-bad declaration gets
// whichever sentinel runs first, and nothing has decided which that should be;
// CheckDeclaration runs first because it mirrors the file order, so no caller
// and no test may read an order out of it. WHERE THE CONCURRENCY BOUND SITS IS
// A CONTRACT, and it is BEHIND both of them, in insertLocked and behind the
// incumbent look-up: at capacity, a first chunk whose declaration is also
// invalid answers the declaration sentinel, so a client is told the fault that
// will never clear ahead of the one that clears by itself. That is the only
// ordering pinned here — which of the two declaration checks runs first stays
// undecided.
//
// A NIL ERROR CERTIFIES THE TWO NUMBERS AND NOTHING ELSE. Not the
// attachment_id, whose canonical-shape check before it may become a path
// component is EnsureDir's (#1781), and not the declared digest, which is
// copied into the accumulator uninspected and compared only by Assemble.
//
// BOTH CHECKS RUN ON A REPEAT under a pair already held, ahead of the look-up
// that finds the incumbent, and that is a DECISION rather than an accident of
// statement order. A repeat whose declaration disagrees with the live transfer
// is a client error worth naming, so it answers its own sentinel and the
// incumbent is left untouched; handing back the incumbent instead would answer
// an accumulator latched with the FIRST declaration's numbers, which that
// client's next chunk fails to feed anyway. It also keeps the capacity gate
// honest — exempting a held pair from a capacity REFUSAL is not the same thing
// as short-circuiting the checks.
// TestRegistry_AdmitRefusedRepeatUnderAHeldPair_KeepsTheIncumbent pins it.
//
// AN ADMISSIBLE REPEAT answers the INCUMBENT with a nil error and drops the
// accumulator just built, because insertLocked never replaces. The caller then
// feeds the repeat's first chunk into an accumulator latched with the FIRST
// declaration's numbers, so Add refuses it — ErrTotalChunksMismatch when the two
// declarations disagree on the count, ErrDuplicateIndex when they agree — and
// that transfer is rejected. Which is the point: the numbers a transfer was
// admitted under can never be swapped underneath bytes it already holds.
//
// PRECONDITION on what comes back: the accumulator is fed by ONE GOROUTINE AT A
// TIME. Admit releases mu before returning and hands the accumulator back
// off-lock, and Accumulator carries no mutex by design; that is sound because
// the conn is in the key and relay spawns exactly one appFrameWorker per
// session. Two Admit calls for one pair hand back the SAME POINTER, so two
// goroutines feeding one pair is a data race rather than two transfers.
func (r *Registry) Admit(connID, attachmentID string, totalChunks int, size int64, sha256 string) (*Accumulator, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := CheckDeclaration(totalChunks, size); err != nil {
		return nil, err
	}
	if err := CheckDeclaredSize(size); err != nil {
		return nil, err
	}
	// insertLocked's answer, never the accumulator just constructed: on a fresh
	// pair they are the same pointer, and on a repeat returning the constructed
	// one double-admits. insertLocked and not insert, which takes mu.
	upload, _, err := r.insertLocked(connID, attachmentID, NewAccumulator(totalChunks, size, sha256))
	if err != nil {
		return nil, err
	}
	return upload, nil
}

// Lookup answers the transfer in flight under the pair, comma-ok. Presence comes
// from map-key membership and never from the stored value. It is a PURE READ,
// for #1744's dispatch site and for diagnostics; Deliver's own look-up is
// lookupAndStamp, and it is THAT method's bool — not this one — that becomes
// "chunk for an unknown transfer", ErrUnknownUpload.
//
// IT DELIBERATELY DOES NOT STAMP. A look-up that moved the entry's lastChunkAt
// would let a diagnostic, or a dispatch-site read that routes no bytes at all,
// keep a dead upload alive indefinitely — the exact slot-exhaustion path
// uploadIdleTimeout closes, and that window is what makes the hazard load-
// bearing rather than merely untidy, since the stamp is the reap's only input.
// Only a chunk that actually arrived may move that time, which is why the
// stamping look-up is a second method rather than a flag on this one.
//
// IT DOES NOT REAP EITHER, so a true answer may name an entry the next Admit or
// Deliver will reap. That is the same polarity as the paragraph above — a
// look-up that reaped would be a look-up with a side effect — and it is
// consistent with uploadKey's "MEMBERSHIP CERTIFIES NOTHING": the authoritative
// answer to whether a chunk may resume a transfer is Deliver's, which reaps
// before it looks.
func (r *Registry) Lookup(connID, attachmentID string) (*Accumulator, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.uploads[uploadKey{connID: connID, attachmentID: attachmentID}]
	return u.acc, ok
}

// lookupAndStamp answers the pair's accumulator comma-ok and, ON A HIT, moves
// that entry's lastChunkAt to the clock's reading — a chunk has arrived for it.
// ON A MISS IT STORES NOTHING, which is what keeps Deliver's "nothing is stored"
// contract true on the unknown-pair path. It takes mu for its whole body and
// calls no other method that takes it, so the read, the stamp and the store are
// one critical section.
//
// The re-store is what a by-value map entry costs and what it buys: the stamp
// cannot be moved from outside a locked body, which is the property Registry's
// type doc rests the never-read-off-lock claim on.
//
// Its callers are Deliver and this package's tests.
func (r *Registry) lookupAndStamp(connID, attachmentID string) (*Accumulator, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// AHEAD OF THE MAP READ, which is the whole of the placement: a reap that
	// ran after would find the expired entry, stamp it forward and hand its
	// accumulator back — resuming exactly what the window exists to end. It runs
	// INSIDE the acquisition this body already holds, which is what keeps
	// Deliver's "AT MOST TWO acquisitions per delivered chunk" true.
	r.reapExpiredLocked()
	key := uploadKey{connID: connID, attachmentID: attachmentID}
	u, ok := r.uploads[key]
	if !ok {
		return nil, false
	}
	u.lastChunkAt = r.now()
	r.uploads[key] = u
	return u.acc, true
}

// lastChunkAt is when a chunk last arrived for the pair, comma-ok: the clock's
// reading at admission until a chunk is delivered, and the reading at the last
// delivered chunk after that.
//
// Unexported because in-package tests are its only reader — count is the
// precedent — and it is shaped for a single-pair read, which reapExpiredLocked
// has no use for: that sweep ranges r.uploads directly under mu, exactly as this
// accessor reads it, and the two share nothing.
//
// IT DOES NOT REAP, and that is what the tests observing expiry through it rest
// on: a reader that reaped would be causing the change it reports, and
// TestRegistry_AdmitStampsTheClockReading — which advances an hour with nothing
// delivered and then expects the entry present — is the alarm if that ever
// changes. A test asserting expiry drives Admit or Deliver first and reads here
// after.
//
// It reads r.uploads DIRECTLY under mu and must never be built on Lookup or
// lookupAndStamp. That is a testability constraint rather than style: routed
// through Lookup, this accessor would redden for its own reason under the mutant
// that makes Lookup stamp, and the sole-red measurement that pins Lookup's pure
// read would be destroyed.
func (r *Registry) lastChunkAt(connID, attachmentID string) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.uploads[uploadKey{connID: connID, attachmentID: attachmentID}]
	return u.lastChunkAt, ok
}

// Release removes exactly the pair's entry, leaving another conn's transfer of
// the same attachment_id untouched; releasing a pair the registry does not hold
// is a no-op. It returns nothing because Deliver releases what it just looked up
// and #1817, which releases a dropped conn's uploads, decides only when to call
// it, so a presence answer would invite a caller to branch on it outside the
// lock.
//
// THE IDLE REAP IS NOT BUILT ON THIS METHOD and a reader must not expect it to
// be: this one takes mu and sync.Mutex is not reentrant, so a reap running
// inside a body that already holds it would deadlock rather than race.
// reapExpiredLocked deletes directly under its caller's acquisition instead.
func (r *Registry) Release(connID, attachmentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.uploads, uploadKey{connID: connID, attachmentID: attachmentID})
}

// ErrUnknownUpload reports a chunk for a conn-and-attachment_id pair the
// registry does not hold — never admitted, or admitted and already over.
// Deliver is the one place that raises it, BARE rather than wrapped, and callers
// distinguish it with errors.Is rather than by comparing error strings.
//
// It lives HERE, beside the method that raises it, rather than in either
// package-wide var block: accumulator.go's opens "Sentinel errors returned by
// Add and Assemble" and this one is returned by neither, and admission.go's
// three all refuse a DECLARATION at admission, where this one refuses a chunk
// for a transfer that was never admitted or is already released. storage.go is
// the precedent for a sentinel living with its raiser.
//
// ErrIncomplete is the near miss a reader might reach for and is its OPPOSITE:
// that one says a live transfer is waiting for more chunks, this one says there
// is no transfer at all. It is distinct from every other sentinel this package
// publishes, which is a property TestRegistry_DeliverUnheldPair_RefusesAndCreatesNothing
// asserts over the whole set rather than over a sample of it.
//
// WHICH WIRE CODE IT MAPS TO IS #1744's, and no candidate is named here. None of
// the attachment.* codes internal/protocol publishes today means "no live
// transfer under this pair" — CodeAttachmentNotFound is the retrieval leg's, and
// its message is deliberately static — so whether this folds into an existing
// code or wants one of its own is that ticket's decision, and pre-empting it
// here would publish a mapping this package deliberately does not own.
var ErrUnknownUpload = errors.New("attachments: no upload in flight for this conn and attachment_id")

// Deliver routes one chunk to the transfer in flight for its pair and answers
// the transfer's assembled bytes once they are complete and verified. It is the
// counterpart to Admit: Admit takes the slot, this gives it back, so
// maxInFlightUploads bounds LIVE transfers rather than counting a lifetime
// quota. Named for the custodial register the rest of this type uses (Admit,
// Lookup, Release) rather than Add, which is Accumulator's and would be
// ambiguous at every call site across two types in one package.
//
// The steps, in the order the body runs them:
//
//  1. look the pair up AND STAMP IT — a chunk has arrived for it. On a miss,
//     ErrUnknownUpload and NOTHING IS STORED: lookupAndStamp creates no entry
//     and stamps none, and neither does this path.
//  2. feed the accumulator off-lock. On ANY non-nil answer from Add, release the
//     pair and return that error verbatim.
//  3. assemble. Keep the entry when — and ONLY when — the answer is
//     ErrIncomplete, and return it verbatim.
//  4. every other outcome releases, the nil-error one included: the assembled
//     bytes on success, the latched refusal on an integrity mismatch.
//
// THE STAMP SITS AT THE LOOK-UP rather than after Add accepts, and the two are
// observationally identical. The entry survives exactly ONE of the four outcomes
// above — Assemble answering ErrIncomplete — and on that one Add had already
// accepted, so "a chunk arrived" and "a chunk arrived and was accepted" agree
// wherever the stamp can still be read; every other outcome releases the entry
// and takes its stamp with it. Stamping after Add would buy nothing and cost a
// third acquisition of mu.
//
// THE KEEP CASE IS THE ALLOW-LIST AND THAT POLARITY IS THE CONTRACT. Written
// the other way — enumerate the refusals that release — a further latching
// answer added to Assemble later would silently leak a slot per corrupt
// transfer, which is exactly the lockout this method exists to remove. Same
// polarity on the Add leg: every non-nil answer from Add latches, so err != nil
// is the release condition rather than a list of four sentinels.
//
// THE KEY COMES OUT OF THE CHUNK, not from a second parameter beside it. Two ids
// in one call can disagree, and a caller passing the wrong one would route this
// chunk's bytes into a DIFFERENT transfer's accumulator with nothing downstream
// to catch it, since Add reads neither the id nor the declared size or digest.
// One id in play forecloses that structurally, and it is what turns Add's claim
// that a foreign chunk cannot reach it from a caller obligation into a property
// of this package. connID stays a scalar because it is not on the wire: it is
// the receiver's own name for the conn the frame arrived on, and it is the half
// of the key that makes a client-chosen attachment_id safe to key on at all.
//
// ASSEMBLE RUNS AFTER EVERY ACCEPTED CHUNK, not only the last, and that is cheap
// by construction: its completion check answers ErrIncomplete before allocating
// or hashing anything, so only the completing chunk pays for the copy and the
// digest.
//
// It TAKES mu AT NO POINT. lookupAndStamp and Release each take it for their own
// whole body and the accumulator is fed between them with the lock released, so
// AT MOST TWO acquisitions per delivered chunk — one on a miss and one when
// Assemble answers ErrIncomplete, since neither releases; two on every path that
// does release, which is any non-nil answer from Add, an integrity mismatch, and
// the completing chunk. Never three: nothing on this path calls Admit. mu stays
// a LEAF — never held across Add
// or Assemble, which is the rule Registry's type doc states and which this
// method must not be the first to break. The gap between the look-up and the
// release is not a TOCTOU hole: the conn is in the key and relay spawns exactly
// one appFrameWorker per session, so exactly one goroutine can reach any one
// accumulator. That is the same precondition Admit already hands its accumulator
// back under, inherited here rather than re-derived.
//
// It CONSTRUCTS NO ERROR OF ITS OWN: ErrUnknownUpload bare, everything else
// another function's verbatim. There is no format string in this body, which is
// how the never-log rule protocol.AttachmentChunkPayload's SECURITY block states
// stays structural at the one function in this package that holds ALL FOUR of
// the strings that rule bans — AttachmentID, Filename, SHA256 and Data. There is
// nothing to add anyway: the pair is the caller's own two values.
//
// Three consequences worth stating, because each is a question a reader would
// otherwise have to re-derive:
//
//   - reject's LATCH STAYS LOAD-BEARING and must not be touched. Release
//     recovers the ENTRY; reject recovers the MEMORY, at the moment of refusal,
//     and is the only thing that does. A refused accumulator is simply no longer
//     reachable through the registry once this method has released it.
//   - A CHUNK ARRIVING AFTER A REFUSAL ANSWERS ErrUnknownUpload, not the latched
//     sentinel, and that is correct: the latch exists so one LIVE transfer
//     cannot produce two contradictory diagnoses of its own bytes, and after
//     release there is no transfer left to diagnose. "No such transfer" is a
//     statement about the registry, not a second verdict on the bytes.
//   - A RE-ADMITTED PAIR IS A NEW TRANSFER and a straggler from the old one
//     cannot corrupt it. The same attachment_id may be admitted again the moment
//     its slot is free — no blacklist — so a late chunk can land in the
//     successor's accumulator, and because Assemble compares the declared size
//     and digest against the WHOLE assembled slice, the only reachable outcome
//     is that the client's own new transfer fails its own integrity check.
//     Cross-client contamination is foreclosed a level up, by the conn being in
//     the key.
//
// A STALLED UPLOAD EXPIRES, so an entry can vanish between this method's look-up
// and its release — reaped by lookupAndStamp's own head, or by a concurrent
// Admit — and both halves are safe: Release on a pair the registry no longer
// holds is a documented no-op, and a look-up that misses answers
// ErrUnknownUpload, the honest answer for a transfer that has been reaped. The
// window is uploadIdleTimeout, measured from the last chunk this method routed.
func (r *Registry) Deliver(connID string, chunk protocol.AttachmentChunkPayload) ([]byte, error) {
	upload, ok := r.lookupAndStamp(connID, chunk.AttachmentID)
	if !ok {
		return nil, ErrUnknownUpload
	}
	if err := upload.Add(chunk); err != nil {
		r.Release(connID, chunk.AttachmentID)
		return nil, err
	}
	out, err := upload.Assemble()
	if errors.Is(err, ErrIncomplete) {
		return nil, err
	}
	r.Release(connID, chunk.AttachmentID)
	return out, err
}

// count is how many entries the registry holds: no refusal leaves one, and every
// insertion path in this package runs the maxInFlightUploads gate, so this never
// exceeds it.
//
// THAT IS NO LONGER EXACTLY THE SET OF LIVE ADMITTED UPLOADS, since
// uploadIdleTimeout: an upload idle past the window is over but is still counted
// until some Admit or Deliver reaps it, because THIS METHOD DOES NOT REAP. That
// divergence is the right trade rather than an oversight — this accessor is how
// the tests observe the reap, so it must report what the map holds instead of
// causing the change it is measuring. A test asserting expiry drives Admit or
// Deliver first and reads this after.
//
// Unexported because in-package tests are its only reader: an exported one
// would publish exactly the TOCTOU shape the capacity gate must not be built
// on, a cap check reading the count under one acquisition and admitting under
// another. The gate itself reads len(uploads) rather than calling this, for a
// second reason as well — mu is not reentrant, so this call from a body holding
// it deadlocks.
func (r *Registry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.uploads)
}
