package apps

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const updateSQLRead = `const d = new DatabaseSync(process.env.PYRY_APP_DB_PATH ?? '', {readOnly:true});
const evidence = JSON.stringify({schema:d.prepare('SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY name').all(),preferences:d.prepare('SELECT * FROM preferences ORDER BY id').all(),ledger:d.prepare('SELECT * FROM pyry_schema_migrations ORDER BY version').all()}); d.close();`

func updateSQL(t *testing.T, node, db string) string {
	t.Helper()
	cmd := exec.Command(node, "--input-type=module", "-e", "import {DatabaseSync} from 'node:sqlite';"+updateSQLRead+"console.log(evidence);")
	cmd.Env = []string{"PYRY_APP_DB_PATH=" + db}
	b, e := cmd.Output()
	if e != nil {
		t.Fatal(e)
	}
	return strings.TrimSpace(string(b))
}

func TestUpdateTemplateSnapshot(t *testing.T) {
	node, npm := os.Getenv("HOSTED_APP_NODE"), os.Getenv("HOSTED_APP_NPM_CLI")
	if node == "" || npm == "" {
		t.Skip("supported real Node/npm required; skips supply no initialized update evidence")
	}
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	t.Cleanup(func() {
		for _, release := range ownerRecord(t, r, testApp).ReleaseHistory {
			testWritableBuild(t, release.BuildRoot)
		}
	})
	files, err := templateFiles(ownerRecord(t, r, testApp).Manifest)
	backupOK(t, err)
	backupOK(t, writeSource(context.Background(), r.root, testApp, files))
	o, err := NewOwner(r, node)
	backupOK(t, err)
	defer o.Shutdown()
	tools := Toolchain{Node: node, NPM: npm}
	_, err = o.Publish(context.Background(), testHost, testApp, tools)
	backupOK(t, err)
	preference := `{"version":1,"selected_tab":"preferences","period":"7d"}`
	req, err := http.NewRequest("POST", "http://caller.invalid/api/preferences", strings.NewReader(preference))
	backupOK(t, err)
	response, err := healthTarget(t, o).Do(req)
	backupOK(t, err)
	backupOK(t, response.Body.Close())
	backupCheck(t, response.StatusCode == 204, response.StatusCode)
	data := filepath.Join(r.root, testApp, "data")
	before := updateSQL(t, node, filepath.Join(data, "app.sqlite"))
	source := filepath.Join(r.root, testApp, "source")
	m := testMap(t)
	m["release_version"], m["title"] = "2.0.0", "Candidate"
	testArtifactWrite(t, source, "app.json", string(testJSON(t, m)))
	testArtifactWrite(t, source, "sql/migrations/0002_candidate.sql", "CREATE TABLE candidate_only(value TEXT); INSERT INTO candidate_only VALUES('new');")
	// Unit fixtures exercise their baseline ledger; publication exercises the new migration.
	serviceTest := string(testBytes(t, filepath.Join(source, "test/service.test.mjs")))
	serviceTest = strings.Replace(serviceTest, "writeFileSync(join(release, 'package.json')", "rmSync(join(release, 'sql/migrations/0002_candidate.sql'));\n  writeFileSync(join(release, 'package.json')", 1)
	testArtifactWrite(t, source, "test/service.test.mjs", serviceTest)
	result, err := o.Publish(context.Background(), testHost, testApp, tools)
	backupOK(t, err)
	backupCheck(t, result.Healthy && result.ActivationCommitted && !result.RecoveryPending && result.Lifecycle.ActiveRelease == "2.0.0", "candidate activation failed", result)
	after := updateSQL(t, node, filepath.Join(data, "app.sqlite"))
	backupCheck(t, strings.Contains(after, "candidate_only") && strings.Contains(after, `"version":2`), "candidate migration missing", after)
	req, err = http.NewRequest("GET", "http://caller.invalid/api/preferences", nil)
	backupOK(t, err)
	response, err = healthTarget(t, o).Do(req)
	backupOK(t, err)
	body, err := io.ReadAll(response.Body)
	backupOK(t, errors.Join(err, response.Body.Close()))
	backupCheck(t, response.StatusCode == 200 && string(body) == preference, "saved preferences lost", string(body))
	backups := filepath.Join(r.root, testApp, "backups")
	entries, err := os.ReadDir(backups)
	backupOK(t, err)
	initialized := 0
	for _, entry := range entries {
		ref := BackupReference{ServerID: testHost, AppID: testApp, PublishID: entry.Name(), Root: filepath.Join(backups, entry.Name())}
		db := filepath.Join(ref.Root, "data/app.sqlite")
		if _, err := os.Stat(db); errors.Is(err, os.ErrNotExist) {
			continue
		}
		backupOK(t, VerifyBackup(context.Background(), r.root, testHost, testApp, ref, node))
		backupCheck(t, updateSQL(t, node, db) == before, "backup does not retain pre-migration schema/preferences/ledger")
		initialized++
	}
	backupCheck(t, initialized == 1, "initialized pre-migration backup missing", initialized)
	for _, args := range [][]string{{"--version"}, {npm, "--version"}} {
		version, err := exec.Command(node, args...).Output()
		backupOK(t, err)
		t.Logf("runtime %s: %s", strings.Join(args, " "), strings.TrimSpace(string(version)))
	}
}

