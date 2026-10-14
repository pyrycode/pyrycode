package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func publishFixture(t *testing.T, reply healthReply, migration string) (*Registry, *Owner, Toolchain, candidateDouble, string) {
	t.Helper()
	r, tools, c, config := testCandidate(t)
	b, _ := json.Marshal(reply)
	testArtifactWrite(t, c.Fixture, "dist/service/main.js", string(b))
	testArtifactWrite(t, c.Fixture, "dist/service/migrate.js", migration)
	binary, _ := os.Executable()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	wrapper := fmt.Sprintf("#!/bin/sh\nexport GORACE=atexit_sleep_ms=0\ncase \"$1\" in\ndist/service/main.js) exec %s -test.run '^TestHealthHelperProcess$' -- \"$@\";;\ndist/service/migrate.js) exec %s -test.run '^TestPublishMigrationHelperProcess$' -- \"$@\";;\nesac\nexec %s -test.run '^TestCandidateHelperProcess$' -- --candidate %s \"$@\"\n", quote(binary), quote(binary), quote(binary), quote(config))
	if e := os.WriteFile(tools.Node, []byte(wrapper), 0700); e != nil {
		t.Fatal(e)
	}
	o, e := NewOwner(r, tools.Node)
	if e != nil {
		t.Fatal(e)
	}
	o.grace = 30 * time.Millisecond
	o.startup = 250 * time.Millisecond
	o.startInterval = 10 * time.Millisecond
	o.requestTimeout = 50 * time.Millisecond
	t.Cleanup(func() { o.Shutdown() })
	return r, o, tools, c, config
}

