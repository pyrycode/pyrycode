//go:build permission_observe

package permbridge

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Observation limits apply per recorder run, including across Reset.
const (
	ObservationMaxRecords       = 1024
	ObservationMaxIdentities    = 512
	ObservationMaxIdentityInput = 1024
	ObservationMaxIdentityBytes = 41 * ObservationMaxIdentities
	ObservationMaxCounter       = uint64(9007199254740991)
)

// ObservationIdentity contains only opaque, kind-prefixed run-scoped aliases.
// Generation zero means unknown; Epoch is the originating observer epoch.
// Connection input must identify a connection lifetime, not a reusable client ID.
// Copy live identities for Submit; numerical fields cannot be edited or restored
// from serialization. The zero value represents wholly unknown numerical identity.
type ObservationIdentity struct {
	Run, Epoch, Generation                                      uint64
	Conversation, Session, Request, Modal, Connection, Envelope string
	issued                                                      [3]uint64
}

// ObservationRecord carries no caller content. Nanoseconds use the monotonic
// elapsed clock since recorder creation. Epoch is the recording epoch.
type ObservationRecord struct {
	Nanoseconds   int64
	Epoch         uint64
	Stage, Reason string
	Identity      ObservationIdentity
}

// ObservationSnapshot is detached and safe to serialize away from approval paths.
// Complete requires ended coverage without any loss, including previous resets.
// Complete evidence establishes recorded stages, never transport or peer receipt.
type ObservationSnapshot struct {
	Schema, Clock                string
	Run, Epoch                   uint64
	Ended, Complete              bool
	IdentityCount, IdentityBytes int
	Records                      []ObservationRecord
	Counters                     map[string]uint64
	Loss                         map[string]bool
}

// ObservationRecorder is opt-in, bounded and content-free. Submit and Identity
// make one TryLock attempt; contention permanently marks evidence incomplete.
// Snapshot, Reset and End may block each other, but never approval operations.
// Stages include registry, bridge, relay and queue_modal_shown/queue_modal_dismissed.
// Reasons: start, end, reset, accepted, empty, duplicate, allow, deny, timeout,
// unknown, seen, sent, rejected, absent, connected, disconnected; queue reasons are
// admitted, cancelled, byte_ceiling, hold_transport, hold_replay, quarantined,
// dequeued and discarded.
// Loss keys: record_capacity, identity_capacity, identity_oversize,
// identity_exhausted, counter_exhausted, contention, reset_truncation,
// incomplete_coverage, closed_submission, invalid_vocabulary, invalid_identity,
// run_exhausted.
type ObservationRecorder struct {
	mu               sync.Mutex
	contention       atomic.Bool
	run, epoch, next uint64
	started          time.Time
	ended            bool
	pending          uint64
	records          []ObservationRecord
	aliases          map[[33]byte]uint64
	counts           map[string]uint64
	loss             map[string]bool
}

var observationRuns atomic.Uint64

// NewObservationRecorder starts coverage; attach before isolated registry work.
// Run epochs are process-local; captures from different processes are separate.
func NewObservationRecorder() *ObservationRecorder {
	o := &ObservationRecorder{epoch: 1, started: time.Now(), aliases: make(map[[33]byte]uint64), counts: make(map[string]uint64), loss: make(map[string]bool)}
	// Bounded allocation attempts; failure cannot mint a colliding run.
	for i := 0; i < 3; i++ {
		n := observationRuns.Load()
		if n == ObservationMaxCounter {
			break
		}
		if observationRuns.CompareAndSwap(n, n+1) {
			o.run = n + 1
			break
		}
	}
	if o.run == 0 {
		o.loss["run_exhausted"] = true
		o.ended = true
	} else {
		o.append("coverage", "start", ObservationIdentity{})
	}
	return o
}

func (o *ObservationRecorder) try() bool {
	if o.mu.TryLock() {
		return true
	}
	o.contention.Store(true)
	return false
}

func observationVocabulary(stage, reason string) bool {
	switch stage {
	case "coverage", "register", "terminal", "bridge", "relay", "modal", "answer", "connection", "queue_modal_shown", "queue_modal_dismissed":
	default:
		return false
	}
	switch reason {
	case "start", "end", "reset", "accepted", "empty", "duplicate", "allow", "deny", "timeout", "unknown", "seen", "sent", "rejected", "absent", "connected", "disconnected", "admitted", "cancelled", "byte_ceiling", "hold_transport", "hold_replay", "quarantined", "dequeued", "discarded":
		return true
	}
	return false
}

