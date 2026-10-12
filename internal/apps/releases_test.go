package apps

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func testRelease(r *Registry, app, version string) Release {
	id := strings.Repeat("a", 64)
	return Release{ReleaseVersion: version, BuildID: id, BuildRoot: filepath.Join(r.root, app, "builds", id), NodeVersion: "24.15.0", NPMVersion: "11.6.2", LockfileDigest: strings.Repeat("b", 64)}
}
func testJournal(r *Registry, app string, previous bool) *PublishJournal {
	j := &PublishJournal{Candidate: ReleaseReference{ServerID: testHost, AppID: app, ReleaseVersion: "2.0.0"}, VerifiedBackup: BackupReference{ServerID: testHost, AppID: app, PublishID: testID(90), Root: filepath.Join(r.root, app, "backups", testID(90))}}
	if previous {
		p := j.Candidate
		p.ReleaseVersion = "1.0.0"
		j.Previous = &p
	}
	return j
}
func TestReleaseHistoryRoundTrip(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	testRegister(t, r, testInput(testID(3)))
	c, e := r.Remove(testID(3))
	testChange(t, c, e)
	fields := testLifecycle()
	fields.PendingRelease = "2.0.0"
	c, e = r.UpdateLifecycle(testHost, testApp, fields)
	testChange(t, c, e)
	legacy := ownerRecord(t, r, testApp)
	reopened, e := Open(r.root, testHost)
	if e != nil {
		t.Fatal(e)
	}
	got := ownerRecord(t, reopened, testApp)
	legacy.State, legacy.Revision = "starting", got.Revision
	if !reflect.DeepEqual(legacy, got) || len(got.ReleaseHistory) != 0 || got.PublishJournal != nil || !reflect.DeepEqual(r.snapshot.Tombstones, reopened.snapshot.Tombstones) {
		t.Fatal("legacy altered", got, legacy)
	}
	r = reopened
	for _, v := range []string{"1.0.0", "2.0.0"} {
		c, e = r.RecordRelease(testHost, testApp, testRelease(r, testApp, v))
		testChange(t, c, e)
	}
	before := ownerRecord(t, r, testApp)
	c, e = r.RecordRelease(testHost, testApp, before.ReleaseHistory[0])
	if c || e != nil {
		t.Fatal("duplicate", c, e)
	}
	conflict := before.ReleaseHistory[0]
	conflict.NPMVersion = "11.6.3"
	c, e = r.RecordRelease(testHost, testApp, conflict)
	if c || !errors.Is(e, ErrConflict) {
		t.Fatal("immutable version", c, e)
	}
	c, e = r.Register([]byte(testManifest))
	if c || e != nil {
		t.Fatal(c, e)
	}
	m := testMap(t)
	m["title"] = "Candidate"
	c, e = r.UpdateManifest(testApp, testJSON(t, m))
	testChange(t, c, e)
	c, e = r.UpdateLifecycle(testHost, testApp, fields)
	testChange(t, c, e)
	current := ownerRecord(t, r, testApp)
	observed, c, e := r.CommitObservation(testHost, testApp, current.Revision, Observation{State: "starting"})
	testChange(t, c, e)
	reopened, e = Open(r.root, testHost)
	if e != nil {
		t.Fatal(e)
	}
	if got = ownerRecord(t, reopened, testApp); !reflect.DeepEqual(got, observed) || !reflect.DeepEqual(got.ReleaseHistory, before.ReleaseHistory) {
		t.Fatal("round trip", got, observed)
	}
}