func TestPublishMigrationHelperProcess(t *testing.T) {
	if os.Getenv("PYRY_APP_DATA_DIR") == "" || !strings.Contains(strings.Join(os.Args, " "), "dist/service/migrate.js") {
		return
	}
	mode, _ := os.ReadFile("dist/service/migrate.js")
	data := os.Getenv("PYRY_APP_DATA_DIR")
	port, e := strconv.Atoi(os.Getenv("PYRY_APP_PORT"))
	if e != nil || port < 1 || port > 65535 || os.Getenv("PYRY_APP_BIND_HOST") != "127.0.0.1" || os.Getenv("NODE_ENV") != "production" || os.Getenv("PYRY_APP_DB_PATH") != filepath.Join(data, "app.sqlite") {
		os.Exit(97)
	}
	if publishOwnerPID(filepath.Join(data, "migration")) != nil {
		os.Exit(99)
	}
	if string(mode) == "fork" {
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "-test.run", "^TestOwnerHelperProcess$", "--", "descendant", "forced")
		cmd.Env = os.Environ()
		if cmd.Start() != nil {
			os.Exit(96)
		}
		for {
			if _, e := os.Stat(filepath.Join(data, "descendant")); e == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	if string(mode) == "hold" {
		time.Sleep(20 * time.Second)
	}
	if string(mode) == "fail" {
		os.Exit(4)
	}
	os.Exit(0)
}

func publishDisk(t *testing.T, r *Registry) snapshot {
	t.Helper()
	var s snapshot
	if e := json.Unmarshal(testBytes(t, filepath.Join(r.root, "registry.json")), &s); e != nil {
		t.Fatal(e)
	}
	return s
}

func TestPublishSuccess(t *testing.T) {
	r, o, tools, _, _ := publishFixture(t, healthReply{Body: healthyBody()}, "ok")
	m := testMap(t)
	m["title"] = "Candidate"
	testArtifactWrite(t, filepath.Join(r.root, testApp, "source"), "app.json", string(testJSON(t, m)))
	before := ownerRecord(t, r, testApp)
	result, e := o.Publish(context.Background(), testHost, testApp, tools)
	if e != nil {
		t.Fatal(e)
	}
	after := ownerRecord(t, r, testApp)
	if !result.Healthy || !result.ActivationCommitted || result.RecoveryPending || result.ServerID != testHost || result.AppID != testApp || result.CandidateRelease != "1.0.0" || result.Lifecycle.ActiveRelease != "1.0.0" || after.Title != "Candidate" || result.Revision != after.Revision || after.Revision <= before.Revision || after.State != "running" || after.PendingRelease != "" || after.PublishJournal != nil {
		t.Fatal(result, after)
	}
	if publishDisk(t, r).Records[0].ActiveRelease != "1.0.0" {
		t.Fatal("activation not durable")
	}
	if e := healthUse(healthTarget(t, o)); e != nil {
		t.Fatal(e)
	}
	bytes, _ := json.Marshal(result)
	if strings.Contains(string(bytes), r.root) || strings.Contains(string(bytes), "BuildRoot") {
		t.Fatal("private result", string(bytes))
	}
}

func TestPublishAdmission(t *testing.T) {
	for _, why := range []string{"unknown", "foreign", "removed", "active", "journal", "toolchain", "cancelled"} {
		t.Run(why, func(t *testing.T) {
			r, o, tools, c, _ := publishFixture(t, healthReply{Body: healthyBody()}, "ok")
			server, app := testHost, testApp
			ctx := context.Background()
			switch why {
			case "unknown":
				app = testID(9)
			case "foreign":
				server = testID(9)
			case "removed":
				if _, e := r.Remove(app); e != nil {
					t.Fatal(e)
				}
			case "active":
				if _, e := r.UpdateLifecycle(server, app, Lifecycle{Title: "App", Desired: "stopped", State: "stopped", ActiveRelease: "1.0.0"}); e != nil {
					t.Fatal(e)
				}
			case "journal":
				if _, e := r.UpdatePublish(server, app, testJournal(r, app, false), nil); e != nil {
					t.Fatal(e)
				}
			case "toolchain":
				tools.Node = "/private/SECRET"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := testBytes(t, filepath.Join(r.root, "registry.json"))
			if _, e := o.Publish(ctx, server, app, tools); e == nil {
				t.Fatal("admitted", why)
			}
			if _, e := os.Stat(c.Log); !os.IsNotExist(e) {
				t.Fatal("rejected admission built", e)
			}
			if string(testBytes(t, filepath.Join(r.root, "registry.json"))) != string(before) {
				t.Fatal("rejected admission changed registry")
			}
		})
	}
}

func TestPublishRetry(t *testing.T) {
	r, o, tools, c, _ := publishFixture(t, healthReply{Body: `{}`}, "ok")
	if _, e := o.Publish(context.Background(), testHost, testApp, tools); e == nil {
		t.Fatal("bad health accepted")
	}
	first := ownerRecord(t, r, testApp)
	if first.PublishJournal != nil || first.State != "failed" {
		t.Fatal(first)
	}
	o.startup = 3 * time.Second
	done := make(chan error, 1)
	go func() { _, e := o.Publish(context.Background(), testHost, testApp, tools); done <- e }()
	waitFile(t, filepath.Join(r.root, testApp, "data/ready"))
	healthWrite(t, r, healthReply{Body: healthyBody()})
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	second := ownerRecord(t, r, testApp)
	if len(second.ReleaseHistory) != 1 || second.ReleaseHistory[0] != first.ReleaseHistory[0] || second.Revision <= first.Revision || second.PublishJournal != nil {
		t.Fatal(second)
	}
	if !strings.Contains(string(testBytes(t, c.Log)), "run build") {
		t.Fatal("retry did not perform checks")
	}
}

func TestPublishMigrationGroup(t *testing.T) {
	r, o, tools, _, _ := publishFixture(t, healthReply{Body: healthyBody()}, "fork")
	if _, e := o.Publish(context.Background(), testHost, testApp, tools); e != nil {
		t.Fatal(e)
	}
	pid, e := strconv.Atoi(string(waitFile(t, filepath.Join(r.root, testApp, "data/descendant"))))
	if e != nil || pid < 1 {
		t.Fatal(pid, e)
	}
	waitGone(t, pid)
	if e := healthUse(healthTarget(t, o)); e != nil {
		t.Fatal(e)
	}
}

func TestPublishContention(t *testing.T) {
	r, o, tools, c, _ := publishFixture(t, healthReply{Body: `{}`}, "ok")
	o.startup = 3 * time.Second
	// Source metadata belongs only to the candidate until activation.
	m := testMap(t)
	m["title"] = "Candidate"
	testArtifactWrite(t, filepath.Join(r.root, testApp, "source"), "app.json", string(testJSON(t, m)))
	done := make(chan error, 1)
	go func() { _, e := o.Publish(context.Background(), testHost, testApp, tools); done <- e }()
	waitFile(t, filepath.Join(r.root, testApp, "data/ready"))
	v := ownerRecord(t, r, testApp)
	if v.Title != "App" || v.ActiveRelease != "" || v.PendingRelease != "1.0.0" || v.State != "starting" || v.PublishJournal == nil || v.PublishJournal.ActivationCommitted {
		t.Fatal(v)
	}
	if _, e := o.Lookup(testHost, testApp); e == nil {
		t.Fatal("private candidate exposed")
	}
	log := testBytes(t, c.Log)
	if _, e := o.Publish(context.Background(), testHost, testApp, tools); e == nil {
		t.Fatal("overlap admitted")
	}
	if e := o.Start(context.Background(), testApp); e == nil {
		t.Fatal("start bypassed publisher")
	}
	if string(testBytes(t, c.Log)) != string(log) {
		t.Fatal("rejected publisher built")
	}
	other, e := r.Create(context.Background(), "Sibling")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		history := ownerRecord(t, r, other.AppID).ReleaseHistory
		for _, release := range history {
			testWritableBuild(t, release.BuildRoot)
		}
	})
	body := strings.Replace(healthyBody(), testApp, other.AppID, 1)
	b, _ := json.Marshal(healthReply{Body: body})
	testArtifactWrite(t, c.Fixture, "dist/service/main.js", string(b))
	if _, e = o.Publish(context.Background(), testHost, other.AppID, tools); e != nil {
		t.Fatal("independent publication", e)
	}
	if e = o.Stop(testApp); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil {
		t.Fatal("cancelled candidate activated")
	}
	if _, e = o.Lookup(testHost, other.AppID); e != nil {
		t.Fatal("sibling retired", e)
	}
}

