package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func TestPublishedModelSelection_SettingsRoundTrip(t *testing.T) {
	t.Parallel()
	for _, dormant := range []bool{false, true} {
		for _, alias := range []bool{false, true} {
			name := "live/pinned-only"
			if dormant {
				name = "dormant/pinned-only"
			}
			if alias {
				name += "/alias-present"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				pool, plan := newDormantWritePool(t, `"model":"sonnet","effort":"low",`)
				x, y := dormantWriteBootID, dormantWriteTargetID
				if dormant {
					x, y = y, x
				}
				otherModel, otherEffort := "sonnet", "low"
				if err := pool.UpdateSettings(dormantWriteBootID, sessions.SettingsUpdate{Model: &otherModel, Effort: &otherEffort}); err != nil {
					t.Fatal(err)
				}
				published := "claude-fable-5-1[1m]"
				rows := []turnevent.ModelOption{
					{Value: "sonnet", EffortLevels: []string{"low"}},
					{Value: "claude-fable-5[1m]", EffortLevels: []string{"low", "max"}},
					{Value: published, EffortLevels: []string{"low", "high"}},
				}
				if alias {
					published = "fable[1m]"
					rows = append(rows, turnevent.ModelOption{Value: published, EffortLevels: []string{"low", "high"}})
				}
				path := filepath.Join(t.TempDir(), "model_list.json")
				body, err := encodeModelVocabulary(turnevent.ModelList{Models: rows}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body, 0o600); err != nil {
					t.Fatal(err)
				}
				store := newModelVocabularyStore(path)
				store.Load()
				list, ok := store.ModelList()
				if !ok {
					t.Fatal("saved vocabulary unavailable")
				}
				plan.arm(dormantWriteBootID, list)
				reg := &conversations.Registry{}
				reg.Create(conversations.Conversation{ID: "X", CurrentSessionID: x})
				reg.Create(conversations.Conversation{ID: "Y", CurrentSessionID: y})
				adapter := settingsUpdaterAdapter{p: pool, saved: store}
				resolve := runSettingsFor(reg, pool, store)
				key, err := ecdh.X25519().GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				paired := &devices.Registry{}
				for _, id := range []string{"phone", "reopened"} {
					paired.Add(devices.Device{TokenHash: devices.HashToken(id + "-token"), Name: "test phone", PairedAt: time.Now().UTC()})
				}
				w := &switchWire{t: t, frames: make(chan protocol.RoutingEnvelope, 16), out: make(chan protocol.RoutingEnvelope, 128), phones: map[string]switchPhone{}, pub: key.PublicKey().Bytes()}
				mgr, err := relay.NewV2SessionManager(relay.V2SessionConfig{
					Frames: w.frames, Outbound: func(e protocol.RoutingEnvelope) error { w.out <- e; return nil },
					StaticPriv: key.Bytes(), Devices: paired, ServerID: string(identity.NewServerID()),
					Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
					SettingsUpdater: adapter, CapabilitiesFor: adapter.Capabilities,
					RunConfigFor: runConfigFor(resolve, nil), ModelListFor: modelListFor(reg, pool, store),
					KnownConversation: func(id string) bool { _, ok := reg.Get(conversations.ConversationID(id)); return ok },
				})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() { defer close(done); _ = mgr.Run(ctx) }()
				t.Cleanup(func() { cancel(); <-done })
				w.open("phone", true)
				requestID := uint64(10)
				exchange := func(phone, typ string, payload any, want string) protocol.Envelope {
					t.Helper()
					requestID++
					p, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					raw, err := json.Marshal(protocol.Envelope{ID: requestID, Type: typ, TS: time.Now().UTC(), Payload: p})
					if err != nil {
						t.Fatal(err)
					}
					sealed, err := w.phones[phone].send.Encrypt(raw)
					if err != nil {
						t.Fatal(err)
					}
					w.frames <- w.wrap(phone, protocol.TypeNoiseMsg, sealed)
					id, reply := w.read()
					if id != phone || reply.Type != want || reply.InReplyTo == nil || *reply.InReplyTo != requestID {
						t.Fatalf("%s reply = %+v on %s, want %s", typ, reply, id, want)
					}
					return reply
				}
				menuReply := exchange("phone", protocol.TypeRequestModelList, protocol.RequestModelListPayload{ConversationID: "X"}, protocol.TypeModelList)
				var menu protocol.ModelListPayload
				if err := json.Unmarshal(menuReply.Payload, &menu); err != nil {
					t.Fatal(err)
				}
				if menu.ConversationID != "X" || menu.DroppedModels != 0 || len(menu.Models) != 2 || menu.Models[1].Value != published {
					t.Fatalf("published menu = %+v, want one Fable row %q", menu, published)
				}
				read := func(phone, conv, model, effort, session string) {
					t.Helper()
					reply := exchange(phone, protocol.TypeRequestSessionSettings, protocol.RequestSessionSettingsPayload{ConversationID: conv}, protocol.TypeSessionSettings)
					var got protocol.SessionSettingsPayload
					if err := json.Unmarshal(reply.Payload, &got); err != nil {
						t.Fatal(err)
					}
					if got.Model != model || got.Effort != effort || got.SessionID != session {
						t.Fatalf("%s read = %+v, want %s/%s on %s", conv, got, model, effort, session)
					}
					if conv == "X" && model != "" && (got.Capabilities == nil || !reflect.DeepEqual(got.Capabilities.EffortLevels, []string{"low", "high"})) {
						t.Fatalf("effort capabilities = %+v", got.Capabilities)
					}
				}
				effort := "high"
				for i := 0; i < 2; i++ {
					picked := menu.Models[1].Value
					if alias && i == 1 {
						picked = "claude-fable-5-1[1m]"
					}
					ack := exchange("phone", protocol.TypeSetSessionSettings, protocol.SetSessionSettingsPayload{SessionID: x, Model: &picked, Effort: &effort}, protocol.TypeSessionSettingsUpdated)
					var fields map[string]any
					if err := json.Unmarshal(ack.Payload, &fields); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(fields, map[string]any{"session_id": x}) {
						t.Fatalf("ack = %s", ack.Payload)
					}
					for j := 0; j < 2; j++ {
						read("phone", "X", published, effort, x)
					}
					read("phone", "Y", otherModel, otherEffort, y)
				}
				var stored sessions.SessionSettings
				if dormant {
					stored, err = pool.DormantSettingsFor(sessions.SessionID(x))
				} else {
					stored, err = pool.SettingsFor(sessions.SessionID(x))
				}
				if err != nil || stored.Model != "fable[1m]" || stored.Effort != effort {
					t.Fatalf("stored = %+v, %v", stored, err)
				}
				if len(pool.List()) != 1 {
					t.Fatal("settings materialized a dormant session")
				}
				w.open("reopened", true)
				read("reopened", "X", published, effort, x)
				read("reopened", "Y", otherModel, otherEffort, y)
				// A refused combined update must preserve model, effort and posture.
				badEffort, mode := "max", "plan"
				exchange("phone", protocol.TypeSetSessionSettings, protocol.SetSessionSettingsPayload{SessionID: x, Model: &published, Effort: &badEffort, PermissionMode: &mode}, protocol.TypeError)
				read("phone", "X", published, effort, x)
				// Effort-only validation must find the pinned row from stored family settings.
				exchange("phone", protocol.TypeSetSessionSettings, protocol.SetSessionSettingsPayload{SessionID: x, Effort: &badEffort}, protocol.TypeError)
				read("phone", "X", published, effort, x)
				var afterRefusal sessions.SessionSettings
				if dormant {
					afterRefusal, err = pool.DormantSettingsFor(sessions.SessionID(x))
				} else {
					afterRefusal, err = pool.SettingsFor(sessions.SessionID(x))
				}
				if err != nil || afterRefusal != stored {
					t.Fatalf("refused frame changed stored settings: %+v, %v; want %+v", afterRefusal, err, stored)
				}
				reset := ""
				exchange("phone", protocol.TypeSetSessionSettings, protocol.SetSessionSettingsPayload{SessionID: x, Model: &reset}, protocol.TypeSessionSettingsUpdated)
				read("phone", "X", "", effort, x)
			})
		}
	}
}

