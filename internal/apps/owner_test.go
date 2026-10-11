package apps

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func ownerRecord(t *testing.T, r *Registry, id string) Record {
	t.Helper()
	rows, _ := r.List()
	for _, v := range rows {
		if v.AppID == id {
			return v
		}
	}
	t.Fatal("missing record")
	return Record{}
}
func ownerBuild(t *testing.T, r *Registry, id, build, release, script string) string {
	t.Helper()
	dir := filepath.Join(r.root, id, "builds", strings.Repeat(build, 64))
	if err := os.MkdirAll(filepath.Join(dir, "dist/service"), 0700); err != nil {
		t.Fatal(err)
	}
	m := testMap(t)
	m["app_id"] = id
	m["release_version"] = release
	if err := os.WriteFile(filepath.Join(dir, "app.json"), testJSON(t, m), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dist/service/main.js"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
func ownerFixture(t *testing.T) (*Registry, *Owner) {
	t.Helper()
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	v := ownerRecord(t, r, testApp)
	_, err := r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: v.Title, Desired: "stopped", State: "stopped", ActiveRelease: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	node := os.Getenv("HOSTED_APP_NODE")
	if node == "" {
		node = writeNodeDouble(t, "v24.15.0", false)
	}
	o, err := NewOwner(r, node)
	if err != nil {
		t.Fatal(err)
	}
	o.grace = 100 * time.Millisecond
	t.Cleanup(func() {
		if err := o.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	return r, o
}
func waitFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, e := os.ReadFile(path)
		if e == nil {
			return b
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("child signal timed out", path)
	return nil
}

const ownerService = `const fs=require('node:fs'),net=require('node:net');const server=net.createServer(s=>s.end());process.on('SIGTERM',()=>server.close(()=>process.exit(0)));server.listen(Number(process.env.PYRY_APP_PORT),process.env.PYRY_APP_BIND_HOST,()=>{fs.writeFileSync(process.env.PYRY_APP_DATA_DIR+'/launch-'+process.pid,'started');fs.writeFileSync(process.env.PYRY_APP_DATA_DIR+'/ready',JSON.stringify({cwd:process.cwd(),argv:process.argv,env:process.env,bytes:fs.readFileSync(process.env.PYRY_APP_DB_PATH,'utf8')}));});`

func TestConditionalObservation(t *testing.T) {
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	old := ownerRecord(t, r, testApp)
	events := 0
	r.Subscribe(func(Change) { events++ })
	testRegister(t, r, testInput(testID(9)))
	got, changed, err := r.CommitObservation(testHost, testApp, old.Revision, Observation{State: "starting"})
	if err != nil || !changed || got.Title != old.Title {
		t.Fatal(got, changed, err)
	}
	before := testBytes(t, filepath.Join(r.root, "registry.json"))
	count := events
	_, changed, err = r.CommitObservation(testHost, testApp, old.Revision, Observation{Desired: "stopped", State: "failed"})
	if !errors.Is(err, ErrStale) || changed || events != count || !reflect.DeepEqual(before, testBytes(t, filepath.Join(r.root, "registry.json"))) {
		t.Fatal("stale commit", changed, err, events)
	}
}
func TestOwnerSupportedNode(t *testing.T) {
	node := os.Getenv("HOSTED_APP_NODE")
	if node == "" {
		selected, e := exec.LookPath("node")
		if e != nil {
			t.Skip("set HOSTED_APP_NODE to execute supported-runtime acceptance")
		}
		command := exec.Command(selected, "--version")
		command.Env = []string{}
		version, e := command.Output()
		if e != nil || !supportedNode(strings.TrimSpace(string(version))) {
			t.Skip("set HOSTED_APP_NODE to execute supported-runtime acceptance; an unsupported or skipped arm proves no launch")
		}
		node = selected
	}
	t.Setenv("HOSTED_APP_NODE", node)
	t.Setenv("NOISE_SECRET", "never inherit")
	t.Setenv("NODE_OPTIONS", "--throw-deprecation")
	r, o := ownerFixture(t)
	dir := ownerBuild(t, r, testApp, "a", "1.0.0", ownerService)
	m := testMap(t)
	m["release_version"] = "2.0.0"
	m["title"] = "Candidate"
	if _, e := r.UpdateManifest(testApp, testJSON(t, m)); e != nil {
		t.Fatal(e)
	}
	data := filepath.Join(r.root, testApp, "data")
	if e := os.MkdirAll(data, 0755); e != nil {
		t.Fatal(e)
	}
	db := filepath.Join(data, "app.sqlite")
	if e := os.WriteFile(db, []byte("retained bytes"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := o.Start(context.Background(), testApp); e != nil {
		t.Fatal(e)
	}
	var proof struct {
		Cwd   string
		Argv  []string
		Env   map[string]string
		Bytes string
	}
	if e := json.Unmarshal(waitFile(t, filepath.Join(data, "ready")), &proof); e != nil {
		t.Fatal(e)
	}
	if proof.Cwd != dir || len(proof.Argv) != 2 || proof.Argv[1] != filepath.Join(dir, "dist/service/main.js") || proof.Bytes != "retained bytes" || len(proof.Env) != 8 || proof.Env["PYRY_APP_RELEASE"] != "1.0.0" || proof.Env["NODE_ENV"] != "production" || proof.Env["PYRY_APP_BIND_HOST"] != "127.0.0.1" {
		t.Fatal(proof)
	}
	for key, value := range map[string]string{"PYRY_APP_ID": testApp, "PYRY_SERVER_ID": testHost, "PYRY_APP_DATA_DIR": data, "PYRY_APP_DB_PATH": db} {
		if proof.Env[key] != value {
			t.Fatal(key, proof.Env)
		}
	}
	port, e := strconv.Atoi(proof.Env["PYRY_APP_PORT"])
	if e != nil || port != o.generations[testApp].port {
		t.Fatal("invalid port", port, e)
	}
	conn, e := net.DialTimeout("tcp4", "127.0.0.1:"+proof.Env["PYRY_APP_PORT"], time.Second)
	if e != nil {
		t.Fatal("service did not bind released socket", e)
	}
	_ = conn.Close()
	for path, mode := range map[string]os.FileMode{data: 0700, db: 0600} {
		s, e := os.Stat(path)
		if e != nil || s.Mode().Perm() != mode {
			t.Fatal(path, s, e)
		}
	}
	v := ownerRecord(t, r, testApp)
	if v.State != "starting" || v.Title != "App" || v.Desired != "available" || v.ActiveRelease != "1.0.0" {
		t.Fatal(v)
	}
	if e := o.Stop(testApp); e != nil {
		t.Fatal(e)
	}
	if e := o.Stop(testApp); e != nil {
		t.Fatal(e)
	}
	v = ownerRecord(t, r, testApp)
	if v.State != "stopped" || v.Desired != "stopped" {
		t.Fatal(v)
	}
}
func TestOwnerDuplicateAndIsolation(t *testing.T) {
	r, o := ownerFixture(t)
	ownerBuild(t, r, testApp, "a", "1.0.0", ownerService)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := o.Start(context.Background(), testApp); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	first := o.generations[testApp]
	if e := o.Start(context.Background(), testApp); e != nil {
		t.Fatal(e)
	}
	if first != o.generations[testApp] {
		t.Fatal("duplicate generation")
	}
	waitFile(t, filepath.Join(r.root, testApp, "data/ready"))
	launches, e := filepath.Glob(filepath.Join(r.root, testApp, "data/launch-*"))
	if e != nil || len(launches) != 1 {
		t.Fatal("duplicate children", launches, e)
	}
	other := testID(9)
	testRegister(t, r, testInput(other))
	v := ownerRecord(t, r, other)
	_, e = r.UpdateLifecycle(testHost, other, Lifecycle{Title: v.Title, Desired: "available", State: "stopped", ActiveRelease: "1.0.0"})
	if e != nil {
		t.Fatal(e)
	}
	ownerBuild(t, r, other, "a", "1.0.0", ownerService)
	if e = o.Start(context.Background(), other); e != nil {
		t.Fatal(e)
	}
	waitFile(t, filepath.Join(r.root, other, "data/ready"))
	sibling := o.generations[other]
	if sibling.port == first.port {
		t.Fatal("shared port")
	}
	if e = o.Stop(testApp); e != nil {
		t.Fatal(e)
	}
	select {
	case <-sibling.done:
		t.Fatal("sibling retired")
	default:
	}
}