func TestPublishOrdering(t *testing.T) {
	r, o, tools, _, _ := publishFixture(t, healthReply{Body: healthyBody()}, "ok")
	journalSeen, activationSeen := false, false
	var violation string
	r.mu.Lock()
	r.persist = func(root string, s snapshot) error {
		v := s.Records[0]
		_, dataErr := os.Stat(filepath.Join(root, testApp, "data"))
		if !journalSeen && !os.IsNotExist(dataErr) {
			violation = "data prepared before durable journal"
		}
		if v.PublishJournal != nil && !v.PublishJournal.ActivationCommitted {
			if e := VerifyBackup(context.Background(), root, testHost, testApp, v.PublishJournal.VerifiedBackup, tools.Node); e != nil {
				violation = "journal backup not verified"
			}
			journalSeen = true
		}
		if v.ActiveRelease != "" {
			if !activationSeen && (v.State != "running" || v.PendingRelease != "" || v.LastError != nil || v.PublishJournal == nil || !v.PublishJournal.ActivationCommitted || v.PublishJournal.Previous != nil) {
				violation = "non-atomic activation"
			}
			activationSeen = true
		}
		return writeSnapshot(root, s)
	}
	r.mu.Unlock()
	if _, e := o.Publish(context.Background(), testHost, testApp, tools); e != nil {
		t.Fatal(e)
	}
	if violation != "" || !journalSeen || !activationSeen {
		t.Fatal(violation, journalSeen, activationSeen)
	}
}

