package fakephone

import (
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestDeferredEnvelopeOrderAndIsolation(t *testing.T) {
	var phone, other Client
	phone.DeferEnvelopes([]protocol.Envelope{{ID: 3}})
	phone.DeferEnvelopes([]protocol.Envelope{{ID: 1}, {ID: 2}})
	for _, want := range []uint64{1, 2, 3} {
		env, ok := phone.TakeDeferred()
		if !ok || env.ID != want {
			t.Fatalf("got %d/%t want %d", env.ID, ok, want)
		}
		if _, ok := other.TakeDeferred(); ok {
			t.Fatal("snapshot crossed phones")
		}
	}
	if _, ok := phone.TakeDeferred(); ok {
		t.Fatal("snapshot replayed twice")
	}
}
