package apps

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func testLifecycle() Lifecycle {
	return Lifecycle{Title: "Committed", Desired: "available", State: "running", ActiveRelease: "1.0.0", LastError: &LastError{Code: "app.io_failed", Message: "Update failed"}}
}

func TestLifecycleMutations(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	fields := testLifecycle()
	fields.PendingRelease = "2.0.0"
	c, e := r.UpdateLifecycle(testHost, testApp, fields)
	testChange(t, c, e)
	fields.LastError.Message = "caller mutation"
	rows, rev := r.List()
	if rev != 2 || rows[0].Revision != 2 || rows[0].Title != "Committed" || rows[0].LastError.Message != "Update failed" {
		t.Fatal(rows, rev)
	}
	c, e = r.Register([]byte(testManifest))
	if c || e != nil {
		t.Fatal("registration after lifecycle", c, e)
	}
	m := testMap(t)
	m["title"], m["release_version"] = "Candidate", "2.0.0"
	c, e = r.UpdateManifest(testApp, testJSON(t, m))
	testChange(t, c, e)
	got, rev := r.List()
	want := rows[0]
	want.Manifest, want.Revision = got[0].Manifest, 3
	if rev != 3 || !reflect.DeepEqual(got[0], want) {
		t.Fatal(got, want, rev)
	}
	fields = Lifecycle{Title: "Candidate", Desired: "available", State: "running", ActiveRelease: "2.0.0"}
	c, e = r.UpdateLifecycle(testHost, testApp, fields)
	testChange(t, c, e)
	got, rev = r.List()
	want.Title, want.ActiveRelease, want.PendingRelease, want.LastError, want.Revision = "Candidate", "2.0.0", "", nil, 4
	if rev != 4 || !reflect.DeepEqual(got[0], want) {
		t.Fatal("cutover", got, want, rev)
	}
	before := testBytes(t, filepath.Join(r.root, "registry.json"))
	c, e = r.UpdateLifecycle(testHost, testApp, fields)
	if c || e != nil || !bytes.Equal(before, testBytes(t, filepath.Join(r.root, "registry.json"))) {
		t.Fatal("lifecycle no-op", c, e)
	}
	fields.Desired, fields.State = "stopped", "stopped"
	c, e = r.UpdateLifecycle(testHost, testApp, fields)
	testChange(t, c, e)
	fields.Desired, fields.State = "available", "starting"
	c, e = r.UpdateLifecycle(testHost, testApp, fields)
	testChange(t, c, e)
}

func TestLifecycleValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		edit func(*Lifecycle)
	}{
		{"empty title", func(v *Lifecycle) { v.Title = "" }},
		{"title bytes", func(v *Lifecycle) { v.Title = strings.Repeat("é", 65) }},
		{"title utf8", func(v *Lifecycle) { v.Title = "\xff" }},
		{"title control", func(v *Lifecycle) { v.Title = "bad\nname" }},
		{"desired", func(v *Lifecycle) { v.Desired = "ready" }},
		{"state", func(v *Lifecycle) { v.State = "ready" }},
		{"running unpublished", func(v *Lifecycle) { v.ActiveRelease = "" }},
		{"active release", func(v *Lifecycle) { v.ActiveRelease = "01.0.0" }},
		{"pending release", func(v *Lifecycle) { v.PendingRelease = "bad" }},
		{"error code", func(v *Lifecycle) { v.LastError.Code = "internal.failure" }},
		{"empty message", func(v *Lifecycle) { v.LastError.Message = "" }},
		{"message bytes", func(v *Lifecycle) { v.LastError.Message = strings.Repeat("é", 81) }},
		{"message utf8", func(v *Lifecycle) { v.LastError.Message = "\xff" }},
		{"message control", func(v *Lifecycle) { v.LastError.Message = "bad\nmessage" }},
	}
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	before := cloneSnapshot(r.snapshot)
	notifications := 0
	cancel := r.Subscribe(func(Change) { notifications++ })
	defer cancel()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := testLifecycle()
			tc.edit(&v)
			if c, e := r.UpdateLifecycle(testHost, testApp, v); c || !errors.Is(e, ErrInvalidLifecycle) {
				t.Fatal(c, e)
			}
			if !reflect.DeepEqual(before, r.snapshot) {
				t.Fatal("invalid input changed snapshot")
			}
		})
	}
	for _, tc := range []struct {
		host, id string
		err      error
	}{
		{"bad", testApp, ErrInvalidIdentity}, {testHost, "bad", ErrInvalidIdentity},
		{testID(77), testApp, ErrForeignHost}, {testHost, testID(77), ErrNotFound},
	} {
		if c, e := r.UpdateLifecycle(tc.host, tc.id, testLifecycle()); c || !errors.Is(e, tc.err) {
			t.Fatal(tc, c, e)
		}
	}
	if notifications != 0 {
		t.Fatal("rejected lifecycle notified", notifications)
	}
	for _, code := range []string{"protocol.unsupported", "app.invalid_request", "app.not_found", "app.release_changed", "app.unavailable", "app.limit_exceeded", "app.busy", "app.timeout", "app.io_failed", "apps.changed"} {
		v := testLifecycle()
		v.Title = strings.Repeat("é", 64)
		v.LastError = &LastError{Code: code, Message: strings.Repeat("é", 80)}
		c, e := r.UpdateLifecycle(testHost, testApp, v)
		testChange(t, c, e)
	}
	for _, state := range []string{"starting", "stopped", "failed"} {
		c, e := r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: "App", Desired: "stopped", State: state})
		testChange(t, c, e)
	}
	c, e := r.Remove(testApp)
	testChange(t, c, e)
	if c, e := r.UpdateLifecycle(testHost, testApp, testLifecycle()); c || !errors.Is(e, ErrRemoved) {
		t.Fatal(c, e)
	}
}

func TestRestartNormalization(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	for i := 1; i <= 16; i++ {
		testRegister(t, r, testInput(testID(i)))
	}
	testRegister(t, r, testInput(testID(20)))
	c, e := r.Remove(testID(20))
	testChange(t, c, e)
	n := 0
	for _, desired := range []string{"available", "stopped"} {
		for _, active := range []string{"", "1.0.0"} {
			for _, state := range []string{"starting", "running", "stopped", "failed"} {
				row := &r.snapshot.Records[n]
				n++
				row.Title, row.Desired, row.ActiveRelease, row.PendingRelease, row.State = "Committed", desired, active, "2.0.0", state
				if state == "running" && active == "" {
					row.State = "failed"
				}
				row.LastError = &LastError{Code: "app.io_failed", Message: "Start failed"}
			}
		}
	}
	// Disk order must not determine normalization revisions.
	for i, j := 0, len(r.snapshot.Records)-1; i < j; i, j = i+1, j-1 {
		r.snapshot.Records[i], r.snapshot.Records[j] = r.snapshot.Records[j], r.snapshot.Records[i]
	}
	if e := writeSnapshot(r.root, r.snapshot); e != nil {
		t.Fatal(e)
	}
	want := cloneSnapshot(r.snapshot)
	sortSnapshot(&want)
	for i := range want.Records {
		row := &want.Records[i]
		state := "stopped"
		if row.Desired == "available" && row.ActiveRelease != "" {
			state = "starting"
		}
		if row.State != state {
			want.Revision++
			row.State = state
			row.Revision = want.Revision
		}
	}
	reopened, e := Open(r.root, testHost)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(reopened.snapshot, want) {
		t.Fatalf("got %#v want %#v", reopened.snapshot, want)
	}
	path := filepath.Join(r.root, "registry.json")
	before := testBytes(t, path)
	reopened, e = openRegistry(r.root, testHost, func(string, snapshot) error { t.Fatal("unchanged reopen wrote"); return nil })
	if e != nil || !reflect.DeepEqual(reopened.snapshot, want) || !bytes.Equal(before, testBytes(t, path)) {
		t.Fatal("unchanged reopen", e)
	}
	var count int
	cancel := reopened.Subscribe(func(Change) { count++ })
	defer cancel()
	if count != 0 {
		t.Fatal("normalization replayed")
	}
}

func TestNormalizationFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"exhausted", "write failure", "exact budget"} {
		t.Run(mode, func(t *testing.T) {
			r := testOpen(t)
			for i := 1; i <= 3; i++ {
				testRegister(t, r, testInput(testID(i)))
			}
			for i := range r.snapshot.Records {
				r.snapshot.Records[i].State = "running"
				r.snapshot.Records[i].ActiveRelease = "1.0.0"
			}
			r.snapshot.Revision = maxRevision - 2
			if mode == "exact budget" {
				r.snapshot.Revision = maxRevision - 3
			}
			if mode == "write failure" {
				r.snapshot.Revision = 3
			}
			if e := writeSnapshot(r.root, r.snapshot); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(r.root, "registry.json")
			before := testBytes(t, path)
			failure := errors.New("private diagnostic")
			writes := 0
			persist := func(root string, s snapshot) error {
				writes++
				if mode == "write failure" {
					return failure
				}
				return writeSnapshot(root, s)
			}
			got, e := openRegistry(r.root, testHost, persist)
			if mode == "exact budget" {
				if e != nil || writes != 1 || got.snapshot.Revision != maxRevision {
					t.Fatal(got, e, writes)
				}
				for i, row := range got.snapshot.Records {
					if row.State != "starting" || row.Revision != maxRevision-2+uint64(i) {
						t.Fatal(row)
					}
				}
				before = testBytes(t, path)
				again, e := Open(r.root, testHost)
				if e != nil || !reflect.DeepEqual(again.snapshot, got.snapshot) || !bytes.Equal(before, testBytes(t, path)) {
					t.Fatal("unchanged exhausted reopen", e)
				}
				return
			}
			expected := ErrExhausted
			expectedWrites := 0
			if mode == "write failure" {
				expected = failure
				expectedWrites = 1
			}
			if got != nil || !errors.Is(e, expected) || writes != expectedWrites || !bytes.Equal(before, testBytes(t, path)) {
				t.Fatal("normalization leaked", got, e, writes)
			}
		})
	}
}

func TestCommittedChanges(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	var events []Change
	cancelMutator := r.Subscribe(func(c Change) {
		if c.Record != nil {
			c.Record.Title, c.Record.Revision = "consumer mutation", 0
			if c.Record.LastError != nil {
				c.Record.LastError.Message = "consumer mutation"
			}
		}
	})
	cancel := r.Subscribe(func(c Change) {
		// Persistence and the committed memory swap precede every notification.
		var stored snapshot
		data := testBytes(t, filepath.Join(r.root, "registry.json"))
		if e := json.Unmarshal(data, &stored); e != nil {
			t.Error(e)
		}
		if stored.Revision != c.Revision || r.snapshot.Revision != c.Revision {
			t.Error("notification before commit")
		}
		events = append(events, c)
	})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 32; i++ {
			unsubscribe := r.Subscribe(func(Change) {})
			unsubscribe()
			unsubscribe()
		}
	}()
	for i := 1; i <= 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := testID(i)
			c, e := r.Register(testInput(id))
			if !c || e != nil {
				t.Error(c, e)
				return
			}
			m := testMap(t)
			m["app_id"], m["title"] = id, fmt.Sprintf("Candidate %d", i)
			c, e = r.UpdateManifest(id, testJSON(t, m))
			if !c || e != nil {
				t.Error(c, e)
			}
			c, e = r.UpdateLifecycle(testHost, id, testLifecycle())
			if !c || e != nil {
				t.Error(c, e)
			}
			c, e = r.Remove(id)
			if !c || e != nil {
				t.Error(c, e)
			}
		}(i)
	}
	wg.Wait()
	if c, e := r.Remove(testID(1)); c || e != nil {
		t.Fatal("repeated removal", c, e)
	}
	cancel()
	cancel()
	cancelMutator()
	if len(events) != 32 {
		t.Fatal(len(events))
	}
	stages := map[string]int{}
	for i, c := range events {
		if c.ServerID != testHost || c.Revision != uint64(i+1) {
			t.Fatal("unordered", c)
		}
		if c.Record != nil {
			if c.Record.AppID != c.AppID || c.Record.Revision != c.Revision {
				t.Fatal(c)
			}
			if c.Record.LastError != nil && c.Record.LastError.Message != "Update failed" {
				t.Fatal("aliased consumers", c)
			}
		}
		stage := stages[c.AppID]
		stages[c.AppID]++
		if stage == 3 {
			if c.Record != nil {
				t.Fatal("removal record", c)
			}
			continue
		}
		input := testMap(t)
		input["app_id"] = c.AppID
		if stage > 0 {
			// IDs encode the worker index used to construct the candidate title.
			for n := 1; n <= 8; n++ {
				if testID(n) == c.AppID {
					input["title"] = fmt.Sprintf("Candidate %d", n)
				}
			}
		}
		manifest, e := ValidateManifest(testJSON(t, input), testHost)
		if e != nil {
			t.Fatal(e)
		}
		want := Record{AppID: c.AppID, Manifest: manifest, Title: "App", Desired: "available", State: "stopped", Revision: c.Revision}
		if stage == 2 {
			want = Record{AppID: c.AppID, Manifest: manifest, Title: "Committed", Desired: "available", State: "running", ActiveRelease: "1.0.0", LastError: &LastError{Code: "app.io_failed", Message: "Update failed"}, Revision: c.Revision}
		}
		if c.Record == nil || !reflect.DeepEqual(*c.Record, want) {
			t.Fatalf("stage %d got %#v want %#v", stage, c.Record, want)
		}
	}
	last := events[len(events)-1]
	if last.Record != nil {
		t.Fatal("removal must be tombstone", last)
	}
	var late []Change
	stop := r.Subscribe(func(c Change) { late = append(late, c) })
	defer stop()
	testRegister(t, r, testInput(testID(100)))
	if len(late) != 1 || late[0].Revision != 33 || len(events) != 32 {
		t.Fatal("subscription replay or cancellation")
	}
}