// Submit retains a whitelisted stage/reason and recorder-issued identity only.
// It returns false when evidence was discarded. It performs no I/O or callbacks.
func (o *ObservationRecorder) Submit(stage, reason string, id ObservationIdentity) bool {
	if !o.try() {
		return false
	}
	defer o.mu.Unlock()
	if !observationVocabulary(stage, reason) {
		o.loss["invalid_vocabulary"] = true
		return false
	}
	metadata := [3]uint64{id.Run, id.Epoch, id.Generation}
	if metadata != id.issued || id.Run != 0 && id.Run != o.run ||
		id.Run > ObservationMaxCounter || id.Epoch > ObservationMaxCounter || id.Generation > ObservationMaxCounter {
		o.loss["invalid_identity"] = true
		return false
	}
	// Resolve supplied aliases through the private issued vocabulary, preventing
	// callers from using an identity field as an arbitrary content channel.
	for i, a := range []*string{&id.Conversation, &id.Session, &id.Request, &id.Modal, &id.Connection, &id.Envelope} {
		if *a == "unknown" || *a == "" {
			*a = "unknown"
			continue
		}
		valid := false
		for key, issued := range o.aliases {
			if *a == string("csrmle"[i])+":"+strconv.FormatUint(issued, 10) && (key[0] == "csrmle"[i] || i == 4 && key[0] == 'L') {
				valid = true
				break
			}
		}
		if !valid {
			*a = "unknown"
			o.loss["incomplete_coverage"] = true
		}
	}
	return o.append(stage, reason, id)
}

func (o *ObservationRecorder) append(stage, reason string, id ObservationIdentity) bool {
	for _, a := range []*string{&id.Conversation, &id.Session, &id.Request, &id.Modal, &id.Connection, &id.Envelope} {
		if *a == "" {
			*a = "unknown"
		}
	}
	if o.ended {
		o.loss["closed_submission"] = true
		return false
	}
	if o.counts[stage] == ObservationMaxCounter {
		o.loss["counter_exhausted"] = true
	} else {
		o.counts[stage]++
	}
	if len(o.records) == ObservationMaxRecords {
		o.loss["record_capacity"] = true
		return false
	}
	o.records = append(o.records, ObservationRecord{Nanoseconds: time.Since(o.started).Nanoseconds(), Epoch: o.epoch, Stage: stage, Reason: reason, Identity: id})
	return true
}

func (o *ObservationRecorder) number() uint64 {
	if o.run == 0 || o.next == ObservationMaxCounter {
		o.loss["identity_exhausted"] = true
		return 0
	}
	o.next++
	return o.next
}

func (o *ObservationRecorder) alias(kind byte, value string) string {
	if value == "" {
		return "unknown"
	}
	if len(value) > ObservationMaxIdentityInput {
		o.loss["identity_oversize"] = true
		return "unknown"
	}
	key := [33]byte{kind}
	hash := sha256.Sum256([]byte(value))
	copy(key[1:], hash[:])
	if a, ok := o.aliases[key]; ok {
		return string(kind) + ":" + strconv.FormatUint(a, 10)
	}
	if len(o.aliases) == ObservationMaxIdentities {
		o.loss["identity_capacity"] = true
		return "unknown"
	}
	n := o.number()
	if n == 0 {
		return "unknown"
	}
	a := string(kind) + ":" + strconv.FormatUint(n, 10)
	o.aliases[key] = n
	return a
}

func (o *ObservationRecorder) identity(conversation, session, request, modal, connection string) ObservationIdentity {
	return ObservationIdentity{Run: o.run, Epoch: o.epoch, issued: [3]uint64{o.run, o.epoch, 0}, Conversation: o.alias('c', conversation), Session: o.alias('s', session), Request: o.alias('r', request), Modal: o.alias('m', modal), Connection: o.alias('l', connection), Envelope: "unknown"}
}

// Identity aliases bounded identifiers without retaining their verbatim bytes.
// Missing values are unknown. Canonical session and conversation are independent.
// The optional envelope input must identify a known source lifetime/revision,
// never a reusable envelope ID alone; it is aliased with kind e.
func (o *ObservationRecorder) Identity(conversation, session, request, modal, connection string, envelope ...string) ObservationIdentity {
	if !o.try() {
		return ObservationIdentity{Conversation: "unknown", Session: "unknown", Request: "unknown", Modal: "unknown", Connection: "unknown", Envelope: "unknown"}
	}
	defer o.mu.Unlock()
	id := o.identity(conversation, session, request, modal, connection)
	if len(envelope) == 1 {
		id.Envelope = o.alias('e', envelope[0])
	}
	return id
}

