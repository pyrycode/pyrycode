package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const lifecycleHost = "11111111-1111-4111-8111-111111111111"
const lifecycleApp = "22222222-2222-4222-8222-222222222222"

func testLifecycleRegistry(t *testing.T, host string) (*apps.Registry, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve lifecycle fixture root: %v", err)
	}
	root := filepath.Join(dir, "apps")
	r, err := apps.Open(root, host)
	if err != nil {
		t.Fatal(err)
	}
	return r, root
}
func testLifecycleManifest(host, title string) []byte {
	return []byte(fmt.Sprintf(`{"contract_version":1,"server_id":%q,"app_id":%q,"title":%q,"release_version":"1.0.0","runtime":{"node":">=24.15.0 <25.0.0","sqlite":"node:sqlite"},"frontend":{"root":"dist/web","entry":"index.html"},"service":{"entry":"dist/service/main.js","migrate":"dist/service/migrate.js","health_path":"/_pyry/health","migrations":"sql/migrations"},"build":{"package":"package.json","lockfile":"package-lock.json","install":["npm","ci"],"typecheck":["npm","run","typecheck"],"test":["npm","test"],"compile":["npm","run","build"]}}`, host, lifecycleApp, title))
}
func testLifecyclePeer(t *testing.T, r *apps.Registry, host string, caps []string) *testHostedPeer {
	t.Helper()
	p := testHostedStartCaps(t, r, caps, func(c *V2SessionConfig) { c.ServerID = host })
	unsubscribe := r.Subscribe(p.mgr.HostedAppsChanged)
	t.Cleanup(unsubscribe)
	return p
}
func testLifecycleEvent(t *testing.T, p *testHostedPeer, typ string, revision uint64) map[string]json.RawMessage {
	t.Helper()
	e, obj := p.next()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if e.Type != typ || e.InReplyTo != nil || obj["in_reply_to"] != nil || string(payload["revision"]) != fmt.Sprint(revision) {
		t.Fatalf("unsolicited revision %d: %+v %s", revision, e, e.Payload)
	}
	return payload
}
func TestHostedLifecycleCommittedWire(t *testing.T) {
	r, _ := testLifecycleRegistry(t, lifecycleHost)
	p := testLifecyclePeer(t, r, lifecycleHost, []string{protocol.CapabilityHostedAppsV1})
	if changed, err := r.Register(testLifecycleManifest(lifecycleHost, "Committed")); !changed || err != nil {
		t.Fatal(changed, err)
	}
	got := testLifecycleEvent(t, p, "app_updated", 1)
	if len(got) != 8 || string(got["title"]) != `"Committed"` {
		t.Fatal(got)
	}
	for _, key := range []string{"active_release", "pending_release", "last_error"} {
		if string(got[key]) != "null" {
			t.Fatal(key, got)
		}
	}
	if changed, err := r.UpdateManifest(lifecycleApp, testLifecycleManifest(lifecycleHost, "Candidate")); !changed || err != nil {
		t.Fatal(changed, err)
	}
	got = testLifecycleEvent(t, p, "app_updated", 2)
	if string(got["title"]) != `"Committed"` {
		t.Fatal("candidate escaped", got)
	}
	fields := apps.Lifecycle{Title: "Activated", Desired: "available", State: "running", ActiveRelease: "1.0.0", PendingRelease: "2.0.0", LastError: &apps.LastError{Code: "app.io_failed", Message: "Safe committed error"}}
	if changed, err := r.UpdateLifecycle(lifecycleHost, lifecycleApp, fields); !changed || err != nil {
		t.Fatal(changed, err)
	}
	got = testLifecycleEvent(t, p, "app_updated", 3)
	if string(got["title"]) != `"Activated"` || string(got["active_release"]) != `"1.0.0"` || string(got["pending_release"]) != `"2.0.0"` || string(got["last_error"]) != `{"code":"app.io_failed","message":"Safe committed error"}` {
		t.Fatal(got)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f := fields
			f.Title = fmt.Sprintf("Commit %d", i)
			if changed, err := r.UpdateLifecycle(lifecycleHost, lifecycleApp, f); !changed || err != nil {
				t.Errorf("commit: %v %v", changed, err)
			}
		}(i)
	}
	wg.Wait()
	for revision := uint64(4); revision <= 11; revision++ {
		testLifecycleEvent(t, p, "app_updated", revision)
	}
	if changed, err := r.Remove(lifecycleApp); !changed || err != nil {
		t.Fatal(changed, err)
	}
	got = testLifecycleEvent(t, p, "app_removed", 12)
	if len(got) != 2 || string(got["app_id"]) != fmt.Sprintf("%q", lifecycleApp) {
		t.Fatal(got)
	}
}
func TestHostedLifecycleCommitSuppression(t *testing.T) {
	r, root := testLifecycleRegistry(t, lifecycleHost)
	p := testLifecyclePeer(t, r, lifecycleHost, []string{protocol.CapabilityHostedAppsV1})
	manifest := testLifecycleManifest(lifecycleHost, "Committed")
	if _, err := r.Register(manifest); err != nil {
		t.Fatal(err)
	}
	testLifecycleEvent(t, p, "app_updated", 1)
	if changed, err := r.Register(manifest); changed || err != nil {
		t.Fatal(changed, err)
	}
	if changed, err := r.Register([]byte("{}")); changed || err == nil {
		t.Fatal(changed, err)
	}
	if changed, err := r.UpdateLifecycle("foreign", lifecycleApp, apps.Lifecycle{}); changed || err == nil {
		t.Fatal(changed, err)
	}
	// Replacing the root directory with a file deterministically fails persistence.
	moved := root + "-saved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if changed, err := r.Remove(lifecycleApp); changed || err == nil {
		t.Fatal(changed, err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, root); err != nil {
		t.Fatal(err)
	}
	// A committed list response is the delivery barrier: no event may precede it.
	page := testHostedPage(t, p, 20, nil)
	if page.Revision != 1 || len(page.Items) != 1 || !strings.Contains(page.Items[0].Title, "Committed") {
		t.Fatal(page)
	}
}

