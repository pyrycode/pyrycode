package apps

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupTemplateRecovery(t *testing.T) {
	node, npm := os.Getenv("HOSTED_APP_NODE"), os.Getenv("HOSTED_APP_NPM_CLI")
	if node == "" || npm == "" {
		t.Skip("supported real Node/npm required; skips provide no restart evidence")
	}
	version, err := exec.Command(node, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("runtime %s", strings.TrimSpace(string(version)))
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	t.Cleanup(func() {
		for _, release := range ownerRecord(t, r, testApp).ReleaseHistory {
			testWritableBuild(t, release.BuildRoot)
		}
	})
	files, err := templateFiles(ownerRecord(t, r, testApp).Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeSource(context.Background(), r.root, testApp, files); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(r.root, testApp, "source")
	data := filepath.Join(r.root, testApp, "data")
	main := string(testBytes(t, filepath.Join(source, "src/service/main.ts")))
	guard := fmt.Sprintf(`import {existsSync as recoveryExists} from 'node:fs';
import {DatabaseSync as RecoveryDatabase} from 'node:sqlite';
if (process.env.PYRY_APP_RELEASE === '1.0.0' && process.env.PYRY_APP_DATA_DIR === %q) {
 const probe = new RecoveryDatabase(process.env.PYRY_APP_DB_PATH ?? '', {readOnly:true});
 try { if (recoveryExists(process.env.PYRY_APP_DATA_DIR + '/candidate-only') || probe.prepare("SELECT name FROM sqlite_schema WHERE name='candidate_only'").get()) throw new Error('candidate data before previous health'); }
 finally { probe.close(); }
}
`, data)
	testArtifactWrite(t, source, "src/service/main.ts", guard+main)
	o, err := NewOwner(r, node)
	if err != nil {
		t.Fatal(err)
	}
	tools := Toolchain{Node: node, NPM: npm}
	if result, e := o.Publish(context.Background(), testHost, testApp, tools); e != nil {
		t.Fatal(result, e)
	}
	target := healthTarget(t, o)
	preference := `{"version":1,"selected_tab":"preferences","period":"7d"}`
	req, _ := http.NewRequest("POST", "http://caller.invalid/api/preferences", strings.NewReader(preference))
	resp, err := target.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal(resp.StatusCode)
	}
	if err = o.Shutdown(); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(data, "app.sqlite")
	baseline := updateSQL(t, node, db)
	baselineRef, err := SnapshotData(context.Background(), r.root, testHost, testApp, "55555555-5555-4555-8555-555555555555", node)
	if err != nil {
		t.Fatal(err)
	}
	m := testMap(t)
	m["release_version"], m["title"] = "2.0.0", "Candidate display title"
	testArtifactWrite(t, source, "app.json", string(testJSON(t, m)))
	testArtifactWrite(t, source, "sql/migrations/0002_candidate.sql", "CREATE TABLE candidate_only (value TEXT); INSERT INTO candidate_only VALUES ('pending');")
	serviceTest := string(testBytes(t, filepath.Join(source, "test/service.test.mjs")))
	serviceTest = strings.Replace(serviceTest, "writeFileSync(join(release, 'package.json')", "rmSync(join(release, 'sql/migrations/0002_candidate.sql'));\n  writeFileSync(join(release, 'package.json')", 1)
	testArtifactWrite(t, source, "test/service.test.mjs", serviceTest)
	candidate, err := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.UpdateManifest(testApp, testJSON(t, m)); err != nil {
		t.Fatal(err)
	}
	for index, arm := range []string{"available SQL restart", "stopped", "invalid schema", "invalid preferences", "missing applied", "changed checksum", "missing migration", "first stopped"} {
		t.Run(arm, func(t *testing.T) {
			// Exclusive ownership has been transferred away from every earlier owner.
			if e := RestoreData(context.Background(), r.root, testHost, testApp, baselineRef, node); e != nil {
				t.Fatal(e)
			}
			mutations := map[string]string{
				"invalid schema":      "ALTER TABLE preferences ADD COLUMN unexpected TEXT;",
				"invalid preferences": "DROP TABLE preferences; CREATE TABLE preferences (id INTEGER PRIMARY KEY, version INTEGER NOT NULL, selected_tab TEXT NOT NULL, period TEXT NOT NULL) STRICT; INSERT INTO preferences VALUES(1,1,'preferences','invalid');",
				"missing applied":     "DELETE FROM pyry_schema_migrations;",
				"changed checksum":    "UPDATE pyry_schema_migrations SET sha256='changed';",
				"missing migration":   "INSERT INTO pyry_schema_migrations VALUES(99,'unknown');",
			}
			if sql := mutations[arm]; sql != "" {
				cmd := exec.Command(node, "--input-type=module", "-e", "import {DatabaseSync} from 'node:sqlite';const d=new DatabaseSync(process.env.PYRY_APP_DB_PATH);d.exec("+fmt.Sprintf("%q", sql)+");d.close();")
				cmd.Env = []string{"PYRY_APP_DB_PATH=" + db}
				if b, e := cmd.CombinedOutput(); e != nil {
					t.Fatal(string(b), e)
				}
			}
			id := fmt.Sprintf("66666666-6666-4666-8666-%012d", index)
			ref, e := SnapshotData(context.Background(), r.root, testHost, testApp, id, node)
			if e != nil {
				t.Fatal(e)
			}
			private := ownerRecord(t, r, testApp)
			private.ActiveRelease = "2.0.0"
			if _, e = candidateCommand(context.Background(), candidate.BuildRoot, serviceEnvironment(private, data, 1), node, []string{"dist/service/migrate.js"}, false); e != nil && arm == "available SQL restart" {
				t.Fatal(e)
			}
			testArtifactWrite(t, data, "candidate-only", "pending")
			if arm == "available SQL restart" && (!strings.Contains(updateSQL(t, node, db), "candidate_only") || !strings.Contains(updateSQL(t, node, db), `"version":2`)) {
				t.Fatal("candidate SQL migration absent")
			}
			v := ownerRecord(t, r, testApp)
			fields := Lifecycle{Title: v.Title, Desired: "stopped", State: "stopped", ActiveRelease: "1.0.0", PendingRelease: "2.0.0"}
			if arm == "available SQL restart" {
				fields.Desired = "available"
				fields.State = "starting"
			}
			previous := &ReleaseReference{testHost, testApp, "1.0.0"}
			if arm == "first stopped" {
				fields.ActiveRelease = ""
				previous = nil
			}
			j := &PublishJournal{Candidate: ReleaseReference{testHost, testApp, "2.0.0"}, Previous: previous, VerifiedBackup: ref}
			if _, e = r.UpdatePublish(testHost, testApp, j, &fields); e != nil {
				t.Fatal(e)
			}
			before := publishDisk(t, r)
			fresh, owner := reopenPublication(t, r, node)
			waitRecoveryWorker(t, owner)
			got := ownerRecord(t, fresh, testApp)
			reject := mutations[arm] != ""
			if (got.PublishJournal != nil) != reject || got.Title != v.Title || got.Title == "Candidate display title" || got.Desired != fields.Desired || got.ActiveRelease != fields.ActiveRelease || got.LastError == nil || got.Revision <= before.Revision {
				t.Fatal(got)
			}
			if arm == "available SQL restart" {
				if got.State != "running" || healthUse(healthTarget(t, owner)) != nil {
					t.Fatal(got)
				}
				if after := updateSQL(t, node, db); after != baseline {
					t.Fatal("schema/preferences/ledger changed", after, baseline)
				}
			} else {
				if got.State != "stopped" || recoveryGeneration(owner, testApp) != nil {
					t.Fatal("stopped recovery spawned", got)
				}
				if _, e = owner.Lookup(testHost, testApp); e == nil {
					t.Fatal("stopped target exposed")
				}
			}
			if _, e = os.Stat(filepath.Join(data, "candidate-only")); !os.IsNotExist(e) {
				t.Fatal("candidate file retained", e)
			}
			if e = owner.Shutdown(); e != nil {
				t.Fatal(e)
			}
			// Clear rejected test evidence only after joining the failed owner.
			r = fresh
			if _, e = r.UpdatePublish(testHost, testApp, nil, nil); e != nil {
				t.Fatal(e)
			}
		})
	}
}