func TestLifecycleFailureAndExhaustion(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"failure", "exhaustion"} {
		t.Run(mode, func(t *testing.T) {
			r := testOpen(t)
			testRegister(t, r, []byte(testManifest))
			fields := testLifecycle()
			c, e := r.UpdateLifecycle(testHost, testApp, fields)
			testChange(t, c, e)
			if mode == "exhaustion" {
				r.snapshot.Revision = maxRevision
				r.snapshot.Records[0].Revision = maxRevision
				if e := writeSnapshot(r.root, r.snapshot); e != nil {
					t.Fatal(e)
				}
			}
			before := cloneSnapshot(r.snapshot)
			path := filepath.Join(r.root, "registry.json")
			data := testBytes(t, path)
			var events []Change
			cancel := r.Subscribe(func(c Change) { events = append(events, c) })
			defer cancel()
			expected := errors.New("private I/O diagnostic")
			writes := 0
			r.persist = func(string, snapshot) error { writes++; return expected }
			if mode == "exhaustion" {
				expected = ErrExhausted
			}
			c, e = r.UpdateLifecycle(testHost, testApp, fields)
			if c || e != nil {
				t.Fatal("semantic pointer no-op", c, e)
			}
			c, e = r.Register([]byte(testManifest))
			if c || e != nil {
				t.Fatal(c, e)
			}
			c, e = r.UpdateManifest(testApp, []byte(testManifest))
			if c || e != nil {
				t.Fatal(c, e)
			}
			fields.Title = "Changed"
			for _, op := range []func() (bool, error){
				func() (bool, error) { return r.UpdateLifecycle(testHost, testApp, fields) },
				func() (bool, error) { return r.Register(testInput(testID(3))) },
				func() (bool, error) {
					m := testMap(t)
					m["title"] = "Candidate"
					return r.UpdateManifest(testApp, testJSON(t, m))
				},
				func() (bool, error) { return r.Remove(testApp) },
			} {
				if c, e := op(); c || !errors.Is(e, expected) {
					t.Fatal(c, e)
				}
			}
			wantWrites := 4
			if mode == "exhaustion" {
				wantWrites = 0
			}
			if writes != wantWrites || len(events) != 0 || !reflect.DeepEqual(before, r.snapshot) || !bytes.Equal(data, testBytes(t, path)) {
				t.Fatal("failed mutation leaked", writes, events)
			}
		})
	}
}
