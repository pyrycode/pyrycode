package apps

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestConditionalObservationIntegrity(t *testing.T) {
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	_, e := r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: "Committed", Desired: "stopped", State: "stopped", ActiveRelease: "1.0.0", PendingRelease: "2.0.0"})
	if e != nil {
		t.Fatal(e)
	}
	m := testMap(t)
	m["title"] = "Candidate"
	m["release_version"] = "3.0.0"
	if _, e = r.UpdateManifest(testApp, testJSON(t, m)); e != nil {
		t.Fatal(e)
	}
	before := ownerRecord(t, r, testApp)
	metadata := &LastError{Code: "app.unavailable", Message: "Service could not start"}
	got, changed, e := r.CommitObservation(testHost, testApp, before.Revision, Observation{State: "failed", LastError: metadata})
	if e != nil || !changed {
		t.Fatal(got, changed, e)
	}
	metadata.Message = "caller mutation"
	got.LastError.Message = "returned mutation"
	expected := before
	expected.State = "failed"
	expected.LastError = &LastError{Code: "app.unavailable", Message: "Service could not start"}
	expected.Revision = before.Revision + 1
	committed := ownerRecord(t, r, testApp)
	if !reflect.DeepEqual(committed, expected) {
		t.Fatal(committed, expected)
	}
	events := 0
	r.Subscribe(func(Change) { events++ })
	disk := testBytes(t, filepath.Join(r.root, "registry.json"))
	_, hostRev := r.List()
	for _, o := range []Observation{{State: "bogus"}, {Desired: "bogus"}, {State: "failed", LastError: &LastError{Code: "raw diagnostics", Message: "unsafe"}}} {
		_, changed, e = r.CommitObservation(testHost, testApp, committed.Revision, o)
		if changed || !errors.Is(e, ErrInvalidLifecycle) {
			t.Fatal(changed, e)
		}
	}
	failure := errors.New("failed conditional write")
	r.persist = func(string, snapshot) error { return failure }
	_, changed, e = r.CommitObservation(testHost, testApp, committed.Revision, Observation{Desired: "available", State: "starting"})
	if changed || !errors.Is(e, failure) {
		t.Fatal(changed, e)
	}
	after, rev := r.List()
	if rev != hostRev || events != 0 || !reflect.DeepEqual(after, []Record{committed}) || !bytes.Equal(disk, testBytes(t, filepath.Join(r.root, "registry.json"))) {
		t.Fatal("rejected/failed observation changed storage")
	}
	r.persist = writeSnapshot
	same, changed, e := r.CommitObservation(testHost, testApp, committed.Revision, Observation{State: "failed", LastError: committed.LastError})
	if e != nil || changed || !reflect.DeepEqual(same, committed) || events != 0 {
		t.Fatal("no-op", same, changed, e)
	}
	for _, identity := range []struct {
		host, id string
		want     error
	}{{testID(9), testApp, ErrForeignHost}, {testHost, testID(9), ErrNotFound}, {"bad", testApp, ErrInvalidIdentity}} {
		_, changed, e = r.CommitObservation(identity.host, identity.id, committed.Revision, Observation{State: "stopped"})
		if changed || !errors.Is(e, identity.want) {
			t.Fatal(identity, e)
		}
	}
	if _, e = r.Remove(testApp); e != nil {
		t.Fatal(e)
	}
	_, changed, e = r.CommitObservation(testHost, testApp, committed.Revision, Observation{State: "stopped"})
	if changed || !errors.Is(e, ErrRemoved) {
		t.Fatal(changed, e)
	}
}