func TestPublishRemovalEvidence(t *testing.T) {
	r, o, tools, _, _ := publishFixture(t, healthReply{Body: `{}`, Descendant: true}, "ok")
	o.startup = 3 * time.Second
	done := make(chan error, 1)
	go func() { _, e := o.Publish(context.Background(), testHost, testApp, tools); done <- e }()
	data := filepath.Join(r.root, testApp, "data")
	pid, _ := strconv.Atoi(string(waitFile(t, filepath.Join(data, "ready"))))
	if e := os.Symlink("/private/SECRET", filepath.Join(data, "unsafe")); e != nil {
		t.Fatal(e)
	}
	if changed, e := r.Remove(testApp); e == nil || changed {
		t.Fatal("unresolved journal removed", changed, e)
	}
	if e := <-done; e == nil {
		t.Fatal("removed candidate activated")
	}
	waitGone(t, pid)
	v := publishDisk(t, r).Records[0]
	if v.PublishJournal == nil || v.Desired != "stopped" || v.LastError == nil || v.LastError.Code != "app.io_failed" {
		t.Fatal(v)
	}
	if _, e := o.Publish(context.Background(), testHost, testApp, tools); e == nil {
		t.Fatal("unresolved recovery admitted")
	}
	if e := o.Start(context.Background(), testApp); e == nil {
		t.Fatal("recovery start admitted")
	}
}

func TestPublishFailures(t *testing.T) {
	for _, phase := range []string{"build", "journal", "migration", "migration deadline", "health", "activation", "finalization", "restore", "failure finalization"} {
		t.Run(phase, func(t *testing.T) {
			migration := "ok"
			if phase == "migration" || phase == "restore" || phase == "failure finalization" {
				migration = "fail"
			}
			if phase == "migration deadline" {
				migration = "hold"
			}
			reply := healthReply{Body: healthyBody(), Descendant: true}
			if phase == "health" {
				reply.Body = `{}`
			}
			r, o, tools, c, config := publishFixture(t, reply, migration)
			if phase == "build" {
				c.Fail = "run build"
				testCandidateConfig(t, config, c)
			}
			testArtifactWrite(t, r.root, testApp+"/data/sentinel", "before")
			o.migrateTimeout = 100 * time.Millisecond
			r.mu.Lock()
			r.persist = func(root string, s snapshot) error {
				v := s.Records[0]
				if phase == "journal" && v.PublishJournal != nil || phase == "activation" && v.ActiveRelease != "" || phase == "finalization" && v.ActiveRelease != "" && v.PublishJournal == nil || phase == "failure finalization" && v.State == "failed" && v.PublishJournal == nil {
					return fmt.Errorf("SECRET %s", root)
				}
				if phase == "restore" && v.PublishJournal != nil {
					// A forbidden current-data entry makes real restoration fail.
					os.Symlink("/private/SECRET", filepath.Join(root, testApp, "data/unsafe"))
				}
				return writeSnapshot(root, s)
			}
			r.mu.Unlock()
			result, e := o.Publish(context.Background(), testHost, testApp, tools)
			publishErr := e
			if e == nil || strings.Contains(e.Error(), "SECRET") || strings.Contains(e.Error(), r.root) {
				t.Fatal(result, e)
			}
			v := publishDisk(t, r).Records[0]
			if phase == "finalization" {
				if !result.ActivationCommitted || !result.RecoveryPending || v.ActiveRelease != "1.0.0" || v.PublishJournal == nil || !v.PublishJournal.ActivationCommitted {
					t.Fatal(result, v)
				}
				if _, e := os.Stat(filepath.Join(r.root, testApp, "data/migration")); e != nil {
					t.Fatal("committed data restored", e)
				}
				return
			}
			if v.ActiveRelease != "" || v.State != "failed" {
				t.Fatal(v)
			}
			if _, e = o.Lookup(testHost, testApp); e == nil {
				t.Fatal("failure exposed target")
			}
			if phase == "restore" || phase == "failure finalization" {
				if !result.RecoveryPending || v.PublishJournal == nil {
					t.Fatal(result, v)
				}
				if phase == "restore" && v.LastError.Code != "app.io_failed" {
					t.Fatal(v)
				}
			} else {
				if v.PublishJournal != nil {
					t.Fatal("completed recovery journal", v)
				}
				if string(testBytes(t, filepath.Join(r.root, testApp, "data/sentinel"))) != "before" {
					t.Fatal("data lost")
				}
				if _, e := os.Stat(filepath.Join(r.root, testApp, "data/migration")); !os.IsNotExist(e) {
					t.Fatal("writer output survived recovery")
				}
			}
			if g := recoveryGeneration(o, testApp); g != nil && g.cmd != nil && g.cmd.Process != nil {
				waitGone(t, g.cmd.Process.Pid)
			}
			if phase == "build" {
				var failure *PublishFailure
				if !errors.As(publishErr, &failure) || failure.Phase != "build" || result.RecoveryPending || result.ActivationCommitted || result.Healthy || v.PendingRelease != "" || len(v.ReleaseHistory) != 0 || v.LastError == nil || strings.Contains(v.LastError.Message, "SECRET") || strings.Contains(v.LastError.Message, r.root) {
					t.Fatal("build failure did not finalize safely", result, e, v)
				}
				if _, e := os.Stat(filepath.Join(r.root, testApp, "data/app.sqlite")); !os.IsNotExist(e) {
					t.Fatal("failed build prepared database", e)
				}
				c.Fail = ""
				testCandidateConfig(t, config, c)
				if retry, e := o.Publish(context.Background(), testHost, testApp, tools); e != nil || !retry.ActivationCommitted || !retry.Healthy || retry.RecoveryPending {
					t.Fatal("failed build barred healthy retry", retry, e)
				}
			}
		})
	}
}

