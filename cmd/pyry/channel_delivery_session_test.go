package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

func testPostSessionEntries(t *testing.T, h *history.Store, id conversations.ConversationID, want *history.SessionProvenance, count int) []history.Entry {
	t.Helper()
	page, err := h.Page(id, "", history.MaxPageEntries)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != count {
		t.Fatalf("entries = %d, want %d", len(page.Entries), count)
	}
	for _, entry := range page.Entries {
		if !reflect.DeepEqual(entry.Session, want) {
			t.Errorf("entry %d session = %#v, want %#v", entry.ID, entry.Session, want)
		}
		shown := entry.Type == protocol.TypeAssistantDelta
		if entry.Shown == nil || *entry.Shown != shown {
			t.Errorf("entry %d visibility = %v, want %v", entry.ID, entry.Shown, shown)
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"session", "session_id", "shown"} {
			if _, ok := payload[key]; ok {
				t.Errorf("metadata %q leaked into legacy payload", key)
			}
		}
	}
	latest, err := h.LatestDisplayableEntryID(id)
	if err != nil || latest != uint64(count-1) {
		t.Fatalf("displayable watermark = %d, %v, want %d", latest, err, count-1)
	}
	return page.Entries
}

func TestChannelDelivery_SessionAcceptance(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, binding, agent                     string
		missing, noProvider, lookupError, create bool
		want                                     *history.SessionProvenance
	}{
		{name: "claude", binding: "route-claude", agent: "claude", want: &history.SessionProvenance{Kind: "claude", SessionID: "route-claude"}},
		{name: "codex", binding: "route-codex", agent: "codex", want: &history.SessionProvenance{Kind: "codex", SessionID: "route-codex"}},
		{name: "unbound", want: &history.SessionProvenance{Kind: "none"}},
		{name: "unbound-without-agent-provider", noProvider: true, want: &history.SessionProvenance{Kind: "none"}},
		{name: "missing-conversation", missing: true},
		{name: "missing-agent", binding: "unknown", agent: "claude", lookupError: true},
		{name: "unavailable-provider", binding: "unknown", noProvider: true},
		{name: "unsupported-agent", binding: "route-other", agent: "other"},
		{name: "empty-agent", binding: "route-empty"},
		{name: "auto-created", binding: "route-created", agent: "codex", create: true, want: &history.SessionProvenance{Kind: "codex", SessionID: "route-created"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			reg, _ := newChannelTestRegistry(t, dir)
			id := conversations.ConversationID(testPostID(t))
			name := "session-channel"
			add := func() {
				reg.Create(conversations.Conversation{ID: id, Name: &name, IsPromoted: true, CurrentSessionID: tc.binding})
			}
			if !tc.missing && !tc.create {
				add()
			}
			calls := 0
			var harnessFor func(sessions.SessionID) (string, error)
			if !tc.noProvider {
				harnessFor = func(got sessions.SessionID) (string, error) {
					calls++
					if string(got) != tc.binding {
						t.Fatalf("lookup ID = %q, want %q", got, tc.binding)
					}
					if tc.lookupError {
						return tc.agent, errors.New("unavailable agent")
					}
					return tc.agent, nil
				}
			}
			h := history.New(dir)
			d := testDelivery(t, dir, h, nil)
			d.sessionFor = channelPostSession(reg, harnessFor)
			if tc.create {
				post := channelPoster(reg, func(string, string) (string, error) { add(); return string(id), nil }, dir, d.accept, quietLogger())
				if err := post(name, "post"); err != nil {
					t.Fatal(err)
				}
			} else {
				testAccept(t, d, id, "post")
			}
			if !reflect.DeepEqual(d.posts[0].Session, tc.want) {
				t.Fatalf("pending snapshot = %#v, want %#v", d.posts[0].Session, tc.want)
			}
			if persisted := testDelivery(t, dir, h, nil).posts[0].Session; !reflect.DeepEqual(persisted, tc.want) {
				t.Fatalf("persisted snapshot = %#v, want %#v", persisted, tc.want)
			}
			before := calls
			reg.Update(id, func(c *conversations.Conversation) { c.CurrentSessionID = "later-binding" })
			d.active[string(id)] = true
			d.drain()
			if len(testDeltas(t, h, id)) != 0 {
				t.Fatal("held post delivered")
			}
			delete(d.active, string(id))
			d.drain()
			if calls != before {
				t.Fatal("delivery looked up current binding")
			}
			warm := testPostSessionEntries(t, h, id, tc.want, 2)
			cold := testPostSessionEntries(t, history.New(dir), id, tc.want, 2)
			if !reflect.DeepEqual(warm, cold) {
				t.Fatal("reopened history changed")
			}
		})
	}
}