// NewConnectionIdentity issues a fresh lifetime alias, independent of routing IDs.
// It shares the bounded alias budget and allocator with Identity.
func (o *ObservationRecorder) NewConnectionIdentity() ObservationIdentity {
	id := ObservationIdentity{Connection: "unknown", Envelope: "unknown"}
	if !o.try() {
		return id
	}
	defer o.mu.Unlock()
	if len(o.aliases) == ObservationMaxIdentities {
		o.loss["identity_capacity"] = true
		return id
	}
	n := o.number()
	if n == 0 {
		return id
	}
	// Domain-separated private keys cannot collide with Identity's SHA-256 keys.
	key := [33]byte{'L'}
	binary.BigEndian.PutUint64(key[1:], n)
	o.aliases[key] = n
	id = o.identity("", "", "", "", "")
	id.Connection = "l:" + strconv.FormatUint(n, 10)
	return id
}

// MarkIncomplete marks attachment gaps without waiting on the recorder.
func (o *ObservationRecorder) MarkIncomplete() {
	if o.try() {
		o.loss["incomplete_coverage"] = true
		o.mu.Unlock()
	}
}

// Reset truncates records and starts a new epoch. Aliases, counts and loss remain
// run-scoped. Pending lifetimes keep their originating epoch and generation.
func (o *ObservationRecorder) Reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.epoch == ObservationMaxCounter {
		o.loss["counter_exhausted"] = true
		return
	}
	if len(o.records) > 0 {
		o.loss["reset_truncation"] = true
	}
	o.records = nil
	o.epoch++
	o.append("coverage", "reset", ObservationIdentity{})
}

// End closes coverage. Pending observed lifetimes make coverage incomplete.
func (o *ObservationRecorder) End() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ended {
		return
	}
	if o.pending != 0 {
		o.loss["incomplete_coverage"] = true
	}
	o.append("coverage", "end", ObservationIdentity{})
	o.ended = true
}

// Snapshot copies retained data; serialization never runs under a recorder lock.
func (o *ObservationRecorder) Snapshot() ObservationSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	s := ObservationSnapshot{Schema: "permission_observe/v1", Clock: "monotonic_elapsed_nanoseconds", Run: o.run, Epoch: o.epoch, Ended: o.ended, IdentityCount: len(o.aliases), IdentityBytes: 41 * len(o.aliases), Records: append([]ObservationRecord{}, o.records...), Counters: make(map[string]uint64), Loss: make(map[string]bool)}
	for k, v := range o.counts {
		s.Counters[k] = v
	}
	for k, v := range o.loss {
		s.Loss[k] = v
	}
	if o.contention.Load() {
		s.Loss["contention"] = true
	}
	s.Complete = s.Ended && len(s.Loss) == 0
	return s
}

type observationState struct{ recorder *ObservationRecorder }
type observationLifetime struct {
	recorder *ObservationRecorder
	id       ObservationIdentity
}

// Observe installs observation for future registrations. Existing lifetimes
// retain their recorder. nil disables future registration observation.
func (r *Registry) Observe(o *ObservationRecorder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.observation.recorder != nil && r.observation.recorder != o {
		r.observation.markIncomplete()
	}
	r.observation.recorder = o
	if len(r.pending) > 0 {
		r.observation.markIncomplete()
	}
}
func (s *observationState) markIncomplete() {
	if o := s.recorder; o != nil && o.try() {
		o.loss["incomplete_coverage"] = true
		o.mu.Unlock()
	}
}
func (s *observationState) register(id string, req Request, reason string) observationLifetime {
	o := s.recorder
	if o == nil || !o.try() {
		return observationLifetime{}
	}
	defer o.mu.Unlock()
	identity := o.identity("", req.SessionID, id, "", "")
	if reason == "accepted" && !o.ended {
		identity.Generation = o.number()
		identity.issued[2] = identity.Generation
	}
	if !o.append("register", reason, identity) || identity.Generation == 0 {
		return observationLifetime{}
	}
	o.pending++
	return observationLifetime{o, identity}
}
func (l observationLifetime) terminal(v Verdict, expired bool) {
	o := l.recorder
	if o == nil || !o.try() {
		return
	}
	defer o.mu.Unlock()
	if o.pending > 0 {
		o.pending--
	}
	reason := "unknown"
	if expired {
		reason = "timeout"
	} else if v.Behavior == BehaviorAllow {
		reason = "allow"
	} else if v.Behavior == BehaviorDeny {
		reason = "deny"
	}
	o.append("terminal", reason, l.id)
}

// ObservationIdentity returns the originating registration identity, including
// across resets or recorder replacement. Zero generation indicates lost evidence.
func (p *Pending) ObservationIdentity() ObservationIdentity {
	id := p.observation.id
	for _, a := range []*string{&id.Conversation, &id.Session, &id.Request, &id.Modal, &id.Connection, &id.Envelope} {
		if *a == "" {
			*a = "unknown"
		}
	}
	return id
}
