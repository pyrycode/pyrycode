package questionbridge

import (
	"crypto/rand"
	"fmt"
	"slices"
	"sync"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Registry is the in-memory store of surfaced-but-unretired question batches,
// keyed by question_batch_id. It is the seam three tickets share without any of
// them owning it: the producer (#1973) records and broadcasts, the answer path
// (#1907) resolves an inbound answer against the daemon's own parked copy
// rather than a client echo, and the connect-time reconcile (#1928) reads
// current truth while minting and retiring nothing.
//
// It carries a sync.Mutex because two real goroutines touch it — the surfacer
// that records and the relay dispatch that looks up and resolves — so it is NOT
// confined to one goroutine by convention. The mutex is a leaf lock: held only
// around O(1) map ops and the clone, never nested with any other lock.
//
// THERE IS NO SEPARATE STORED-ENTRY TYPE, which is the one place
// modalbridge.Registry is deliberately not mirrored. That package needs one
// because its entry and its wire payload genuinely differ (its payload is
// assembled from a rendered prompt and a fail-safe default); here the parked
// thing IS the stamped payload, so a second type would buy a field-by-field
// copy in and a field-by-field rebuild out and nothing else.
type Registry struct {
	mu          sync.Mutex
	outstanding map[string]protocol.QuestionShownPayload
}

// New returns an empty Registry ready for use.
func New() *Registry {
	return &Registry{outstanding: make(map[string]protocol.QuestionShownPayload)}
}

// Record is the single nonce mint site. It mints one fresh question_batch_id,
// stamps it and convID onto p, parks the batch under that id, and returns the
// stamped payload — the frame the producer broadcasts.
//
// BOTH IDS ARE ASSERTED, NEVER ADOPTED. p arrives from Parse, which fills
// neither because both are daemon-owned, but an id that did arrive on p is
// overwritten rather than trusted: p's ultimate source is claude's tool call,
// and a batch parked under a caller-supplied id would be resolvable by whoever
// supplied it. The mint and the write happen in the same call, so a parked
// batch is never Snapshot-able without its scope key.
//
// The only error path is RNG failure: nothing is stored and the zero payload
// comes back, so the caller drops the batch rather than broadcasting an id-less
// one. That branch mirrors modalbridge.Registry.Record's and is untested for
// its reason — crypto/rand.Read does not fail on a supported platform, and an
// injectable RNG seam would be API nothing consumes.
//
// Clone-on-write: the parked copy owns its own questions and options, so what
// this returns — the caller's own slice, stamped — shares no backing storage
// with it at either nesting level.
func (r *Registry) Record(p protocol.QuestionShownPayload, convID string) (protocol.QuestionShownPayload, error) {
	id, err := newQuestionBatchID()
	if err != nil {
		return protocol.QuestionShownPayload{}, err
	}
	p.QuestionBatchID = id
	p.ConversationID = convID

	r.mu.Lock()
	r.outstanding[id] = protocol.QuestionShownPayload{
		ConversationID:  convID,
		QuestionBatchID: id,
		Questions:       cloneQuestions(p.Questions),
	}
	r.mu.Unlock()
	return p, nil
}

// Lookup returns the outstanding batch for batchID without retiring it, so the
// one-shot consume stays Resolve's alone. The returned batch is cloned.
func (r *Registry) Lookup(batchID string) (protocol.QuestionShownPayload, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.outstanding[batchID]
	if !ok {
		return protocol.QuestionShownPayload{}, false
	}
	p.Questions = cloneQuestions(p.Questions)
	return p, true
}

// Resolve returns the batch for batchID and retires it — the one-shot
// consumption that makes "exactly one broadcaster" structural rather than an
// agreement between the answer path (#1907) and the retire backstop (#1973):
// whichever calls Resolve first gets the batch, and every later call reports
// absent. A retired batch resolves nothing (docs/protocol-mobile.md
// § question_dismissed's question_batch_id row).
//
// The read and the delete are one critical section, which is what makes the
// guarantee hold under concurrent callers.
//
// It does NOT clone, unlike Lookup and Snapshot, and the asymmetry is the
// point: the entry is gone by the time this returns, so there is no kept copy
// left for a mutating consumer to reach.
func (r *Registry) Resolve(batchID string) (protocol.QuestionShownPayload, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.outstanding[batchID]
	if ok {
		delete(r.outstanding, batchID)
	}
	return p, ok
}

// Snapshot returns every currently-outstanding batch, each re-emitting the
// nonce and conversation id Record stamped on it. It is the current-truth read
// #1928's connect-time reconcile consumes, so a reconnecting client is scoped
// exactly as the initial broadcast was.
//
// Pure read: it mints nothing, retires nothing, and mutates no registry state —
// an outstanding batch stays Lookup-able and Resolve-able after a call. Each
// batch is cloned. Map-walk order is unspecified; callers reconcile by
// question_batch_id, never by position. An empty registry yields a non-nil,
// zero-length slice.
func (r *Registry) Snapshot() []protocol.QuestionShownPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]protocol.QuestionShownPayload, 0, len(r.outstanding))
	for _, p := range r.outstanding {
		p.Questions = cloneQuestions(p.Questions)
		out = append(out, p)
	}
	return out
}

// cloneQuestions deep-copies a batch's questions ONE LEVEL DEEPER than a plain
// slices.Clone, and that depth is the whole reason it exists. Cloning the
// questions slice alone copies the Question structs but leaves every
// Question.Options pointing at the stored backing array, so the shallow clone
// that fully isolates modalbridge's flat []protocol.ModalOption isolates
// nothing here: a consumer mutating a returned question's option would reach
// the parked batch. A nil slice clones to nil, which
// QuestionShownPayload.MarshalJSON normalises to [] on the wire.
func cloneQuestions(qs []protocol.Question) []protocol.Question {
	out := slices.Clone(qs)
	for i := range out {
		out[i].Options = slices.Clone(out[i].Options)
	}
	return out
}

// newQuestionBatchID returns a fresh UUIDv4-shaped nonce drawn from crypto/rand,
// mirroring modalbridge's newModalID and conversations.NewID. 122 bits of
// entropy ⇒ opaque + unguessable, which is the property
// docs/protocol-mobile.md § Question's question_batch_id row publishes and the
// one an inbound answer's server-side resolution rests on. NOT math/rand.
// Returns an error only when the system RNG fails.
func newQuestionBatchID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant RFC 4122
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
