//go:build permission_observe

package permbridge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestObservationQueueContract(t *testing.T) {
	o := NewObservationRecorder()
	a, b := o.NewConnectionIdentity(), o.NewConnectionIdentity()
	if a.Connection == b.Connection || a.Connection == "unknown" {
		t.Fatal("lifetime reused")
	}
	id := o.Identity("conversation", "session", "", "modal", "", "source-envelope")
	id.Connection = a.Connection
	for _, stage := range []string{"queue_modal_shown", "queue_modal_dismissed"} {
		for _, reason := range []string{"admitted", "cancelled", "absent", "byte_ceiling", "hold_transport", "hold_replay", "hold_capacity", "requeued", "quarantined", "dequeued", "discarded"} {
			if !o.Submit(stage, reason, id) {
				t.Fatal(stage, reason)
			}
		}
	}
	hostile := id
	hostile.Envelope = "HOSTILE"
	o.Submit("queue_modal_shown", "admitted", hostile)
	forged := id
	forged.Epoch++
	if o.Submit("queue_modal_shown", "admitted", forged) {
		t.Fatal("forged epoch")
	}
	if o.Submit("queue_modal_shown", "HOSTILE", id) {
		t.Fatal("hostile vocabulary")
	}
	o.Identity("", "", "", "", "", strings.Repeat("x", ObservationMaxIdentityInput+1))
	o.MarkIncomplete()
	s := o.Snapshot()
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "HOSTILE") || !s.Loss["invalid_identity"] || !s.Loss["invalid_vocabulary"] || !s.Loss["identity_oversize"] || !s.Loss["incomplete_coverage"] {
		t.Fatal("contract/loss violation")
	}
	o.mu.Lock()
	if o.Submit("queue_modal_shown", "admitted", id) {
		t.Fatal("stalled admitted")
	}
	if o.NewConnectionIdentity().Connection != "unknown" {
		t.Fatal("stalled lifetime")
	}
	o.MarkIncomplete()
	o.mu.Unlock()
	if !o.Snapshot().Loss["contention"] {
		t.Fatal("silent contention")
	}
}

func TestObservationQueueBounds(t *testing.T) {
	o := NewObservationRecorder()
	for i := 0; i < ObservationMaxIdentities; i++ {
		o.NewConnectionIdentity()
	}
	if o.NewConnectionIdentity().Connection != "unknown" || o.Identity("", "", "", "", "", "source").Envelope != "unknown" {
		t.Fatal("alias budget escaped")
	}
	s := o.Snapshot()
	if s.IdentityBytes != ObservationMaxIdentityBytes || !s.Loss["identity_capacity"] {
		t.Fatal("wrong bounds")
	}
	exhausted := NewObservationRecorder()
	exhausted.next = ObservationMaxCounter
	exhausted.counts["queue_modal_shown"] = ObservationMaxCounter
	if exhausted.NewConnectionIdentity().Connection != "unknown" {
		t.Fatal("lifetime wrapped")
	}
	exhausted.Submit("queue_modal_shown", "admitted", ObservationIdentity{})
	s = exhausted.Snapshot()
	if !s.Loss["identity_exhausted"] || !s.Loss["counter_exhausted"] || s.Counters["queue_modal_shown"] != ObservationMaxCounter {
		t.Fatal("silent exhaustion")
	}
}
