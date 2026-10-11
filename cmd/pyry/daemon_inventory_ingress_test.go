package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/streamsup"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

func testInventoryIngress(t *testing.T, lateOwner ...bool) (*daemonLiveBindings, *sessions.Pool, streamRunner) {
	t.Helper()
	reg := &conversations.Registry{}
	sink := newStreamTurnSink(128, discardLogger())
	owner := newDaemonLiveState(func(sid string) (string, bool) { return conversationForSession(reg, sid) })
	if len(lateOwner) == 0 || !lateOwner[0] {
		sink.live = owner
	}
	vocab := newModelVocabularyStore(filepath.Join(t.TempDir(), "models.json"))
	t.Cleanup(vocab.Close)
	factory := newStreamRunnerFactory(sink, "", vocab, streamApprovalConfig{})
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0], WorkDir: t.TempDir()},
		RegistryPath: filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: func(cfg sessions.RunnerConfig) (sessions.Runner, error) {
			reg.Create(conversations.Conversation{ID: "conv", CurrentSessionID: cfg.SessionID})
			return factory(cfg)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sink.live = owner // production installs the owner after bootstrap construction, before workers
	return &daemonLiveBindings{sink: sink, reg: reg}, pool, pool.Default().Runner().(streamRunner)
}

func testInventoryLine(family, value string) []byte {
	menu := fmt.Sprintf(`"models":[{"value":%q,"resolvedModel":%q,"displayName":%q}]`, value, value, value)
	if family == protocol.TypeSlashCommandList {
		menu = fmt.Sprintf(`"commands":[{"name":%q,"description":%q}]`, value, value)
	}
	return []byte(`{"type":"control_response","response":{"subtype":"success","response":{` + menu + "}}}\n")
}

func testPauseInventory(t *testing.T, runner streamRunner, family string) (<-chan struct{}, func()) {
	t.Helper()
	reached, resume := make(chan struct{}), make(chan struct{})
	var once, release sync.Once
	wrap := func(next func(turnevent.Event)) func(turnevent.Event) {
		return func(ev turnevent.Event) {
			once.Do(func() { close(reached); <-resume })
			if next != nil {
				next(ev)
			}
		}
	}
	if family == protocol.TypeModelList {
		runner.models.next = wrap(runner.models.next)
	} else {
		runner.commands.next = wrap(runner.commands.next)
	}
	unblock := func() { release.Do(func() { close(resume) }) }
	t.Cleanup(unblock)
	return reached, unblock
}

func testAwaitInventory(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("inventory did not reach the forwarding barrier")
	}
}

func TestControlLiveInventoryIngress(t *testing.T) {
	for _, family := range []string{protocol.TypeModelList, protocol.TypeSlashCommandList} {
		for _, changedID := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/changed_id=%t", family, changedID), func(t *testing.T) {
				b, pool, runner := testInventoryIngress(t)
				sid := string(pool.BootstrapID())
				original := b.capture("conv", sid, false)
				b.reg.Create(conversations.Conversation{ID: "other", CurrentSessionID: "other-session"})
				other, err := newStreamRunnerFactory(b.sink, "", nil, streamApprovalConfig{})(sessions.RunnerConfig{ClaudeBin: os.Args[0], WorkDir: t.TempDir(), SessionID: "other-session"})
				if err != nil {
					t.Fatal(err)
				}
				otherParser := streamsup.NewParser(other.(streamRunner).models.Sink, discardLogger())
				if _, err := otherParser.Write(testInventoryLine(family, "other-value")); err != nil {
					t.Fatal(err)
				}
				<-b.sink.ch
				otherBefore := testLiveReadings(t, b.sink.live, "other")[liveReadingKey{family, ""}]
				reached, release := testPauseInventory(t, runner, family)
				// Use the real decoder with the factory-built hold/decorator/fan-in chain.
				parser := streamsup.NewParser(runner.models.Sink, discardLogger())
				done := make(chan struct{})
				go func() {
					defer close(done)
					if _, err := parser.Write(testInventoryLine(family, "sonnet")); err != nil {
						t.Errorf("decode inventory: %v", err)
					}
				}()
				t.Cleanup(func() { release(); <-done })
				testAwaitInventory(t, reached)
				nextSID := sid
				if changedID {
					nextSID = "22222222-2222-4222-8222-222222222222"
				}
				b.sink.offerMu.Lock()
				b.sink.live.transition(sessions.SessionTransition{ConversationID: "conv", PreviousID: sessions.SessionID(sid), NewID: sessions.SessionID(nextSID), NextAgent: "claude"})
				b.sink.offerMu.Unlock()
				clear := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{family, ""}]
				release()
				testAwaitInventory(t, done)
				forwarded := <-b.sink.ch
				if forwarded.live == nil || forwarded.live.source.SessionGeneration != original.SessionGeneration || forwarded.source.SessionID != sid || forwarded.incarnation == 0 {
					t.Fatalf("predecessor was retagged in fan-in: %+v", forwarded)
				}
				var cached daemonLiveSource
				if family == protocol.TypeModelList {
					_, cached, _ = runner.ModelListLive()
				} else {
					_, cached, _ = runner.SlashCommandListLive()
				}
				if cached != forwarded.live.source {
					t.Fatalf("cache and forwarding disagree on producing source: cache=%+v fan-in=%+v", cached, forwarded.live.source)
				}
				for _, merged := range []bool{false, true} {
					if family == protocol.TypeModelList {
						if _, ok := modelListFor(b.reg, pool, nil, b)("conv", merged); !ok {
							t.Fatal("legacy cached model reply was refused")
						}
					} else if _, ok := resolveBoundSlashCommandList(b.reg, pool, "conv", b); !ok && !changedID {
						t.Fatal("legacy cached slash reply was refused")
					}
					retained := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{family, ""}]
					if !retained.Envelope.SessionStateCleared || retained.SessionGeneration != clear.SessionGeneration || retained.Revision != clear.Revision {
						t.Fatalf("predecessor forwarding/provider replaced successor clear: %+v", retained)
					}
				}
				otherAfter := testLiveReadings(t, b.sink.live, "other")[liveReadingKey{family, ""}]
				if otherAfter.SessionGeneration != otherBefore.SessionGeneration || otherAfter.Revision != otherBefore.Revision || string(otherAfter.Envelope.Payload) != string(otherBefore.Envelope.Payload) {
					t.Fatal("transition or delayed inventory changed another conversation")
				}
				if changedID {
					if !b.reg.RebindSession(sid, nextSID) {
						t.Fatal("rebind failed")
					}
					fresh, err := newStreamRunnerFactory(b.sink, "", nil, streamApprovalConfig{})(sessions.RunnerConfig{ClaudeBin: os.Args[0], WorkDir: t.TempDir(), SessionID: nextSID})
					if err != nil {
						t.Fatal(err)
					}
					runner = fresh.(streamRunner)
					parser = streamsup.NewParser(runner.models.Sink, discardLogger())
				}
				if _, err := parser.Write(testInventoryLine(family, "sonnet")); err != nil {
					t.Fatal(err)
				}
				fresh := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{family, ""}]
				if fresh.Envelope.SessionStateCleared || fresh.SessionGeneration < clear.SessionGeneration || string(fresh.Envelope.SessionID) != `"`+nextSID+`"` {
					t.Fatalf("identical fresh inventory did not replace clear: %+v", fresh)
				}
			})
		}
	}
}

