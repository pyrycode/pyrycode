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
// #1781's, and what bounds the attacker-influenced half of these bytes is the
// entry cap (#1786), not anything here.
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
// were fresh, and it is the exact shape the count-then-admit gate (#1786) cannot
// be built on, whose "the registry holds exactly the uploads it held before the
// chunk arrived" needs look-up-then-count-then-insert to be indivisible. The
// precedent is sessions' Pool.capMu, whose doc names the sequence it serializes
// so a later caller re-uses it rather than wrapping it; the sequence this one
// serializes is check-the-pair-then-write-the-map.
//
// mu is a LEAF: never held across a call into Accumulator — not Add, not
// Assemble — and never nested with another lock. NO METHOD OF Registry CALLS
// ANOTHER, because sync.Mutex is not reentrant and all four take mu for their
// whole body; a composing method inlines what it needs under one acquisition
// instead. #1786's gate is the concrete case — count followed by insert
// deadlocks, and "fixing" that by unlocking between them is the split lock
// above.
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

// insert stores a as the upload in flight for the pair and answers (a, true),
// or — when the pair is already held — stores NOTHING and answers the incumbent
// with false. Never replacing is a security property rather than hygiene: a
// second first-chunk for a live pair, declaring a different size or sha256, has
// its freshly built accumulator dropped instead of installed, so the numbers a
// transfer was admitted under cannot be swapped underneath the bytes it already
// holds, and the chunks in flight are not dropped on the floor.
//
// inserted is the INVERSE of sync.Map.LoadOrStore's loaded: true means FRESH. A
// caller that ports the intuition and inverts the branch double-admits.
//
// PRECONDITION: a is non-nil, unguarded. insert is unexported because #1788's
// admission entry point, which runs CheckDeclaration and CheckDeclaredSize
// before constructing an Accumulator, is meant to be the only exported way an
// upload enters — exporting the raw insert would ship a bypass around admission
// that #1788 then has to un-ship. That one caller constructs the value it
// passes.
func (r *Registry) insert(connID, attachmentID string, a *Accumulator) (upload *Accumulator, inserted bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := uploadKey{connID: connID, attachmentID: attachmentID}
	if held, ok := r.uploads[key]; ok {
		return held, false
	}
	r.uploads[key] = a
	return a, true
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

// count is how many uploads are in flight. Unexported because in-package tests
// are its only reader: an exported one would publish exactly the TOCTOU shape
// #1786 must not be built on, a cap check reading the count under one
// acquisition and admitting under another.
func (r *Registry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.uploads)
}