func TestUpdateTemplateRollback(t *testing.T) {
	node, npm := os.Getenv("HOSTED_APP_NODE"), os.Getenv("HOSTED_APP_NPM_CLI")
	if node == "" || npm == "" {
		t.Skip("supported real Node/npm required; skips provide no rollback evidence")
	}
	for _, arm := range []string{"transaction", "startup"} {
		t.Run(arm, func(t *testing.T) {
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
			o, e := NewOwner(r, node)
			if e != nil {
				t.Fatal(e)
			}
			defer o.Shutdown()
			tools := Toolchain{Node: node, NPM: npm}
			if result, e := o.Publish(context.Background(), testHost, testApp, tools); e != nil {
				t.Fatal(result, e)
			}
			target := healthTarget(t, o)
			preference := `{"version":1,"selected_tab":"preferences","period":"7d"}`
			req, _ := http.NewRequest("POST", "http://caller.invalid/api/preferences", strings.NewReader(preference))
			response, e := target.Do(req)
			if e != nil {
				t.Fatal(e)
			}
			response.Body.Close()
			if response.StatusCode != 204 {
				t.Fatal(response.StatusCode)
			}
			data := filepath.Join(r.root, testApp, "data")
			before := updateSQL(t, node, filepath.Join(data, "app.sqlite"))
			source := filepath.Join(r.root, testApp, "source")
			m := testMap(t)
			m["release_version"], m["title"] = "2.0.0", "Candidate"
			testArtifactWrite(t, source, "app.json", string(testJSON(t, m)))
			sql := "CREATE TABLE candidate_only (value TEXT); INSERT INTO candidate_only VALUES ('pending');"
			if arm == "transaction" {
				sql += " INSERT INTO missing_table VALUES (1);"
			}
			testArtifactWrite(t, source, "sql/migrations/0002_candidate.sql", sql)
			witness := filepath.Join(t.TempDir(), "sql-evidence")
			quoted, _ := json.Marshal(witness)
			dataQuoted, _ := json.Marshal(data)
			condition := "process.env.PYRY_APP_DATA_DIR === " + string(dataQuoted)
			migrator := string(testBytes(t, filepath.Join(source, "src/service/migrate.ts")))
			migrator = "import {writeFileSync} from 'node:fs'; import {DatabaseSync} from 'node:sqlite';\nif (" + condition + ") writeFileSync(process.env.PYRY_APP_DATA_DIR + '/candidate-only', 'candidate');\n" + migrator + "\nif (" + condition + ") {" + updateSQLRead + "writeFileSync(" + string(quoted) + ", evidence); }\n"
			testArtifactWrite(t, source, "src/service/migrate.ts", migrator)
			// Template unit fixtures keep their baseline migration set; the actual
			// publisher still checks and executes the candidate's new migration.
			serviceTest := string(testBytes(t, filepath.Join(source, "test/service.test.mjs")))
			serviceTest = strings.Replace(serviceTest, "writeFileSync(join(release, 'package.json')", "rmSync(join(release, 'sql/migrations/0002_candidate.sql'));\n  writeFileSync(join(release, 'package.json')", 1)
			testArtifactWrite(t, source, "test/service.test.mjs", serviceTest)
			if arm == "startup" {
				main := string(testBytes(t, filepath.Join(source, "src/service/main.ts")))
				testArtifactWrite(t, source, "src/service/main.ts", "if ("+condition+") process.exit(9);\n"+main)
			}
			result, e := o.Publish(context.Background(), testHost, testApp, tools)
			var failure *PublishFailure
			wantPhase := "migration"
			if arm == "startup" {
				wantPhase = "health"
			}
			if !errors.As(e, &failure) || failure.Phase != wantPhase || result.Healthy || result.ActivationCommitted || result.RecoveryPending || result.Lifecycle.State != "running" || result.Lifecycle.ActiveRelease != "1.0.0" {
				t.Fatal(result, e)
			}
			observed := string(testBytes(t, witness))
			if arm == "transaction" && observed != before {
				t.Fatal("SQL transaction did not revert pending statements", observed, before)
			}
			if arm == "startup" && (!strings.Contains(observed, "candidate_only") || !strings.Contains(observed, `"version":2`)) {
				t.Fatal("startup did not follow committed schema migration", observed)
			}
			if after := updateSQL(t, node, filepath.Join(data, "app.sqlite")); after != before {
				t.Fatal("restored schema/preferences/checksum ledger differ", after, before)
			}
			if _, e := os.Stat(filepath.Join(data, "candidate-only")); !os.IsNotExist(e) {
				t.Fatal("candidate file retained", e)
			}
			fresh := healthTarget(t, o)
			req, _ = http.NewRequest("GET", "http://caller.invalid/api/preferences", nil)
			response, e = fresh.Do(req)
			if e != nil {
				t.Fatal(e)
			}
			body, e := io.ReadAll(response.Body)
			response.Body.Close()
			if e != nil || string(body) != preference {
				t.Fatal(string(body), e)
			}
			o.mu.Lock()
			g := *fresh.generation
			o.mu.Unlock()
			client := privateClient(nil)
			defer client.CloseIdleConnections()
			if !probeHealth(context.Background(), client, &g) {
				t.Fatal("old-release matching health absent")
			}
		})
	}
}
