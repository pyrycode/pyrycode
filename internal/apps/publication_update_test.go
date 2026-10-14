package apps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func updateFixture(t *testing.T) (*Registry, *Owner, Toolchain, candidateDouble, string) {
	t.Helper()
	r, o, tools, c, config := publishFixture(t, healthReply{Body: healthyBody(), API: true}, "ok")
	if _, e := o.Publish(context.Background(), testHost, testApp, tools); e != nil {
		t.Fatal(e)
	}
	testArtifactWrite(t, r.root, testApp+"/data/preference", "saved")
	return r, o, tools, c, config
}

func updateCandidate(t *testing.T, r *Registry, c candidateDouble, version, migration, body string) {
	t.Helper()
	m := testMap(t)
	m["release_version"], m["title"] = version, "Updated"
	testArtifactWrite(t, filepath.Join(r.root, testApp, "source"), "app.json", string(testJSON(t, m)))
	testArtifactWrite(t, c.Fixture, "app.json", string(testJSON(t, m)))
	b, _ := json.Marshal(healthReply{Body: body, API: true})
	testArtifactWrite(t, c.Fixture, "dist/service/main.js", string(b))
	testArtifactWrite(t, c.Fixture, "dist/service/migrate.js", migration)
}

func TestUpdateSuccessAndRetry(t *testing.T) {
	r, o, tools, c, _ := updateFixture(t)
	old := healthTarget(t, o)
	updateCandidate(t, r, c, "2.0.0", "ok", strings.Replace(healthyBody(), "1.0.0", "2.0.0", 1))
	result, e := o.Publish(context.Background(), testHost, testApp, tools)
	if e != nil || !result.Healthy || !result.ActivationCommitted || result.RecoveryPending || result.Lifecycle.Title != "Updated" || result.Lifecycle.ActiveRelease != "2.0.0" {
		t.Fatal(result, e)
	}
	if healthUse(old) == nil {
		t.Fatal("old target reused")
	}
	if string(testBytes(t, filepath.Join(r.root, testApp, "data/preference"))) != "saved" {
		t.Fatal("preference lost")
	}
	g := recoveryGeneration(o, testApp)
	before := ownerRecord(t, r, testApp)
	migrationBefore := string(testBytes(t, filepath.Join(r.root, testApp, "data/migration")))
	result, e = o.Publish(context.Background(), testHost, testApp, tools)
	if e != nil || !result.Healthy || recoveryGeneration(o, testApp) != g || ownerRecord(t, r, testApp).Revision != before.Revision || string(testBytes(t, filepath.Join(r.root, testApp, "data/migration"))) != migrationBefore {
		t.Fatal("active retry changed activation or migrated", result, e)
	}
	// Supply the original checked bytes, including their original display title.
	m := testMap(t)
	testArtifactWrite(t, filepath.Join(r.root, testApp, "source"), "app.json", string(testJSON(t, m)))
	testArtifactWrite(t, c.Fixture, "app.json", string(testJSON(t, m)))
	b, _ := json.Marshal(healthReply{Body: healthyBody(), API: true})
	testArtifactWrite(t, c.Fixture, "dist/service/main.js", string(b))
	if result, e = o.Publish(context.Background(), testHost, testApp, tools); e == nil || result.Lifecycle.ActiveRelease != "2.0.0" || recoveryGeneration(o, testApp) != g {
		t.Fatal("historical downgrade", result, e)
	}
}