func TestPublishRetirement(t *testing.T) {
	for _, point := range []string{"build", "migration", "health"} {
		for _, event := range []string{"stop", "remove", "shutdown", "cancel", "intent", "intent cycle"} {
			t.Run(point+"/"+event, func(t *testing.T) {
				migration := "ok"
				if point == "migration" {
					migration = "hold"
				}
				r, o, tools, c, config := publishFixture(t, healthReply{Body: `{}`, Descendant: true}, migration)
				o.startup = 3 * time.Second
				if point == "build" {
					c.Gate = "run build"
					c.Hold = true
					testCandidateConfig(t, config, c)
				}
				done := make(chan error, 1)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				go func() { _, e := o.Publish(ctx, testHost, testApp, tools); done <- e }()
				path := filepath.Join(r.root, testApp, "data/ready")
				if point == "migration" {
					path = filepath.Join(r.root, testApp, "data/migration")
				}
				if point == "build" {
					path = c.Ready
				}
				pid, _ := strconv.Atoi(string(waitFile(t, path)))
				descendant := 0
				if point == "health" {
					descendant, _ = strconv.Atoi(string(waitFile(t, filepath.Join(r.root, testApp, "data/descendant"))))
				}
				var e error
				switch event {
				case "stop":
					e = o.Stop(testApp)
				case "remove":
					_, e = r.Remove(testApp)
				case "shutdown":
					e = o.Shutdown()
				case "cancel":
					cancel()
				case "intent", "intent cycle":
					v := ownerRecord(t, r, testApp)
					_, e = r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: "New metadata", Desired: "stopped", State: "stopped", PendingRelease: v.PendingRelease})
					if e == nil && event == "intent cycle" {
						_, e = r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: "New metadata", Desired: "available", State: "stopped", PendingRelease: v.PendingRelease})
					}
				}
				if e != nil {
					t.Fatal(e)
				}
				if e = <-done; e == nil {
					t.Fatal("late success")
				}
				if pid > 0 {
					waitGone(t, pid)
				}
				if descendant > 0 {
					waitGone(t, descendant)
				}
				if _, e = o.Lookup(testHost, testApp); e == nil {
					t.Fatal("late target")
				}
				s := publishDisk(t, r)
				if event == "remove" {
					if len(s.Records) != 0 || len(s.Tombstones) != 1 {
						t.Fatal(s)
					}
				} else {
					v := s.Records[0]
					if v.ActiveRelease != "" || v.PublishJournal != nil || event == "stop" && (v.Desired != "stopped" || v.State != "stopped") {
						t.Fatal(v)
					}
					if event == "intent" && (v.Title != "New metadata" || v.Desired != "stopped" || v.State != "stopped") {
						t.Fatal("new intent overwritten", v)
					}
					if event == "intent cycle" && (v.Title != "New metadata" || v.Desired != "available" || v.State != "failed") {
						t.Fatal("reused intent activated", v)
					}
				}
			})
		}
	}
}