func TestOfferedModel(t *testing.T) {
	t.Parallel()
	pinned := "claude-fable-5-1[1m]"
	for _, tc := range []struct {
		name, harness, model, want string
		row                        turnevent.ModelOption
	}{
		{name: "published pin", model: pinned, row: turnevent.ModelOption{Value: pinned}, want: pinned},
		{name: "stored family", model: "fable[1m]", row: turnevent.ModelOption{Value: pinned}, want: pinned},
		{name: "pinned input with alias", model: pinned, row: turnevent.ModelOption{Value: "fable[1m]"}, want: "fable[1m]"},
		{name: "different variant", model: "fable", row: turnevent.ModelOption{Value: pinned}, want: "fable"},
		{name: "different family", model: "opus[1m]", row: turnevent.ModelOption{Value: pinned}, want: "opus[1m]"},
		{name: "cut matching value", model: "fable[1m]", row: turnevent.ModelOption{Value: pinned, TruncatedFields: []string{"value"}}, want: "fable[1m]"},
		{name: "other field cut", model: "fable[1m]", row: turnevent.ModelOption{Value: pinned, TruncatedFields: []string{"display_name"}}, want: pinned},
		{name: "empty reset", row: turnevent.ModelOption{Value: pinned}},
		{name: "Codex exact", harness: harnessCodex, model: "gpt-5.6-sol", row: turnevent.ModelOption{Value: "gpt-5.6-sol"}, want: "gpt-5.6-sol"},
		{name: "Codex does not alias Claude identifiers", harness: harnessCodex, model: pinned, row: turnevent.ModelOption{Value: "fable[1m]"}, want: pinned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			list := turnevent.ModelList{Models: []turnevent.ModelOption{tc.row}}
			if got := offeredModel(tc.harness, list, tc.model); got != tc.want {
				t.Fatalf("offeredModel = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPublishedModelSelection_VocabularyRefresh(t *testing.T) {
	t.Parallel()
	pool, plan := newModelListTestPool(t)
	id := string(pool.Default().ID())
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{ID: "X", CurrentSessionID: id})
	reg.Create(conversations.Conversation{ID: "unbound"})
	saved := &agentVocabularyDouble{have: true, list: turnevent.ModelList{Models: []turnevent.ModelOption{{Value: "claude-fable-5-1[1m]", EffortLevels: []string{"low"}}}}}
	adapter := settingsUpdaterAdapter{p: pool, saved: saved}
	resolve := runSettingsFor(reg, pool, saved)
	model, effort := "claude-fable-5-1[1m]", "low"
	if err := adapter.UpdateSettings(id, relay.SettingsUpdate{Model: &model, Effort: &effort}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []string{"claude-fable-5-1[1m]", "claude-fable-5-2[1m]", "fable[1m]"} {
		saved.list.Models[0].Value = row
		got, ok := resolve("X")
		if !ok || got.model != row || got.effort != "low" || !got.live {
			t.Fatalf("refreshed readback = %+v, %v; want %q", got, ok, row)
		}
		stored, err := pool.SettingsFor(sessions.SessionID(id))
		if err != nil || stored.Model != "fable[1m]" {
			t.Fatalf("canonical storage changed: %+v, %v", stored, err)
		}
	}
	// Bound vocabulary must supersede the saved source for all three seams.
	bound := turnevent.ModelList{Models: []turnevent.ModelOption{{Value: "claude-fable-5-3[1m]", EffortLevels: []string{"high"}}}}
	plan.arm(sessions.SessionID(id), bound)
	got, ok := resolve("X")
	menu, menuOK := modelListFor(reg, pool, saved)("X", true)
	cap, capOK := adapter.Capabilities(id, got.model)
	if !ok || !menuOK || !capOK || got.model != menu.Models[0].Value || !reflect.DeepEqual(cap.EffortLevels, []string{"high"}) {
		t.Fatalf("bound source disagreement: %+v / %+v / %+v", got, menu, cap)
	}
	before := saved.reads.Load()
	for _, conv := range []string{"unknown", "unbound"} {
		if _, ok := resolve(conv); ok {
			t.Fatalf("resolved %q", conv)
		}
	}
	if err := adapter.UpdateSettings("unknown", relay.SettingsUpdate{Model: &model}); !errors.Is(err, relay.ErrSessionUnknown) {
		t.Fatalf("unknown session = %v", err)
	}
	if saved.reads.Load() != before {
		t.Fatal("unresolved identity consulted vocabulary")
	}
}

func TestPublishedModelSelection_Refusals(t *testing.T) {
	t.Parallel()
	pinned := "claude-fable-5-1[1m]"
	for _, tc := range []struct {
		name, model string
		have        bool
		list        turnevent.ModelList
		want        error
	}{
		{name: "unoffered family", model: "opus[1m]", have: true, list: modelListFixture(pinned), want: relay.ErrModelNotOffered},
		{name: "unoffered variant", model: "fable", have: true, list: modelListFixture(pinned), want: relay.ErrModelNotOffered},
		{name: "no vocabulary", model: pinned, want: relay.ErrModelVocabularyUnavailable},
		{name: "incomplete absence", model: "opus", have: true, list: turnevent.ModelList{Models: []turnevent.ModelOption{{Value: pinned}}, DroppedModels: 1}, want: relay.ErrModelVocabularyUnavailable},
		{name: "matching cut row", model: pinned, have: true, list: turnevent.ModelList{Models: []turnevent.ModelOption{{Value: pinned, TruncatedFields: []string{"value"}}}}, want: relay.ErrModelVocabularyUnavailable},
		{name: "matching row despite dropped siblings", model: pinned, have: true, list: turnevent.ModelList{Models: []turnevent.ModelOption{{Value: pinned, EffortLevels: []string{"high"}}}, DroppedModels: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pool, _ := newModelListTestPool(t)
			id := pool.Default().ID()
			before, err := pool.SettingsFor(id)
			if err != nil {
				t.Fatal(err)
			}
			adapter := settingsUpdaterAdapter{p: pool, saved: &agentVocabularyDouble{have: tc.have, list: tc.list}}
			effort, mode := "high", "plan"
			err = adapter.UpdateSettings(string(id), relay.SettingsUpdate{Model: &tc.model, Effort: &effort, PermissionMode: &mode})
			if !errors.Is(err, tc.want) {
				t.Fatalf("UpdateSettings = %v, want %v", err, tc.want)
			}
			after, err := pool.SettingsFor(id)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want != nil && after != before {
				t.Fatalf("refused frame changed settings: %+v to %+v", before, after)
			}
			if tc.want == nil && (after.Model != "fable[1m]" || after.Effort != effort || after.PermissionMode != mode) {
				t.Fatalf("accepted settings = %+v", after)
			}
		})
	}
}

func TestPublishedModelSelection_ProductionWiring(t *testing.T) {
	if !strings.Contains(formattedGoFunc(t, "main.go", "runSupervisor"), "runSettingsFor(convReg, pool, modelVocabulary, liveBindings)") {
		t.Fatal("published selection resolver is not wired in production")
	}
}
