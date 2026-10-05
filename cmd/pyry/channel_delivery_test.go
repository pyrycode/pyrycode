package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

type testPostHistory struct {
	*history.Store
	mu       sync.Mutex
	fail     conversations.ConversationID
	attempts int
	failAt   int
}

func (h *testPostHistory) Append(id conversations.ConversationID, typ string, raw json.RawMessage, ts time.Time) (uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.attempts++
	if id == h.fail && (h.failAt == 0 || h.attempts == h.failAt) {
		return 0, errors.New("posted-secret storage error")
	}
	return h.Store.Append(id, typ, raw, ts)
}
func testDelivery(t *testing.T, dir string, h channelDeliveryHistory, carry func(conversations.ConversationID, string)) *channelDelivery {
	t.Helper()
	d, err := newChannelDelivery(filepath.Join(dir, "channel-delivery.json"), h, carry, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func testPostID(t *testing.T) string {
	t.Helper()
	id, err := conversations.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return string(id)
}
func testAccept(t *testing.T, d *channelDelivery, id conversations.ConversationID, text string) string {
	t.Helper()
	turn := testPostID(t)
	if err := d.accept(id, turn, text); err != nil {
		t.Fatal(err)
	}
	return turn
}
func testDeltas(t *testing.T, h *history.Store, id conversations.ConversationID) []protocol.AssistantDeltaPayload {
	t.Helper()
	page, err := h.Page(id, "", history.MaxPageEntries)
	if err != nil {
		t.Fatal(err)
	}
	var out []protocol.AssistantDeltaPayload
	for i := len(page.Entries) - 1; i >= 0; i-- {
		e := page.Entries[i]
		if e.Type != protocol.TypeAssistantDelta {
			continue
		}
		var p protocol.AssistantDeltaPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}
func testStartDelivery(t *testing.T, d *channelDelivery) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return ctx
}
func testWaitDelivery(t *testing.T, d *channelDelivery, id conversations.ConversationID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := d.beforeInbound(func(context.Context, string, []byte) error { return nil })(ctx, string(id), nil); err != nil {
		t.Fatal(err)
	}
}

func TestChannelDelivery_RefusalPreservesAcceptedWork(t *testing.T) {
	dir := t.TempDir()
	h := history.New(dir)
	var carries int
	d := testDelivery(t, dir, h, func(conversations.ConversationID, string) { carries++ })
	id := conversations.ConversationID(testPostID(t))
	first := testAccept(t, d, id, "first")
	save := d.save
	d.save = func([]channelDeliveryPost) error { return errors.New("posted-secret") }
	if err := d.accept(id, testPostID(t), "posted-secret"); err == nil {
		t.Fatal("acceptance succeeded")
	}
	if len(testDeltas(t, h, id)) != 0 || carries != 1 {
		t.Fatal("refusal delivered content or carry")
	}
	d.save = save
	reloaded := testDelivery(t, dir, h, nil)
	reloaded.drain()
	got := testDeltas(t, h, id)
	if len(got) != 1 || got[0].TurnID != first || got[0].Text != "first" {
		t.Fatalf("accepted work lost: %+v", got)
	}
	info, err := os.Stat(d.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("pending permissions: %v %v", info, err)
	}
}

func TestChannelDelivery_RetryAndCarryIndependence(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "nil-announcer", true: "live"}[live], func(t *testing.T) {
			dir := t.TempDir()
			reg, path := newChannelTestRegistry(t, dir)
			id := addConversation(t, reg, "questions", true, false)
			carry := &channelCarry{reg: reg, path: path, logger: quietLogger()}
			h := &testPostHistory{Store: history.New(dir), fail: id, failAt: 2}
			d := testDelivery(t, dir, h, carry.record)
			text := strings.Repeat("雪<&", maxDeltaTextBytes)
			turn := testAccept(t, d, id, text)
			var announced []protocol.AssistantDeltaPayload
			if live {
				d.announce = func(p protocol.AssistantDeltaPayload) {
					got := testDeltas(t, h.Store, id)
					if len(got) != len(splitDeltaText(text, maxDeltaTextBytes)) {
						t.Error("announcement before whole history")
					}
					announced = append(announced, p)
				}
			}
			d.drain()
			if len(testDeltas(t, h.Store, id)) != 1 {
				t.Fatal("expected recorded prefix")
			}
			// Carry/clear while client delivery is still pending must not consume it.
			if err := carry.carryPending(func(context.Context, string, []byte) error { return nil })(context.Background(), string(id), []byte("reply")); err != nil {
				t.Fatal(err)
			}
			carry.clearDelivered(string(id), msgqueue.QueuedMessage{})
			if len(reg.PendingChannelPosts(id)) != 0 {
				t.Fatal("carry not cleared")
			}
			testStartDelivery(t, d)
			testWaitDelivery(t, d, id)
			d.mu.Lock()
			defer d.mu.Unlock()
			got := testDeltas(t, h.Store, id)
			var joined strings.Builder
			for i, p := range got {
				if p.Seq != i || p.TurnID != turn {
					t.Fatal("retry identity/order changed")
				}
				joined.WriteString(p.Text)
			}
			if joined.String() != text || len(reg.PendingChannelPosts(id)) != 0 {
				t.Fatal("retry lost text or re-added carry")
			}
			if live && len(announced) != len(got) {
				t.Fatal("incomplete live delivery")
			}
		})
	}
	// Completing client delivery must leave carry available for Claude.
	dir := t.TempDir()
	reg, path := newChannelTestRegistry(t, dir)
	id := addConversation(t, reg, "q", true, false)
	carry := &channelCarry{reg: reg, path: path, logger: quietLogger()}
	d := testDelivery(t, dir, history.New(dir), carry.record)
	testAccept(t, d, id, "still carry")
	d.drain()
	if got := reg.PendingChannelPosts(id); len(got) != 1 || got[0] != "still carry" {
		t.Fatal("client consumed carry")
	}
}

