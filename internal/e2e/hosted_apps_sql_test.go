//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
)

// The fixture must be built from the maintained template before this proof.
// Explicit inputs fail closed; a default skip supplies no SQL acceptance evidence.
func TestHostedAppsRecoverySQL(t *testing.T) {
	node, fixture := os.Getenv("HOSTED_APP_NODE"), os.Getenv("HOSTED_APP_TEMPLATE_BUILD")
	if node == "" || fixture == "" {
		t.Skip("SQL proof requires HOSTED_APP_NODE and HOSTED_APP_TEMPLATE_BUILD")
	}
	node, err := filepath.Abs(node)
	if err != nil {
		t.Fatal(err)
	}
	version := hostedSQLCommand(t, node, "", nil, "--version")
	var major, minor, patch int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(version)), "v%d.%d.%d", &major, &minor, &patch); err != nil || major != 24 || minor < 15 {
		t.Fatalf("unsupported actual runtime: %s", version)
	}
	t.Logf("SQL proof actual Node %s", bytes.TrimSpace(version))
	home := shortHome(t)
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	pairing, err := paireddevice.Setup(paireddevice.Config{Home: home, InstanceName: "test", Relay: fr.URL() + "/v2/server", DeviceName: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	seeded := hostedSeed(t, home, "SQL proof")
	// Replace only the seeded committed app with the compiled real template.
	build := filepath.Join(hostedRoot(home), hostedAppID, "builds", strings.Repeat("a", 64))
	for _, part := range []string{"dist/service", "sql/migrations", "package.json"} {
		source := filepath.Join(fixture, part)
		err := filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			dest := filepath.Join(build, part, rel)
			if d.IsDir() {
				return os.MkdirAll(dest, 0700)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(dest, b, 0600)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Remove the storage-only sentinel while stopped, migrate, then seed defaults.
	db := filepath.Join(hostedRoot(home), hostedAppID, "data/app.sqlite")
	if err := os.Remove(db); err != nil {
		t.Fatal(err)
	}
	env := []string{"PYRY_APP_ID=" + hostedAppID, "PYRY_SERVER_ID=" + seeded.ServerID, "PYRY_APP_RELEASE=1.0.0", "PYRY_APP_DATA_DIR=" + filepath.Dir(db), "PYRY_APP_DB_PATH=" + db, "PYRY_APP_BIND_HOST=127.0.0.1", "PYRY_APP_PORT=12345", "NODE_ENV=production"}
	hostedSQLCommand(t, node, build, env, "dist/service/migrate.js")
	hostedSQLCommand(t, node, "", nil, "--input-type=module", "-e", `import {DatabaseSync} from 'node:sqlite'; const db=new DatabaseSync(process.argv[1]); db.prepare('INSERT INTO main.preferences VALUES (1,1,?,?)').run('overview','24h'); db.close();`, db)
	// This launcher instruments process ownership; it executes the unchanged service.
	dir := t.TempDir()
	fakeEnv := hostedNodeEnv(t)
	fakeNode := strings.TrimPrefix(strings.Split(fakeEnv[0], ":")[0], "PATH=") + "/node"
	monitor := filepath.Join(dir, "monitor.mjs")
	script := `import {spawn} from 'node:child_process'; import {writeFileSync,renameSync} from 'node:fs';
const child=spawn('/bin/sleep',['99999'],{stdio:'ignore'});
const dir=process.env.PYRY_APP_DATA_DIR;
writeFileSync(dir+'/process.tmp',JSON.stringify({pid:process.pid,descendant:child.pid,port:Number(process.env.PYRY_APP_PORT)})); renameSync(dir+'/process.tmp',dir+'/process.json');
process.once('SIGTERM',()=>child.kill('SIGTERM'));
await new Promise(resolve=>setTimeout(resolve,200));
`
	if err := os.WriteFile(monitor, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	launcher := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then exec '%s' --version; fi\nif [ \"$PYRY_APP_ID\" = '%s' ]; then exec '%s' --import '%s' \"$@\"; else exec '%s' \"$@\"; fi\n", node, hostedAppID, node, monitor, fakeNode)
	if err := os.WriteFile(filepath.Join(dir, "node"), []byte(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	nodeEnv := []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "GORACE=atexit_sleep_ms=0"}
	const initial = "11111111-1111-4111-8111-111111111111"
	h := StartStreamInteractiveWithRelay(t, home, initial, pairing.Relay, nodeEnv...)
	before := hostedRunning(t, home)
	process := hostedProof(t, home, hostedAppID)
	sibling := hostedProof(t, home, before.Records[3].AppID)
	pub, err := base64.StdEncoding.DecodeString(pairing.ServerStaticPubkey)
	if err != nil {
		t.Fatal(err)
	}
	phone, send, recv := dialPairedPhone(t, fr, home, pairing.Token, pub, "phone")
	defer phone.Close()
	seal, next := noiseWire(t, phone, send, recv)
	conv := createConversationOverWire(t, seal, next, 32040)
	chosen := []byte(`{"version":1,"selected_tab":"preferences","period":"7d"}`)
	hostedPreferences(t, process, chosen)
	hostedSQLRead(t, node, db, chosen)
	if err := syscall.Kill(process.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	hostedWait(t, func() bool { s, _ := hostedRead(t, home); return s.Records[0].State == "failed" })
	failedAt := time.Now()
	// Observe recovery concurrently with a real unrelated fake-Claude turn.
	type observation struct {
		state    string
		at       time.Time
		revision uint64
	}
	observed := make(chan []observation, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		var rows []observation
		for {
			select {
			case <-ctx.Done():
				observed <- rows
				return
			default:
			}
			b, err := os.ReadFile(filepath.Join(hostedRoot(home), "registry.json"))
			var s hostedSnapshot
			if err == nil && json.Unmarshal(b, &s) == nil {
				r := s.Records[0]
				if len(rows) == 0 || rows[len(rows)-1].state != r.State {
					rows = append(rows, observation{r.State, time.Now(), r.Revision})
				}
				if r.State == "running" {
					observed <- rows
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	hostedHealthy(t, sibling)
	sendAndAwaitEcho(t, seal, next, "turn during app recovery", 32041, conv, "sql-recovery", "channel usable", "channel usable")
	var rows []observation
	select {
	case rows = <-observed:
	case <-time.After(12 * time.Second):
		t.Fatal("recovery deadline")
	}
	if len(rows) != 3 || rows[0].state != "failed" || rows[1].state != "starting" || rows[2].state != "running" || rows[1].at.Sub(failedAt) < 800*time.Millisecond || rows[1].revision <= rows[0].revision || rows[2].revision <= rows[1].revision {
		t.Fatal("failed/delay/starting/running", rows)
	}
	recovered := hostedProof(t, home, hostedAppID)
	if recovered.PID == process.PID || recovered.Port == process.Port {
		t.Fatal("reused generation", recovered)
	}
	hostedGone(t, process)
	hostedPreferences(t, recovered, nil)
	hostedSQLRead(t, node, db, chosen)
	if got := hostedProof(t, home, before.Records[3].AppID); got != sibling {
		t.Fatal("sibling restarted", got, sibling)
	}
	h.Stop(t)
	if h.cmd.ProcessState.ExitCode() != 0 {
		t.Fatal("daemon shutdown did not join cleanly", h.cmd.ProcessState)
	}
	hostedGone(t, recovered)
	hostedGone(t, sibling)
	stopped, _ := hostedRead(t, home)
	if stopped.Records[0].Desired != "available" || stopped.Records[0].State != "stopped" {
		t.Fatal(stopped)
	}
	h = RestartStreamInteractiveWithRelay(t, home, initial, pairing.Relay, nodeEnv...)
	restarted := hostedRunning(t, home)
	fresh := hostedProof(t, home, hostedAppID)
	if fresh.PID == recovered.PID || restarted.ServerID != seeded.ServerID || restarted.Revision <= stopped.Revision || restarted.Records[0].ActiveRelease != "1.0.0" || restarted.Records[0].Manifest.ReleaseVersion() != "2.0.0" {
		t.Fatal("restart contract", restarted)
	}
	for _, i := range []int{1, 2} {
		if restarted.Records[i].State != "stopped" {
			t.Fatal(restarted)
		}
		if _, err := os.Stat(filepath.Join(hostedRoot(home), restarted.Records[i].AppID, "data/process.json")); !os.IsNotExist(err) {
			t.Fatal("stopped/unpublished launched", err)
		}
	}
	hostedPreferences(t, fresh, nil)
	hostedSQLRead(t, node, db, chosen)
	hostedHealthy(t, hostedProof(t, home, restarted.Records[3].AppID))
	h.Stop(t)
	if h.cmd.ProcessState.ExitCode() != 0 {
		t.Fatal("daemon shutdown did not join cleanly", h.cmd.ProcessState)
	}
	hostedGone(t, fresh)
	hostedGone(t, hostedProof(t, home, restarted.Records[3].AppID))
}
func hostedSQLCommand(t *testing.T, node, dir string, env []string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, args...)
	cmd.Dir = dir
	cmd.Env = env
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Node fixture command: %v: %s", err, b)
	}
	return b
}
func hostedSQLRead(t *testing.T, node, db string, want []byte) {
	t.Helper()
	b := hostedSQLCommand(t, node, "", nil, "--disable-warning=ExperimentalWarning", "--input-type=module", "-e", `import {DatabaseSync} from 'node:sqlite'; const db=new DatabaseSync(process.argv[1],{readOnly:true}); console.log(JSON.stringify(db.prepare('SELECT version,selected_tab,period FROM main.preferences WHERE id=1').get())); db.close();`, db)
	var got, expected any
	if json.Unmarshal(bytes.TrimSpace(b), &got) != nil || json.Unmarshal(want, &expected) != nil || !reflect.DeepEqual(got, expected) {
		t.Fatalf("SQL readback %s", b)
	}
}
func hostedPreferences(t *testing.T, p hostedProcess, save []byte) {
	t.Helper()
	client := http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/api/preferences", p.Port)
	if save != nil {
		res, err := client.Post(url, "application/json", bytes.NewReader(save))
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != 204 {
			t.Fatal(res.Status)
		}
	}
	res, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4096))
	if err != nil || res.StatusCode != 200 || !bytes.Equal(b, []byte(`{"version":1,"selected_tab":"preferences","period":"7d"}`)) {
		t.Fatalf("service preferences: %s %v", b, err)
	}
}
