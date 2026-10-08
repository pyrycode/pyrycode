package conversations

import "testing"

func TestRegistryCapturedSessionOwner(t *testing.T) {
	t.Parallel()
	r := &Registry{}
	r.Create(Conversation{ID: "owner", CurrentSessionID: "A"})
	r.Create(Conversation{ID: "unbound"})
	if owner, ok := r.RebindSessionOwner("", "B"); ok || owner != "" {
		t.Fatalf("empty owner = %q, %v", owner, ok)
	}
	if owner, ok := r.RebindSessionOwner("missing", "B"); ok || owner != "" {
		t.Fatalf("missing owner = %q, %v", owner, ok)
	}
	if owner, ok := r.RebindSessionOwner("A", "B"); !ok || owner != "owner" {
		t.Fatalf("rebound owner = %q, %v", owner, ok)
	}
	if owner, ok := r.SessionOwner("A"); ok || owner != "" {
		t.Fatalf("historical owner = %q, %v", owner, ok)
	}
	if owner, ok := r.SessionOwner("B"); !ok || owner != "owner" {
		t.Fatalf("current owner = %q, %v", owner, ok)
	}
	if owner, ok := r.SessionOwner(""); ok || owner != "" {
		t.Fatalf("unbound owner = %q, %v", owner, ok)
	}
	if !r.RebindSession("B", "C") {
		t.Fatal("legacy rebind failed")
	}
	c, _ := r.Get("owner")
	if c.CurrentSessionID != "C" || len(c.SessionHistory) != 2 || c.SessionHistory[0] != "A" || c.SessionHistory[1] != "B" {
		t.Fatalf("binding = %+v", c)
	}
}