func TestChannelDelivery_ConcurrentFIFO(t *testing.T) {
	dir := t.TempDir()
	h := history.New(dir)
	d := testDelivery(t, dir, h, nil)
	id := conversations.ConversationID(testPostID(t))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); testAccept(t, d, id, strings.Repeat(testPostID(t), maxDeltaTextBytes/16)) }()
	}
	wg.Wait()
	accepted := append([]channelDeliveryPost(nil), d.posts...)
	var live []protocol.AssistantDeltaPayload
	d.announce = func(p protocol.AssistantDeltaPayload) { live = append(live, p) }
	d.drain()
	got := testDeltas(t, h, id)
	if len(live) != len(got) {
		t.Fatal("missing live chunks")
	}
	pos := 0
	for _, post := range accepted {
		for seq, text := range splitDeltaText(post.Text, maxDeltaTextBytes) {
			if pos >= len(got) {
				t.Fatal("missing history")
			}
			p := got[pos]
			if p.TurnID != post.TurnID || p.Seq != seq || p.Text != text || live[pos] != p {
				t.Fatal("interleaved or reordered posts")
			}
			pos++
		}
	}
	if pos != len(got) {
		t.Fatal("duplicate chunks")
	}
}

func TestChannelDelivery_FailedHeadAndStartupPrecedence(t *testing.T) {
	dir := t.TempDir()
	a, b := conversations.ConversationID(testPostID(t)), conversations.ConversationID(testPostID(t))
	h := &testPostHistory{Store: history.New(dir), fail: a}
	d := testDelivery(t, dir, h, nil)
	first := testAccept(t, d, a, "head")
	second := testAccept(t, d, a, "tail")
	testAccept(t, d, b, "other")
	// Reload is established before inbound delivery, then the worker starts.
	d = testDelivery(t, dir, h, nil)
	ctx := testStartDelivery(t, d)
	delivered := make(chan string, 2)
	inbound := d.beforeInbound(func(_ context.Context, id string, _ []byte) error { delivered <- id; return nil })
	done := make(chan error, 1)
	go func() { done <- inbound(ctx, string(a), nil) }()
	if err := inbound(ctx, string(b), nil); err != nil {
		t.Fatal(err)
	}
	if got := <-delivered; got != string(b) {
		t.Fatal("failed conversation blocked another")
	}
	if len(testDeltas(t, h.Store, a)) != 0 {
		t.Fatal("tail passed failed head")
	}
	h.mu.Lock()
	h.fail = ""
	h.mu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("recovery stalled")
	}
	if got := <-delivered; got != string(a) {
		t.Fatal("wrong delivery")
	}
	got := testDeltas(t, h.Store, a)
	if len(got) != 2 || got[0].TurnID != first || got[1].TurnID != second {
		t.Fatal("user turn preceded recovery")
	}
}

