package apps

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestCommittedTemplateRecovery(t *testing.T) {
	node, npm := os.Getenv("HOSTED_APP_NODE"), os.Getenv("HOSTED_APP_NPM_CLI")
	if node == "" || npm == "" {
		t.Skip("supported real Node/npm required; skips supply no acceptance evidence")
	}
	for _, args := range [][]string{{"--version"}, {npm, "--version"}} {
		b, e := exec.Command(node, args...).Output()
		if e != nil {
			t.Fatal(e)
		}
		t.Logf("runtime %s: %s", strings.Join(args, " "), strings.TrimSpace(string(b)))
	}
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	t.Cleanup(func() {
		for _, release := range ownerRecord(t, r, testApp).ReleaseHistory {
			testWritableBuild(t, release.BuildRoot)
		}
	})
	files, e := templateFiles(ownerRecord(t, r, testApp).Manifest)
	if e != nil {
		t.Fatal(e)
	}
	if e = writeSource(context.Background(), r.root, testApp, files); e != nil {
		t.Fatal(e)
	}
	tools := Toolchain{Node: node, NPM: npm}
	source, data := filepath.Join(r.root, testApp, "source"), filepath.Join(r.root, testApp, "data")
	db := filepath.Join(data, "app.sqlite")
	m := testMap(t)
	m["release_version"], m["title"] = "2.0.0", "Committed candidate title"
	testArtifactWrite(t, source, "app.json", string(testJSON(t, m)))
	testArtifactWrite(t, source, "sql/migrations/0002_candidate.sql", "CREATE TABLE candidate_only (value TEXT); INSERT INTO candidate_only VALUES ('committed row');")
	witness := filepath.Join(t.TempDir(), "migrator-runs")
	migrator := string(testBytes(t, filepath.Join(source, "src/service/migrate.ts")))
	testArtifactWrite(t, source, "src/service/migrate.ts", fmt.Sprintf("import {appendFileSync,writeFileSync} from 'node:fs';\nif (process.env.PYRY_APP_DATA_DIR === %q) {appendFileSync(%q,'run\\n');writeFileSync(process.env.PYRY_APP_DATA_DIR + '/candidate-only','committed file');}\n", data, witness)+migrator)
	serviceTest := string(testBytes(t, filepath.Join(source, "test/service.test.mjs")))
	serviceTest = strings.Replace(serviceTest, "writeFileSync(join(release, 'package.json')", "rmSync(join(release, 'sql/migrations/0002_candidate.sql'));\n  writeFileSync(join(release, 'package.json')", 1)
	testArtifactWrite(t, source, "test/service.test.mjs", serviceTest)
	o, e := NewOwner(r, node)
	if e != nil {
		t.Fatal(e)
	}
	defer o.Shutdown()
	r.mu.Lock()
	activated := false
	r.persist = func(root string, s snapshot) error {
		if s.Records[0].PublishJournal != nil && s.Records[0].PublishJournal.ActivationCommitted {
			activated = true
		}
		if activated && s.Records[0].PublishJournal == nil {
			return errors.New("injected finalization failure")
		}
		return writeSnapshot(root, s)
	}
	r.mu.Unlock()
	result, e := o.Publish(context.Background(), testHost, testApp, tools)
	var failure *PublishFailure
	if !errors.As(e, &failure) || failure.Phase != "finalization" {
		t.Fatal("publication finalization not interrupted", result, e)
	}
	preference := `{"version":1,"selected_tab":"preferences","period":"7d"}`
	req, _ := http.NewRequest("POST", "http://caller.invalid/api/preferences", strings.NewReader(preference))
	resp, e := healthTarget(t, o).Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal(resp.StatusCode)
	}
	_ = o.Shutdown()
	committed := publishDisk(t, r)
	if committed.Records[0].PublishJournal == nil || !committed.Records[0].PublishJournal.ActivationCommitted {
		t.Fatal(committed)
	}
	evidence := updateSQL(t, node, db)
	if !strings.Contains(evidence, `"version":2`) || !strings.Contains(evidence, "candidate_only") || !strings.Contains(evidence, `"period":"7d"`) {
		t.Fatal("candidate migration absent", evidence)
	}
	runs := string(testBytes(t, witness))
	if runs != "run\n" {
		t.Fatal("candidate migration witness", runs)
	}
	query := func(sql string) string {
		b, err := exec.Command(node, "--input-type=module", "-e", fmt.Sprintf("import {DatabaseSync} from 'node:sqlite';const d=new DatabaseSync(%q);console.log(JSON.stringify(d.prepare(%q).all()));d.close();", db, sql)).Output()
		if err != nil {
			t.Fatal(string(b), err)
		}
		return string(b)
	}
	row := query("SELECT * FROM candidate_only")
	for _, arm := range []string{"available SQL restart", "stopped", "invalid schema", "invalid preferences", "missing applied", "changed checksum", "missing migration", "damaged database"} {
		t.Run(arm, func(t *testing.T) {
			// Re-admit retained evidence after each joined owner without resetting revisions.
			fresh, err := Open(r.root, testHost)
			if err != nil {
				t.Fatal(err)
			}
			v := ownerRecord(t, fresh, testApp)
			if v.PublishJournal == nil {
				if _, err = fresh.UpdatePublish(testHost, testApp, committed.Records[0].PublishJournal, nil); err != nil {
					t.Fatal(err)
				}
			}
			if arm != "available SQL restart" {
				if _, err = fresh.UpdateLifecycle(testHost, testApp, Lifecycle{Title: v.Title, Desired: "stopped", State: "stopped", ActiveRelease: v.ActiveRelease, LastError: &LastError{"app.unavailable", "Interrupted"}}); err != nil {
					t.Fatal(err)
				}
			}
			before := publishDisk(t, fresh)
			mutations := map[string]string{
				"invalid schema":      "ALTER TABLE preferences ADD COLUMN unexpected TEXT;",
				"invalid preferences": "PRAGMA ignore_check_constraints=ON; UPDATE preferences SET period='invalid';",
				"missing applied":     "DELETE FROM pyry_schema_migrations WHERE version=2;",
				"changed checksum":    "UPDATE pyry_schema_migrations SET sha256='changed' WHERE version=2;",
				"missing migration":   "INSERT INTO pyry_schema_migrations VALUES(99,'unknown');",
			}
			// Save the exact candidate SQL bytes solely to reset independent reject arms.
			original := testBytes(t, db)
			if sql := mutations[arm]; sql != "" {
				b, e := exec.Command(node, "--input-type=module", "-e", fmt.Sprintf("import {DatabaseSync} from 'node:sqlite';const d=new DatabaseSync(%q);d.exec(%q);d.close();", db, sql)).CombinedOutput()
				if e != nil {
					t.Fatal(string(b), e)
				}
			}
			if arm == "damaged database" {
				testArtifactWrite(t, data, "app.sqlite", "damaged")
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unpark := func() { once.Do(func() { close(release) }) }
			owner, err := newOwner(fresh, node, backupOps{hook: func(step string) error {
				if step == "recovery-validated" {
					close(entered)
					<-release
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Shutdown()
			defer unpark()
			reject := mutations[arm] != "" || arm == "damaged database"
			if !reject {
				<-entered
				assertRecoveryBarred(t, owner, "2.0.0")
				if got := ownerRecord(t, fresh, testApp); got.PublishJournal == nil || got.Title != "Committed candidate title" {
					t.Fatal(got)
				}
				if arm == "available SQL restart" {
					g := recoveryGeneration(owner, testApp)
					if g == nil {
						t.Fatal("private candidate absent")
					}
					owner.mu.Lock()
					private := *g
					owner.mu.Unlock()
					client := privateClient(nil)
					healthy := probeHealth(context.Background(), client, &private)
					client.CloseIdleConnections()
					if !healthy || private.record.ActiveRelease != "2.0.0" {
						t.Fatal("matching candidate health missing before admission")
					}
				} else if recoveryGeneration(owner, testApp) != nil {
					t.Fatal("stopped validator started service")
				}
				unpark()
			}
			waitRecoveryWorker(t, owner)
			got := ownerRecord(t, fresh, testApp)
			if (got.PublishJournal != nil) != reject || got.Title != "Committed candidate title" || got.ActiveRelease != "2.0.0" || got.Desired != before.Records[0].Desired || got.Revision <= before.Revision || !reject && got.LastError != nil || reject && got.LastError == nil {
				t.Fatal(got)
			}
			if arm == "available SQL restart" {
				if got.State != "running" || healthUse(healthTarget(t, owner)) != nil {
					t.Fatal(got)
				}
			} else if _, err = owner.Lookup(testHost, testApp); err == nil {
				t.Fatal("stopped/rejected target")
			}
			if string(testBytes(t, witness)) != runs {
				t.Fatal("migration reran on reopen")
			}
			if string(testBytes(t, filepath.Join(data, "candidate-only"))) != "committed file" {
				t.Fatal("candidate file changed")
			}
			if !reject && (updateSQL(t, node, db) != evidence || query("SELECT * FROM candidate_only") != row) {
				t.Fatal("schema/preferences/ledger/candidate row changed")
			}
			if e := owner.Shutdown(); e != nil {
				t.Fatal(e)
			}
			r = fresh
			if reject {
				if e := os.WriteFile(db, original, 0600); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestCommittedTemplateUpdateSnapshot(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("blocked on #3415: initialized SQLite snapshot subprocess cwd fails on macOS")
	}
	node := os.Getenv("HOSTED_APP_NODE")
	if node == "" {
		t.Skip("supported real Node required")
	}
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	data := filepath.Join(r.root, testApp, "data")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "--input-type=module", "-e", fmt.Sprintf("import {DatabaseSync} from 'node:sqlite';const d=new DatabaseSync(%q);d.exec('CREATE TABLE saved(value TEXT)');d.prepare('INSERT INTO saved VALUES (?)').run('retained');d.close();", filepath.Join(data, "app.sqlite")))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(b), err)
	}
	ref, err := SnapshotData(context.Background(), r.root, testHost, testApp, "88888888-8888-4888-8888-888888888888", node)
	if err != nil {
		t.Fatal(err)
	}
	if err = VerifyBackup(context.Background(), r.root, testHost, testApp, ref, node); err != nil {
		t.Fatal(err)
	}
}
