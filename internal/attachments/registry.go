package attachments

import "sync"

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

// Registry holds the attachment uploads currently in flight, one *Accumulator
// per conn-and-attachment_id pair, and SYNCHRONISES ITSELF rather than
// documenting a caller obligation the way Accumulator does.
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
// CALLS ANOTHER THAT TAKES IT, because sync.Mutex is not reentrant: the five
// locked methods — Admit, insert, Lookup, Release and count — take mu for their
// whole body and never call one another. insertLocked is the one method here
// that takes no lock, which is what lets Admit and insert run ONE critical
// section rather than two copies free to drift; the Locked suffix is this
// repo's signal for a caller-holds-the-lock body, as in sessions' saveLocked.
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
type Registry struct {
	mu      sync.Mutex
	uploads map[uploadKey]*Accumulator
}

// NewRegistry returns an empty Registry ready for use.
func NewRegistry() *Registry {
	return &Registry{uploads: make(map[uploadKey]*Accumulator)}
}

// insertLocked stores a as the upload in flight for the pair and answers
// (a, true, nil); or — when the pair is already held — stores NOTHING and
// answers the incumbent with false; or — when the registry is already at
// maxInFlightUploads — stores nothing and answers capacityRefusal. Caller MUST
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
	key := uploadKey{connID: connID, attachmentID: attachmentID}
	if held, ok := r.uploads[key]; ok {
		return held, false, nil
	}
	// len(r.uploads) DIRECTLY, never r.count(): that one takes mu, this body
	// runs with mu held, and sync.Mutex is not reentrant — the call would
	// deadlock rather than race. >= and not ==, which is equivalent only
	// because this gate runs ahead of every store in the package; the safe form
	// is written anyway, the same discipline Add's subtraction records.
	if len(r.uploads) >= maxInFlightUploads {
		return nil, false, capacityRefusal(len(r.uploads))
	}
	r.uploads[key] = a
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
// incumbent look-up, capacity, construction, store — so the entry cap reads the
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
// from map-key membership and never from the stored value, and the bool is what
// #1784 turns into "chunk for an unknown transfer".
func (r *Registry) Lookup(connID, attachmentID string) (*Accumulator, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.uploads[uploadKey{connID: connID, attachmentID: attachmentID}]
	return a, ok
}

// Release removes exactly the pair's entry, leaving another conn's transfer of
// the same attachment_id untouched; releasing a pair the registry does not hold
// is a no-op. It returns nothing because #1784 releases what it just looked up
// and #1742 decides only when to call it, so a presence answer would invite a
// caller to branch on it outside the lock.
func (r *Registry) Release(connID, attachmentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.uploads, uploadKey{connID: connID, attachmentID: attachmentID})
}

// count is how many uploads are in flight, which is exactly the set of live
// admitted uploads: no refusal leaves an entry, and every insertion path in
// this package runs the maxInFlightUploads gate, so this never exceeds it.
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