func testLifecycleChange(revision uint64) apps.Change {
	return apps.Change{ServerID: v2TestServerID, AppID: lifecycleApp, Revision: revision}
}
func TestHostedLifecycleOverflow(t *testing.T) {
	p, entered, exited, release := testHostedBlockedOutput(t, &testAppDiscovery{})
	other := p.sibling("lifecycle-progress")
	p.request(10, "app_cancel", `{"request_id":999}`)
	testHostedAwait(t, entered)
	// The active control reply consumes one of the same sixteen slots as callbacks.
	for revision := uint64(1); revision <= hostedQueueLimit; revision++ {
		done := make(chan struct{})
		go func() { p.mgr.HostedAppsChanged(testLifecycleChange(revision)); close(done) }()
		testHostedAwait(t, done)
		if revision == hostedQueueLimit-1 {
			p.mgr.hostedMu.Lock()
			in := p.mgr.hostedInboxes[p.conn]
			used := in.used
			overflow := in.overflow
			p.mgr.hostedMu.Unlock()
			if used != hostedQueueLimit || overflow {
				t.Fatalf("combined reservation: used=%d overflow=%v", used, overflow)
			}
		}
		testLifecycleEvent(t, other, "app_removed", revision)
	}
	testHostedGone(t, p)
	testHostedAwait(t, exited)
	testHostedClose(t, p)
	release()
	for _, e := range p.rec.snapshot()[p.seen:] {
		if e.ConnID == p.conn && len(e.Frame) != 0 {
			t.Fatal("late overflow output", e)
		}
	}
}
func TestHostedLifecycleListCancellation(t *testing.T) {
	d := &testAppDiscovery{entered: make(chan struct{}, 1), release: make(chan struct{})}
	p := testHostedStart(t, d, nil)
	var once sync.Once
	release := func() { once.Do(func() { close(d.release) }) }
	t.Cleanup(release)
	p.request(10, "list_apps", `{}`)
	testHostedAwait(t, d.entered)
	p.request(11, "app_cancel", `{"request_id":10}`)
	e, _ := p.next()
	if e.Type != "app_cancelled" {
		t.Fatal(e)
	}
	p.mgr.HostedAppsChanged(testLifecycleChange(1))
	testLifecycleEvent(t, p, "app_removed", 1)
	release()
	p.waitEvent("v2.hosted.completed", "read")
	p.mgr.HostedAppsChanged(testLifecycleChange(2))
	testLifecycleEvent(t, p, "app_removed", 2)
}
func TestHostedLifecycleRetirement(t *testing.T) {
	for _, action := range []string{"disconnect", "revocation", "shutdown", "frames closure"} {
		t.Run(action, func(t *testing.T) {
			p, entered, exited, release := testHostedBlockedOutput(t, &testAppDiscovery{})
			p.mgr.HostedAppsChanged(testLifecycleChange(1))
			testHostedAwait(t, entered)
			p.mgr.HostedAppsChanged(testLifecycleChange(2))
			switch action {
			case "disconnect":
				p.frames <- protocol.RoutingEnvelope{ConnID: p.conn, CloseCode: 1000}
				testHostedGone(t, p)
			case "revocation":
				p.mgr.cfg.Devices.Remove(v2TestDevName)
				testHostedClose(t, p)
			case "shutdown":
				p.stop()
			case "frames closure":
				close(p.frames)
				p.stop()
			}
			testHostedAwait(t, exited)
			p.mgr.HostedAppsChanged(testLifecycleChange(3))
			if action == "shutdown" || action == "frames closure" {
				p.mgr.hostedMu.Lock()
				stopped, pending := p.mgr.hostedStopped, len(p.mgr.hostedInboxes)
				p.mgr.hostedMu.Unlock()
				if !stopped || pending != 0 {
					t.Fatal("post-shutdown admission", stopped, pending)
				}
			}
			release()
			for _, e := range p.rec.snapshot()[p.seen:] {
				if e.ConnID == p.conn && len(e.Frame) != 0 {
					t.Fatal("late retired output", e)
				}
			}
		})
	}
}
func TestHostedLifecycleHostIsolation(t *testing.T) {
	hosts := []string{lifecycleHost, "33333333-3333-4333-8333-333333333333"}
	var registries []*apps.Registry
	var peers []*testHostedPeer
	for _, host := range hosts {
		r, _ := testLifecycleRegistry(t, host)
		registries = append(registries, r)
		peers = append(peers, testLifecyclePeer(t, r, host, []string{protocol.CapabilityHostedAppsV1}))
	}
	// Both managers consume both registries; manager authority filters foreign events.
	for i, r := range registries {
		t.Cleanup(r.Subscribe(peers[1-i].mgr.HostedAppsChanged))
	}
	for i, r := range registries {
		if _, err := r.Register(testLifecycleManifest(hosts[i], fmt.Sprintf("Host %d", i))); err != nil {
			t.Fatal(err)
		}
		testLifecycleEvent(t, peers[i], "app_updated", 1)
	}
	for i, r := range registries {
		page := testHostedPage(t, peers[i], 10, nil)
		if len(page.Items) != 1 || page.Items[0].Title != fmt.Sprintf("Host %d", i) {
			t.Fatal(page)
		}
		if _, err := r.Remove(lifecycleApp); err != nil {
			t.Fatal(err)
		}
		testLifecycleEvent(t, peers[i], "app_removed", 2)
	}
	for _, p := range peers {
		page := testHostedPage(t, p, 11, nil)
		if len(page.Items) != 0 || page.Revision != 2 {
			t.Fatal(page)
		}
	}
	r, _ := testLifecycleRegistry(t, lifecycleHost)
	p := testLifecyclePeer(t, r, lifecycleHost, nil)
	if _, err := r.Register(testLifecycleManifest(lifecycleHost, "Hidden")); err != nil {
		t.Fatal(err)
	}
	p.request(20, "list_apps", `{}`)
	e, _ := p.next()
	testHostedError(t, e, 20, protocol.CodeProtocolUnsupported)
}