func TestPublishAtomicTransitions(t *testing.T) {
	t.Parallel()
	for _, previous := range []bool{false, true} {
		t.Run(map[bool]string{false: "first", true: "update"}[previous], func(t *testing.T) {
			r := testOpen(t)
			testRegister(t, r, []byte(testManifest))
			fields := Lifecycle{Title: "Old", Desired: "available", State: "starting", PendingRelease: "2.0.0", LastError: &LastError{Code: "app.io_failed", Message: "Update failed"}}
			if previous {
				fields.ActiveRelease = "1.0.0"
			}
			c, e := r.UpdateLifecycle(testHost, testApp, fields)
			testChange(t, c, e)
			journal := testJournal(r, testApp, previous)
			c, e = r.UpdatePublish(testHost, testApp, journal, nil)
			testChange(t, c, e)
			reopened, e := Open(r.root, testHost)
			if e != nil {
				t.Fatal(e)
			}
			if got := ownerRecord(t, reopened, testApp); !reflect.DeepEqual(got.PublishJournal, journal) {
				t.Fatal("uncommitted reopen", got)
			}
			r = reopened
			// Subscribers and the actual persistence hook must see both halves together.
			var events []Record
			r.Subscribe(func(c Change) { events = append(events, *c.Record) })
			r.persist = func(root string, s snapshot) error {
				record := s.Records[0]
				if record.PublishJournal != nil && record.PublishJournal.ActivationCommitted && (record.ActiveRelease != "2.0.0" || record.Title != "New" || record.PendingRelease != "" || record.LastError != nil || record.State != "running") {
					t.Fatal("partial activation", record)
				}
				return writeSnapshot(root, s)
			}
			journal.ActivationCommitted = true
			activated := Lifecycle{Title: "New", Desired: "available", State: "running", ActiveRelease: "2.0.0"}
			c, e = r.UpdatePublish(testHost, testApp, journal, &activated)
			testChange(t, c, e)
			if len(events) != 1 || !events[0].PublishJournal.ActivationCommitted || events[0].ActiveRelease != "2.0.0" {
				t.Fatal("activation delivery", events)
			}
			c, e = r.UpdatePublish(testHost, testApp, journal, &activated)
			if c || e != nil || len(events) != 1 {
				t.Fatal("activation no-op", c, e)
			}
			reopened, e = Open(r.root, testHost)
			if e != nil {
				t.Fatal(e)
			}
			got := ownerRecord(t, reopened, testApp)
			if !got.PublishJournal.ActivationCommitted || got.State != "starting" || got.ActiveRelease != "2.0.0" {
				t.Fatal("committed reopen", got)
			}
			other := testJournal(r, testApp, previous)
			other.VerifiedBackup.PublishID = testID(91)
			other.VerifiedBackup.Root = filepath.Join(r.root, testApp, "backups", testID(91))
			c, e = r.UpdatePublish(testHost, testApp, other, nil)
			if c || !errors.Is(e, ErrConflict) {
				t.Fatal("outstanding publish", c, e)
			}
			recovered := fields
			recovered.PendingRelease = ""
			recovered.State = "failed"
			c, e = r.UpdatePublish(testHost, testApp, nil, &recovered)
			testChange(t, c, e)
			if len(events) != 2 || events[1].PublishJournal != nil || events[1].PendingRelease != "" || events[1].State != "failed" || events[1].ActiveRelease != fields.ActiveRelease {
				t.Fatal("partial recovery", events)
			}
			reopened, e = Open(r.root, testHost)
			if e != nil {
				t.Fatal(e)
			}
			if ownerRecord(t, reopened, testApp).PublishJournal != nil {
				t.Fatal("clear not durable")
			}
			c, e = r.UpdatePublish(testHost, testApp, other, nil)
			testChange(t, c, e)
			// Ordinary operations cannot drop an outstanding journal.
			c, e = r.UpdateLifecycle(testHost, testApp, fields)
			testChange(t, c, e)
			m := testMap(t)
			m["title"] = "Candidate"
			c, e = r.UpdateManifest(testApp, testJSON(t, m))
			testChange(t, c, e)
			current := ownerRecord(t, r, testApp)
			_, _, e = r.CommitObservation(testHost, testApp, current.Revision, Observation{State: "starting"})
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(ownerRecord(t, r, testApp).PublishJournal, other) {
				t.Fatal("journal lost")
			}
		})
	}
}