func TestUpdateFailures(t *testing.T) {
	for _, phase := range []string{"build", "snapshot", "migration", "health", "activation", "finalization", "restore", "recovery finalization", "old health", "old build"} {
		t.Run(phase, func(t *testing.T) {
			r, o, tools, c, config := updateFixture(t)
			old := recoveryGeneration(o, testApp)
			migration, body := "ok", strings.Replace(healthyBody(), "1.0.0", "2.0.0", 1)
			if phase == "migration" || phase == "restore" || phase == "recovery finalization" || phase == "old health" || phase == "old build" {
				migration = "fail"
			}
			if phase == "health" {
				body = `{}`
				o.startup = 250000000
			}
			updateCandidate(t, r, c, "2.0.0", migration, body)
			if phase == "build" {
				c.Fail = "run build"
				testCandidateConfig(t, config, c)
			}
			if phase == "snapshot" {
				o.backupOps.hook = func(step string) error {
					if step == "copy" {
						return errors.New("SECRET")
					}
					return nil
				}
			}
			if phase == "old health" {
				script := strings.Replace(string(testBytes(t, tools.Node)), "#!/bin/sh\n", "#!/bin/sh\nif [ \"$1\" = dist/service/main.js ] && [ \"$PYRY_APP_RELEASE\" = 1.0.0 ]; then exit 7; fi\n", 1)
				if e := os.WriteFile(tools.Node, []byte(script), 0700); e != nil {
					t.Fatal(e)
				}
				o.startup = 250000000
			}
			if phase == "old build" {
				entry := filepath.Join(ownerRecord(t, r, testApp).ReleaseHistory[0].BuildRoot, "dist/service/main.js")
				if e := os.Chmod(entry, 0600); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(entry, []byte("SECRET corrupt predecessor"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			injected := false
			r.mu.Lock()
			r.persist = func(root string, s snapshot) error {
				v := s.Records[0]
				activation := v.ActiveRelease == "2.0.0" && v.PublishJournal != nil
				finalization := v.ActiveRelease == "2.0.0" && v.PublishJournal == nil
				recovery := v.ActiveRelease == "1.0.0" && v.State == "running" && v.PublishJournal == nil && v.LastError != nil
				if phase == "activation" && activation || phase == "finalization" && finalization || phase == "recovery finalization" && recovery {
					injected = true
					return errors.New("SECRET " + root)
				}
				if phase == "restore" && v.PublishJournal != nil {
					injected = true
					_ = os.Symlink("/SECRET", filepath.Join(root, testApp, "data/unsafe"))
				}
				return writeSnapshot(root, s)
			}
			r.mu.Unlock()
			result, e := o.Publish(context.Background(), testHost, testApp, tools)
			if e == nil || strings.Contains(e.Error(), "SECRET") || strings.Contains(e.Error(), r.root) {
				t.Fatal(result, e)
			}
			v := publishDisk(t, r).Records[0]
			if phase == "finalization" {
				if !injected || !result.ActivationCommitted || !result.RecoveryPending || v.ActiveRelease != "2.0.0" || !v.PublishJournal.ActivationCommitted {
					t.Fatal(result, v)
				}
				return
			}
			if result.ActivationCommitted || result.Healthy || v.ActiveRelease != "1.0.0" || v.LastError == nil {
				t.Fatal(result, v)
			}
			if phase == "restore" || phase == "old health" || phase == "recovery finalization" || phase == "old build" {
				if !result.RecoveryPending || v.State != "failed" {
					t.Fatal(result, v)
				}
				if _, e := o.Lookup(testHost, testApp); e == nil {
					t.Fatal("unconfirmed recovery exposed")
				}
				return
			}
			if result.RecoveryPending || v.State != "running" || healthUse(healthTarget(t, o)) != nil {
				t.Fatal("old release unavailable", result, v)
			}
			if phase == "build" && recoveryGeneration(o, testApp) != old {
				t.Fatal("build failure retired old service")
			}
			if string(testBytes(t, filepath.Join(r.root, testApp, "data/preference"))) != "saved" {
				t.Fatal("preference lost")
			}
		})
	}
}

func TestUpdateChangedVersionRejected(t *testing.T) {
	_, o, tools, c, _ := updateFixture(t)
	old := recoveryGeneration(o, testApp)
	testArtifactWrite(t, c.Fixture, "dist/service/migrate.js", "fail")
	result, e := o.Publish(context.Background(), testHost, testApp, tools)
	if e == nil || result.RecoveryPending || result.ActivationCommitted || result.Lifecycle.ActiveRelease != "1.0.0" || result.Lifecycle.State != "running" || recoveryGeneration(o, testApp) != old {
		t.Fatal("changed active bytes accepted", result, e)
	}
	if healthUse(healthTarget(t, o)) != nil {
		t.Fatal("changed-version rejection retired old target")
	}
}