func TestHostedLifecycleReconnect(t *testing.T) {
	entered, closeEntered, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	oldDone, closeDone := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	p := testHostedStart(t, &testAppDiscovery{}, func(c *V2SessionConfig) {
		out := c.Outbound
		c.HostedAppsSend = func(ctx context.Context, e protocol.RoutingEnvelope) error {
			if e.ConnID == v2TestConnID && e.CloseCode != 0 {
				defer close(closeDone)
				close(closeEntered)
				<-release
			} else if e.ConnID == v2TestConnID && first.CompareAndSwap(false, true) {
				defer close(oldDone)
				close(entered)
				<-release
			}
			// Retired I/O must suppress publication even when completion arrives late.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return out(e)
		}
	})
	t.Cleanup(unblock)
	p.request(10, "app_cancel", `{"request_id":999}`)
	testHostedAwait(t, entered)
	for revision := uint64(1); revision <= hostedQueueLimit; revision++ {
		p.mgr.HostedAppsChanged(testLifecycleChange(revision))
	}
	testHostedGone(t, p)
	testHostedAwait(t, closeEntered)
	// New handshake reuses the routing ID; old encrypted send and close remain held.
	p.open([]string{protocol.CapabilityHostedAppsV1})
	unblock()
	testHostedAwait(t, oldDone)
	testHostedAwait(t, closeDone)
	p.request(10, "list_apps", `{}`)
	e, _ := p.next()
	if e.Type != "apps" || e.InReplyTo == nil || *e.InReplyTo != 10 {
		t.Fatal("predecessor reached replacement", e)
	}
	p.mgr.HostedAppsChanged(testLifecycleChange(17))
	testLifecycleEvent(t, p, "app_removed", 17)
	page := testHostedPage(t, p, 11, nil)
	if page.Revision != 0 || len(page.Items) != 0 {
		t.Fatal(page)
	}
}