func TestMetadataFailures(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	events := 0
	r.Subscribe(func(Change) { events++ })
	before := cloneSnapshot(r.snapshot)
	path := filepath.Join(r.root, "registry.json")
	disk := testBytes(t, path)
	for _, tc := range []struct {
		name string
		edit func(*Release)
	}{
		{"version", func(v *Release) { v.ReleaseVersion = "01.0.0" }},
		{"build digest", func(v *Release) { v.BuildID = strings.Repeat("A", 64) }},
		{"lock digest", func(v *Release) { v.LockfileDigest = "short" }},
		{"node range", func(v *Release) { v.NodeVersion = ">=24.15.0" }},
		{"npm version", func(v *Release) { v.NPMVersion = "v11.6.2" }},
		{"foreign path", func(v *Release) { v.BuildRoot = filepath.Join(r.root, testID(9), "builds", v.BuildID) }},
		{"arbitrary path", func(v *Release) { v.BuildRoot = "/tmp/build" }},
		{"unclean path", func(v *Release) { v.BuildRoot = r.root + "/" + testApp + "/builds/../builds/" + v.BuildID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := testRelease(r, testApp, "1.0.0")
			tc.edit(&v)
			c, e := r.RecordRelease(testHost, testApp, v)
			if c || !errors.Is(e, ErrInvalidStorage) {
				t.Fatal(c, e)
			}
		})
	}
	for _, tc := range []struct {
		name string
		edit func(*PublishJournal)
	}{
		{"candidate host", func(j *PublishJournal) { j.Candidate.ServerID = testID(8) }},
		{"candidate app", func(j *PublishJournal) { j.Candidate.AppID = testID(8) }},
		{"previous app", func(j *PublishJournal) { j.Previous.AppID = testID(8) }},
		{"previous host", func(j *PublishJournal) { j.Previous.ServerID = testID(8) }},
		{"release ref", func(j *PublishJournal) { j.Candidate.ReleaseVersion = "bad" }},
		{"backup host", func(j *PublishJournal) { j.VerifiedBackup.ServerID = testID(8) }},
		{"backup app", func(j *PublishJournal) { j.VerifiedBackup.AppID = testID(8) }},
		{"publish id", func(j *PublishJournal) { j.VerifiedBackup.PublishID = "../escape" }},
		{"backup path", func(j *PublishJournal) { j.VerifiedBackup.Root = "/tmp/backup" }},
		{"backup escape", func(j *PublishJournal) { j.VerifiedBackup.Root = r.root + "/" + testApp + "/backups/../" + testID(90) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := testJournal(r, testApp, true)
			tc.edit(j)
			c, e := r.UpdatePublish(testHost, testApp, j, nil)
			if c || !errors.Is(e, ErrInvalidStorage) {
				t.Fatal(c, e)
			}
		})
	}
	for _, tc := range []struct {
		host, app string
		want      error
	}{{"bad", testApp, ErrInvalidIdentity}, {testID(8), testApp, ErrForeignHost}, {testHost, "bad", ErrInvalidIdentity}, {testHost, testID(8), ErrNotFound}} {
		c, e := r.RecordRelease(tc.host, tc.app, testRelease(r, testApp, "1.0.0"))
		if c || !errors.Is(e, tc.want) {
			t.Fatal(tc, c, e)
		}
		c, e = r.UpdatePublish(tc.host, tc.app, testJournal(r, testApp, false), nil)
		if c || !errors.Is(e, tc.want) {
			t.Fatal(tc, c, e)
		}
	}
	failure := errors.New("write failed")
	r.persist = func(string, snapshot) error { return failure }
	for _, mutate := range []func() (bool, error){
		func() (bool, error) { return r.RecordRelease(testHost, testApp, testRelease(r, testApp, "1.0.0")) },
		func() (bool, error) {
			f := testLifecycle()
			return r.UpdatePublish(testHost, testApp, testJournal(r, testApp, true), &f)
		},
	} {
		c, e := mutate()
		if c || !errors.Is(e, failure) {
			t.Fatal(c, e)
		}
	}
	if events != 0 || !reflect.DeepEqual(before, r.snapshot) || !bytes.Equal(disk, testBytes(t, path)) {
		t.Fatal("failure changed state")
	}
	r.persist = writeSnapshot
	c, e := r.RecordRelease(testHost, testApp, testRelease(r, testApp, "1.0.0"))
	testChange(t, c, e)
	c, e = r.UpdatePublish(testHost, testApp, testJournal(r, testApp, true), nil)
	testChange(t, c, e)
	before = cloneSnapshot(r.snapshot)
	disk = testBytes(t, path)
	events = 0
	r.persist = func(string, snapshot) error { return failure }
	committed := testJournal(r, testApp, true)
	committed.ActivationCommitted = true
	f := testLifecycle()
	f.ActiveRelease = "2.0.0"
	c, e = r.UpdatePublish(testHost, testApp, committed, &f)
	if c || !errors.Is(e, failure) {
		t.Fatal(c, e)
	}
	c, e = r.UpdatePublish(testHost, testApp, nil, &f)
	if c || !errors.Is(e, failure) {
		t.Fatal(c, e)
	}
	if events != 0 || !reflect.DeepEqual(before, r.snapshot) || !bytes.Equal(disk, testBytes(t, path)) {
		t.Fatal("activation/recovery failure changed state")
	}
	r.persist = writeSnapshot
	c, e = r.Remove(testApp)
	testChange(t, c, e)
	c, e = r.RecordRelease(testHost, testApp, testRelease(r, testApp, "1.0.0"))
	if c || !errors.Is(e, ErrRemoved) {
		t.Fatal(c, e)
	}
	c, e = r.UpdatePublish(testHost, testApp, nil, nil)
	if c || !errors.Is(e, ErrRemoved) {
		t.Fatal(c, e)
	}
}