func TestChannelDelivery_ReloadInterruptions(t *testing.T) {
	for _, prefix := range []int{0, 1, 3} {
		t.Run(map[int]string{0: "undelivered", 1: "between-appends", 3: "before-cleanup"}[prefix], func(t *testing.T) {
			dir := t.TempDir()
			h := history.New(dir)
			id := conversations.ConversationID(testPostID(t))
			var carries int
			d := testDelivery(t, dir, h, func(conversations.ConversationID, string) { carries++ })
			text := strings.Repeat("x", 2*maxDeltaTextBytes+1)
			turn := testAccept(t, d, id, text)
			chunks := splitDeltaText(text, maxDeltaTextBytes)
			for i := 0; i < prefix; i++ {
				raw, _ := json.Marshal(protocol.AssistantDeltaPayload{ConversationID: string(id), TurnID: turn, Seq: i, Text: chunks[i]})
				if _, err := h.Append(id, protocol.TypeAssistantDelta, raw, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			// Place the prefix beyond the newest reconciliation page.
			for i := 0; i < 130; i++ {
				if _, err := h.Append(id, "unrelated", json.RawMessage(`{}`), time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			reloaded := testDelivery(t, dir, history.New(dir), func(conversations.ConversationID, string) { carries++ })
			var live int
			reloaded.announce = func(protocol.AssistantDeltaPayload) { live++ }
			reloaded.drain()
			got := testDeltas(t, history.New(dir), id)
			if len(got) != len(chunks) || carries != 1 {
				t.Fatal("duplicate history or carry")
			}
			for i, p := range got {
				if p.TurnID != turn || p.Seq != i || p.Text != chunks[i] {
					t.Fatal("reload changed identity/text")
				}
			}
			if prefix == len(chunks) && live != 0 {
				t.Fatal("fully recorded post re-announced")
			}
			again := testDelivery(t, dir, history.New(dir), nil)
			again.drain()
			if len(testDeltas(t, h, id)) != len(chunks) {
				t.Fatal("cleanup lost on reload")
			}
		})
	}
}

func TestChannelDelivery_CleanupFailureDoesNotRepeat(t *testing.T) {
	dir := t.TempDir()
	h := history.New(dir)
	d := testDelivery(t, dir, h, nil)
	id := conversations.ConversationID(testPostID(t))
	testAccept(t, d, id, "hello")
	var live int
	d.announce = func(protocol.AssistantDeltaPayload) { live++ }
	save := d.save
	d.save = func([]channelDeliveryPost) error { return errors.New("cleanup") }
	d.drain()
	d.drain()
	if live != 1 || len(testDeltas(t, h, id)) != 1 {
		t.Fatal("cleanup failure duplicated delivery")
	}
	testWaitDelivery(t, d, id)
	d.save = save
	d.drain()
}

func TestChannelDelivery_PersistenceAndLogs(t *testing.T) {
	dir := t.TempDir()
	h := history.New(dir)
	id := conversations.ConversationID(testPostID(t))
	var carry int
	d := testDelivery(t, dir, h, func(conversations.ConversationID, string) { carry++ })
	var logs bytes.Buffer
	d.log = slog.New(slog.NewTextHandler(&logs, nil))
	// Force the real atomic rename to fail, after its temporary file was written.
	if err := os.Mkdir(d.path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := d.accept(id, testPostID(t), "posted-secret"); err == nil || err.Error() != msgChannelPostRecordFailed {
		t.Fatalf("refusal: %v", err)
	}
	if len(d.posts) != 0 || carry != 0 || len(testDeltas(t, h, id)) != 0 {
		t.Fatal("failed persistence delivered part of a post")
	}
	if err := os.Remove(d.path); err != nil {
		t.Fatal(err)
	}
	bad := &testPostHistory{Store: h, fail: id}
	d.hist = bad
	testAccept(t, d, id, "posted-secret")
	d.drain()
	for _, forbidden := range []string{"posted-secret", "seq=", "chunks=", dir} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("log exposed %q", forbidden)
		}
	}
}

func TestChannelDelivery_LoadRefusesUnsafeState(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "malformed", true: "symlink"}[symlink], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "channel-delivery.json")
			if symlink {
				if err := os.Symlink(filepath.Join(dir, "target"), path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("posted-secret"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := newChannelDelivery(path, history.New(dir), nil, quietLogger()); err == nil || strings.Contains(err.Error(), "posted-secret") {
				t.Fatalf("unsafe load: %v", err)
			}
		})
	}
}

func TestChannelDelivery_RejectedDaemonLeavesPendingUntouched(t *testing.T) {
	previous := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(previous)
	for _, state := range []string{"accepted", "malformed"} {
		t.Run(state, func(t *testing.T) {
			for attempt := 0; attempt < 10; attempt++ {
				home := shortTempDir(t)
				t.Setenv("HOME", home)
				t.Setenv("PYRY_RELAY_URL", "")
				if err := os.MkdirAll(filepath.Dir(resolveConfigPath()), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(resolveConfigPath(), []byte(`{"relay_url":""}`), 0600); err != nil {
					t.Fatal(err)
				}
				socket := filepath.Join(home, "live.sock")
				ln, err := net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = ln.Close() })
				instance := resolveInstanceDirPath("pending-startup")
				id := conversations.ConversationID(testPostID(t))
				d := testDelivery(t, instance, history.New(instance), nil)
				testAccept(t, d, id, "existing accepted post")
				if state == "malformed" {
					// Ownership refusal must precede even reading pending state.
					if err := os.WriteFile(d.path, []byte("invalid pending state"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				before, err := os.ReadFile(d.path)
				if err != nil {
					t.Fatal(err)
				}
				err = runSupervisor([]string{"-pyry-name", "pending-startup", "-pyry-socket", socket, "-pyry-workdir", home, "-pyry-codex", "/bin/true", "-pyry-claude", "/bin/true"})
				if !errors.Is(err, control.ErrInstanceRunning) {
					t.Fatalf("startup result: %v, want instance ownership refusal", err)
				}
				after, err := os.ReadFile(d.path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) || len(testDeltas(t, history.New(instance), id)) != 0 {
					t.Fatal("rejected daemon modified pending state or history")
				}
			}
		})
	}
}