func TestControlLiveInventoryIngressOvertaking(t *testing.T) {
	for _, family := range []string{protocol.TypeModelList, protocol.TypeSlashCommandList} {
		t.Run(family, func(t *testing.T) {
			b, _, runner := testInventoryIngress(t)
			reached, release := testPauseInventory(t, runner, family)
			parser := streamsup.NewParser(runner.models.Sink, discardLogger())
			done := make(chan struct{})
			go func() {
				defer close(done)
				if _, err := parser.Write(testInventoryLine(family, "old")); err != nil {
					t.Errorf("decode inventory: %v", err)
				}
			}()
			t.Cleanup(func() { release(); <-done })
			testAwaitInventory(t, reached)
			src := b.capture("conv", "", false)
			var newer turnevent.Event = sentinelSlashCommandList("new")
			if family == protocol.TypeModelList {
				newer = turnevent.ModelList{Models: []turnevent.ModelOption{{Value: "new"}}}
			}
			b.sink.live.acceptEvent(src, newer)
			before := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{family, ""}]
			release()
			testAwaitInventory(t, done)
			after := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{family, ""}]
			if string(before.Envelope.Payload) != string(after.Envelope.Payload) || before.Revision != after.Revision {
				t.Fatalf("delayed inventory overtook newer reading: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestControlLiveInventoryStoredEvidence(t *testing.T) {
	models := newSessionModelHold(nil)
	commands := newSessionSlashCommandHold(nil)
	models.Sink(turnevent.ModelList{Models: []turnevent.ModelOption{{Value: "stored"}}})
	commands.Sink(sentinelSlashCommandList("stored"))
	pool, err := sessions.New(sessions.Config{
		Bootstrap:    sessions.SessionConfig{ClaudeBin: os.Args[0]},
		RegistryPath: filepath.Join(t.TempDir(), "sessions.json"),
		RunnerFactory: func(sessions.RunnerConfig) (sessions.Runner, error) {
			return testLiveInventoryRunner{models: models, commands: commands}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := testControlBindings(t, "conv", string(pool.BootstrapID()))
	for _, merged := range []bool{false, true} {
		if _, ok := modelListFor(b.reg, pool, nil, b)("conv", merged); !ok {
			t.Fatal("stored model inventory was refused")
		}
	}
	if _, ok := resolveBoundSlashCommandList(b.reg, pool, "conv", b); !ok {
		t.Fatal("stored slash inventory was refused")
	}
	for _, family := range []string{protocol.TypeModelList, protocol.TypeSlashCommandList} {
		reading := testLiveReadings(t, b.sink.live, "conv")[liveReadingKey{family, ""}]
		if reading.Revision == 0 || len(reading.Envelope.SessionID) != 0 {
			t.Fatalf("stored %s acquired current producer evidence: revision=%d source=%s", family, reading.Revision, reading.Envelope.SessionID)
		}
	}
}

func TestControlLiveInventoryLateOwner(t *testing.T) {
	for _, family := range []string{protocol.TypeModelList, protocol.TypeSlashCommandList} {
		t.Run(family, func(t *testing.T) {
			b, pool, runner := testInventoryIngress(t, true)
			parser := streamsup.NewParser(runner.models.Sink, discardLogger())
			if _, err := parser.Write(testInventoryLine(family, "bootstrap")); err != nil {
				t.Fatal(err)
			}
			forwarded := <-b.sink.ch
			var cached daemonLiveSource
			if family == protocol.TypeModelList {
				_, cached, _ = runner.ModelListLive()
			} else {
				_, cached, _ = runner.SlashCommandListLive()
			}
			if forwarded.live == nil || cached.SessionGeneration == 0 || cached != forwarded.live.source || cached.provenance.SessionID != string(pool.BootstrapID()) {
				t.Fatalf("bootstrap inventory missed the late-installed owner: cache=%+v", cached)
			}
			select {
			case <-b.sink.ch:
				t.Fatal("bootstrap inventory forwarded twice")
			default:
			}
		})
	}
}