func TestChannelDelivery_SessionDormant(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			dir := t.TempDir()
			boot, dormant := testPostID(t), testPostID(t)
			path := filepath.Join(dir, "sessions.json")
			raw, err := json.Marshal(map[string]any{"version": 1, "sessions": []map[string]any{
				{"id": boot, "bootstrap": true}, {"id": dormant, "harness": agent, "thread_id": "codex-thread-is-not-routing-id"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			constructed := 0
			pool, err := sessions.New(sessions.Config{Bootstrap: sessions.SessionConfig{ClaudeBin: os.Args[0]}, RegistryPath: path,
				RunnerFactory: func(sessions.RunnerConfig) (sessions.Runner, error) { constructed++; return stubRunner{}, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			before := constructed
			reg, _ := newChannelTestRegistry(t, dir)
			id := addConversation(t, reg, "dormant", true, false)
			reg.Update(id, func(c *conversations.Conversation) { c.CurrentSessionID = dormant })
			h := history.New(dir)
			d := testDelivery(t, dir, h, nil)
			d.sessionFor = channelPostSession(reg, pool.HarnessFor)
			testAccept(t, d, id, "post")
			d.drain()
			testPostSessionEntries(t, h, id, &history.SessionProvenance{Kind: agent, SessionID: dormant}, 2)
			if constructed != before {
				t.Fatal("dormant lookup constructed a runner")
			}
			if _, err := pool.Lookup(sessions.SessionID(dormant)); !errors.Is(err, sessions.ErrSessionNotFound) {
				t.Fatalf("dormant session revived: %v", err)
			}
		})
	}
}

func TestChannelDelivery_SessionRetryRecovery(t *testing.T) {
	t.Parallel()
	for _, restart := range []bool{false, true} {
		for _, failAt := range []int{2, 4} {
			t.Run(map[bool]string{false: "retry", true: "restart"}[restart]+map[int]string{2: "-delta", 4: "-completion"}[failAt], func(t *testing.T) {
				dir := t.TempDir()
				reg, _ := newChannelTestRegistry(t, dir)
				id := addConversation(t, reg, "retry", true, false)
				reg.Update(id, func(c *conversations.Conversation) { c.CurrentSessionID = "accepted-route" })
				h := &testPostHistory{Store: history.New(dir), fail: id, failAt: failAt}
				d := testDelivery(t, dir, h, nil)
				d.sessionFor = channelPostSession(reg, func(sessions.SessionID) (string, error) { return "claude", nil })
				turn := testAccept(t, d, id, strings.Repeat("x", 2*maxDeltaTextBytes+1))
				var live, complete int
				announce := func(protocol.AssistantDeltaPayload) { live++ }
				completion := func(channelPostTurnEndPayload) { complete++ }
				d.announce, d.complete = announce, completion
				d.drain()
				if live != 0 || complete != 0 {
					t.Fatal("published partial history")
				}
				prefix, err := h.Page(id, "", 10)
				if err != nil || len(prefix.Entries) != failAt-1 {
					t.Fatalf("prefix = %#v, %v", prefix, err)
				}
				reg.Update(id, func(c *conversations.Conversation) { c.CurrentSessionID = "later-codex-route" })
				if restart {
					d = testDelivery(t, dir, history.New(dir), nil)
				}
				d.sessionFor = func(conversations.ConversationID) *history.SessionProvenance {
					t.Fatal("recovery inferred provenance")
					return nil
				}
				d.announce, d.complete = announce, completion
				d.drain()
				want := &history.SessionProvenance{Kind: "claude", SessionID: "accepted-route"}
				entries := testPostSessionEntries(t, history.New(dir), id, want, 4)
				for _, old := range prefix.Entries {
					if !reflect.DeepEqual(entries[4-int(old.ID)], old) {
						t.Fatal("retry rewrote stored prefix")
					}
				}
				for i, p := range testDeltas(t, history.New(dir), id) {
					if p.Seq != i || p.TurnID != turn {
						t.Fatal("retry changed chunk identity")
					}
				}
				if live != 3 || complete != 1 {
					t.Fatalf("publication counts = %d, %d", live, complete)
				}
				again := testDelivery(t, dir, history.New(dir), nil)
				again.announce, again.complete = announce, completion
				again.drain()
				if live != 3 || complete != 1 {
					t.Fatal("cleanup recovery republished")
				}
			})
		}
	}
}

func TestChannelDelivery_SessionLegacyRecovery(t *testing.T) {
	t.Parallel()
	for _, snapshot := range []string{"", `,"session":{"kind":"none"}`, `,"session":{"kind":"codex","session_id":"original-route"}`} {
		t.Run(snapshot, func(t *testing.T) {
			dir := t.TempDir()
			id := conversations.ConversationID(testPostID(t))
			turn := testPostID(t)
			text := strings.Repeat("y", maxDeltaTextBytes+1)
			raw, err := json.Marshal(channelDeliveryPost{ConversationID: id, TurnID: turn, Text: text, TS: time.Now().UTC()})
			if err != nil {
				t.Fatal(err)
			}
			// Literal pre-provenance records have no session key at all.
			pending := "[" + strings.TrimSuffix(string(raw), "}") + snapshot + "}]"
			if err := os.WriteFile(filepath.Join(dir, "channel-delivery.json"), []byte(pending), 0600); err != nil {
				t.Fatal(err)
			}
			h := history.New(dir)
			old, err := json.Marshal(protocol.AssistantDeltaPayload{ConversationID: string(id), TurnID: turn, Seq: 0, Text: text[:maxDeltaTextBytes]})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.Append(id, protocol.TypeAssistantDelta, old, time.Now()); err != nil {
				t.Fatal(err)
			}
			d := testDelivery(t, dir, history.New(dir), nil)
			d.sessionFor = func(conversations.ConversationID) *history.SessionProvenance {
				t.Fatal("loaded post looked up new binding")
				return nil
			}
			want := d.posts[0].Session
			if snapshot == "" && want != nil {
				t.Fatal("legacy snapshot became known")
			}
			if snapshot != "" && want == nil {
				t.Fatal("explicit snapshot lost")
			}
			d.drain()
			for _, store := range []*history.Store{h, history.New(dir)} {
				page, err := store.Page(id, "", 10)
				if err != nil || len(page.Entries) != 3 {
					t.Fatalf("recovery page = %#v, %v", page, err)
				}
				for _, e := range page.Entries {
					if e.ID == 1 {
						if e.Session != nil || e.Shown != nil || string(e.Payload) != string(old) {
							t.Fatal("legacy entry rewritten")
						}
					} else if !reflect.DeepEqual(e.Session, want) {
						t.Fatalf("recovered session = %#v, want %#v", e.Session, want)
					}
				}
			}
		})
	}
}

func TestChannelDelivery_SessionInvalidPending(t *testing.T) {
	t.Parallel()
	for _, provenance := range []history.SessionProvenance{{Kind: "none", SessionID: "unexpected"}, {Kind: "claude"}, {Kind: "codex"}, {Kind: "other", SessionID: "route"}} {
		dir := t.TempDir()
		post := channelDeliveryPost{ConversationID: conversations.ConversationID(testPostID(t)), TurnID: testPostID(t), Text: "post", TS: time.Now(), Session: &provenance}
		raw, err := json.Marshal([]channelDeliveryPost{post})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "channel-delivery.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := newChannelDelivery(filepath.Join(dir, "channel-delivery.json"), history.New(dir), nil, quietLogger()); err == nil || err.Error() != "channel delivery state invalid" {
			t.Fatalf("invalid session load = %v", err)
		}
	}
}

func TestChannelDelivery_SessionDaemonWiring(t *testing.T) {
	home := shortTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("PYRY_RELAY_URL", "")
	if err := os.MkdirAll(filepath.Dir(resolveConfigPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolveConfigPath(), []byte(`{"relay_url":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	const name = "provenance-wiring"
	reg, _ := newChannelTestRegistry(t, home)
	id := addConversation(t, reg, "posts", true, false)
	boot, dormant := testPostID(t), testPostID(t)
	reg.Update(id, func(c *conversations.Conversation) { c.CurrentSessionID = dormant })
	instance := resolveInstanceDirPath(name)
	if err := os.MkdirAll(instance, 0700); err != nil {
		t.Fatal(err)
	}
	if err := reg.Save(resolveConversationsRegistryPath(name)); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"version": 1, "sessions": []map[string]any{
		{"id": boot, "bootstrap": true}, {"id": dormant, "harness": "codex"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolveRegistryPath(name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	bin := stubBinary(t)
	socket := filepath.Join(home, "daemon.sock")
	done := make(chan error, 1)
	go func() {
		done <- runSupervisor([]string{"-pyry-name", name, "-pyry-socket", socket, "-pyry-workdir", home, "-pyry-codex", bin, "-pyry-claude", bin})
	}()
	joined := false
	t.Cleanup(func() {
		if joined {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = control.Stop(ctx, socket)
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("daemon did not stop")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		if _, err := control.SessionsList(ctx, socket); err == nil {
			break
		}
		select {
		case err := <-done:
			joined = true
			t.Fatalf("startup: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := control.ChannelPost(ctx, socket, "posts", "accepted by daemon"); err != nil {
		t.Fatal(err)
	}
	testShadowWait(t, func() bool {
		page, err := history.New(instance).Page(id, "", 10)
		return err == nil && len(page.Entries) == 2
	})
	if err := control.Stop(ctx, socket); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		joined = true
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("daemon shutdown did not join")
	}
	rawCache, err := os.ReadFile(filepath.Join(instance, "conversations", string(id), "history", "thread-cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cache struct {
		Version  uint64
		Complete bool
	}
	if json.Unmarshal(rawCache, &cache) != nil || cache.Version != 2 || !cache.Complete {
		t.Fatal("daemon did not persist final shadow version")
	}
	// Reload retains the same source captured at acceptance.
	h := history.New(instance)
	d := testDelivery(t, instance, h, nil)
	d.drain()
	testPostSessionEntries(t, h, id, &history.SessionProvenance{Kind: "codex", SessionID: dormant}, 2)
}