func TestPublishStaleActivation(t *testing.T) {
	r, o, tools, _, _ := publishFixture(t, healthReply{Body: `{}`}, "ok")
	o.startup = 3 * time.Second
	done := make(chan error, 1)
	go func() { _, e := o.Publish(context.Background(), testHost, testApp, tools); done <- e }()
	waitFile(t, filepath.Join(r.root, testApp, "data/ready"))
	g := recoveryGeneration(o, testApp)
	if e := o.Stop(testApp); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e == nil {
		t.Fatal("late success")
	}
	before := testBytes(t, filepath.Join(r.root, "registry.json"))
	if e := o.confirmRunning(context.Background(), testApp, g); !errors.Is(e, ErrStale) {
		t.Fatal(e)
	}
	if string(testBytes(t, filepath.Join(r.root, "registry.json"))) != string(before) {
		t.Fatal("late health mutated state")
	}
}

func TestPublishTemplate(t *testing.T) {
	node, npm := os.Getenv("HOSTED_APP_NODE"), os.Getenv("HOSTED_APP_NPM_CLI")
	if node == "" || npm == "" {
		t.Skip("set supported Node/npm; skip proves no template publication")
	}
	r := testOpen(t)
	testRegister(t, r, []byte(testManifest))
	t.Cleanup(func() {
		for _, release := range ownerRecord(t, r, testApp).ReleaseHistory {
			testWritableBuild(t, release.BuildRoot)
		}
	})
	m := ownerRecord(t, r, testApp).Manifest
	files, e := templateFiles(m)
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
	result, e := o.Publish(context.Background(), testHost, testApp, Toolchain{Node: node, NPM: npm})
	if e != nil {
		t.Fatal(result, e)
	}
	want := Lifecycle{Title: m.Title(), Desired: "available", State: "running", ActiveRelease: m.ReleaseVersion()}
	if result.ServerID != testHost || result.AppID != testApp || result.CandidateRelease != m.ReleaseVersion() || result.Lifecycle != want || !result.Healthy || !result.ActivationCommitted || result.RecoveryPending {
		t.Fatal("incomplete template activation", result)
	}
	committed := publishDisk(t, r).Records[0]
	if committed.ActiveRelease != m.ReleaseVersion() || committed.Title != want.Title || committed.Desired != want.Desired || committed.State != want.State || committed.PendingRelease != "" || committed.LastError != nil || committed.PublishJournal != nil || committed.Revision != result.Revision {
		t.Fatal("template activation not durable", committed, result)
	}
	target, e := o.Lookup(testHost, testApp)
	if e != nil {
		t.Fatal(e)
	}
	req, _ := http.NewRequest("GET", "http://caller.invalid/api/preferences", nil)
	response, e := target.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	var preferences struct {
		Version     int    `json:"version"`
		SelectedTab string `json:"selected_tab"`
		Period      string `json:"period"`
	}
	if e := json.NewDecoder(response.Body).Decode(&preferences); e != nil {
		t.Fatal(e)
	}
	if preferences.Version != 1 || preferences.SelectedTab != "overview" || preferences.Period != "24h" {
		t.Fatal("unexpected migrated preferences", preferences)
	}
	cmd := exec.Command(node, "--version")
	version, _ := cmd.Output()
	t.Logf("real Node %s; template migration, publication and API passed", strings.TrimSpace(string(version)))
}