func TestMetadataIsolationAndOrdering(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	apps := []string{testApp, testID(2)}
	for _, app := range apps {
		testRegister(t, r, testInput(app))
		c, e := r.UpdatePublish(testHost, app, testJournal(r, app, true), nil)
		testChange(t, c, e)
	}
	var events []Change
	r.Subscribe(func(c Change) { events = append(events, c) })
	r.Subscribe(func(c Change) {
		if (c.Revision <= 10 && c.Record.PublishJournal.Previous.ReleaseVersion != "1.0.0") || c.Record.ReleaseHistory[0].NPMVersion != "11.6.2" {
			t.Error("subscriber shared metadata")
		}
		c.Record.ReleaseHistory[0].NPMVersion = "99.0.0"
		c.Record.PublishJournal.Previous.AppID = "bad"
	})
	var wg sync.WaitGroup
	for _, app := range apps {
		wg.Add(1)
		go func(app string) {
			defer wg.Done()
			for _, v := range []string{"1.0.0", "2.0.0", "3.0.0"} {
				c, e := r.RecordRelease(testHost, app, testRelease(r, app, v))
				if !c || e != nil {
					t.Error(c, e)
				}
			}
		}(app)
	}
	wg.Wait()
	if len(events) != 6 {
		t.Fatal(events)
	}
	for i, c := range events {
		if c.Revision != uint64(i+5) || c.Record.Revision != c.Revision || c.Record.PublishJournal.Previous.AppID != c.AppID || c.Record.ReleaseHistory[0].NPMVersion != "11.6.2" {
			t.Fatal("ordering/isolation", events)
		}
	}
	input := testJournal(r, testApp, true)
	input.Previous.ReleaseVersion = "0.9.0"
	c, e := r.UpdatePublish(testHost, testApp, input, nil)
	testChange(t, c, e)
	input.Previous.AppID = "input mutation"
	rows, _ := r.List()
	rows[0].ReleaseHistory[0].BuildID = "list mutation"
	rows[0].PublishJournal.Previous.AppID = "list mutation"
	for _, event := range events {
		event.Record.ReleaseHistory[0].BuildID = "event mutation"
		event.Record.PublishJournal.Previous.AppID = "event mutation"
	}
	rows, _ = r.List()
	for _, row := range rows {
		if len(row.ReleaseHistory) != 3 || row.ReleaseHistory[0].BuildID != strings.Repeat("a", 64) || row.PublishJournal.Previous.AppID != row.AppID {
			t.Fatal("metadata escaped", row)
		}
	}
}

