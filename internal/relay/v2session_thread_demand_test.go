package relay

import (
	"context"
	"testing"
)

func TestThreadDemandTracksOnlyLiveQueryOwners(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	in := &hostedInbox{ctx: ctx, threadJoins: map[string]threadJoin{"joined": {epoch: "epoch", version: 4}}, query: &threadQuery{conversation: "pending"}}
	m := &V2SessionManager{hostedInboxes: map[string]*hostedInbox{"phone": in}}
	if !m.HasThreadDemand(t.Context(), "joined") || !m.HasThreadDemand(t.Context(), "pending") || m.HasThreadDemand(t.Context(), "unopened") {
		t.Fatal("query membership ownership changed")
	}
	in.retired = true
	if m.HasThreadDemand(t.Context(), "joined") {
		t.Fatal("retired connection retained demand")
	}
	in.retired = false
	cancel()
	if m.HasThreadDemand(t.Context(), "joined") {
		t.Fatal("canceled generation retained demand")
	}
}