func TestMetadataStorageValidation(t *testing.T) {
	t.Parallel()
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	c, e := r.RecordRelease(testHost, testApp, testRelease(r, testApp, "1.0.0"))
	testChange(t, c, e)
	c, e = r.UpdatePublish(testHost, testApp, testJournal(r, testApp, true), nil)
	testChange(t, c, e)
	data := testBytes(t, filepath.Join(r.root, "registry.json"))
	var base map[string]any
	if e = json.Unmarshal(data, &base); e != nil {
		t.Fatal(e)
	}
	record := base["records"].([]any)[0].(map[string]any)
	history := record["release_history"].([]any)
	journal := record["publish_journal"].(map[string]any)
	objects := []struct {
		name   string
		object map[string]any
	}{
		{"release", history[0].(map[string]any)}, {"journal", journal},
		{"candidate", journal["candidate"].(map[string]any)}, {"previous", journal["previous"].(map[string]any)}, {"backup", journal["verified_backup"].(map[string]any)},
	}
	var cases [][]byte
	for _, o := range objects {
		for key, value := range o.object {
			if o.name != "journal" || key != "previous" {
				delete(o.object, key)
				cases = append(cases, testJSON(t, base))
				o.object[key] = value
			}
			o.object[strings.ToUpper(key)] = value
			cases = append(cases, testJSON(t, base))
			delete(o.object, strings.ToUpper(key))
			o.object[key] = nil
			cases = append(cases, testJSON(t, base))
			o.object[key] = value
			encoded, _ := json.Marshal(o.object)
			keyJSON, _ := json.Marshal(key)
			valueJSON, _ := json.Marshal(value)
			duplicate := append([]byte{'{'}, keyJSON...)
			duplicate = append(duplicate, ':')
			duplicate = append(duplicate, valueJSON...)
			duplicate = append(duplicate, ',')
			duplicate = append(duplicate, encoded[1:]...)
			original := testJSON(t, base)
			cases = append(cases, bytes.Replace(original, encoded, duplicate, 1))
		}
	}
	record["release_history"] = append(history, history[0])
	cases = append(cases, testJSON(t, base))
	record["release_history"] = history
	for _, key := range []string{"release_history", "publish_journal"} {
		value := record[key]
		record[key] = nil
		cases = append(cases, testJSON(t, base))
		record[key] = value
	}
	// The state would normalize if invalid metadata were accepted first.
	record["state"] = "running"
	record["active_release"] = "1.0.0"
	history[0].(map[string]any)["build_root"] = "/tmp/foreign"
	cases = append(cases, testJSON(t, base))
	cases = append(cases, append(append([]byte{}, data...), []byte(" {}")...))
	for i, invalid := range cases {
		path := filepath.Join(r.root, "registry.json")
		if e = os.WriteFile(path, invalid, 0600); e != nil {
			t.Fatal(e)
		}
		if _, e = Open(r.root, testHost); !errors.Is(e, ErrInvalidStorage) {
			t.Fatalf("case %d accepted: %v", i, e)
		}
		if !bytes.Equal(testBytes(t, path), invalid) {
			t.Fatalf("case %d rewritten", i)
		}
	}
}
