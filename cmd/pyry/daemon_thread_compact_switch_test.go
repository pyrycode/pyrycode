package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// A pending projection participates in publication even while the completed
// join still names the other projection. Admission after the recipient snapshot
// instead waits for the publication boundary before capturing its result.
func TestDaemonThreadCompactSwitch(t *testing.T) {
	for _, target := range []string{protocol.ThreadProjectionCompact, ""} {
		for _, afterSnapshot := range []bool{false, true} {
			name := "to-compact"
			if target == "" {
				name = "to-complete"
			}
			if afterSnapshot {
				name += "/admission-after-snapshot"
			}
			t.Run(name, func(t *testing.T) {
				s, store, _ := testDaemonExpansionFixture(t, 3, 1, 64)
				captured, release := make(chan struct{}), make(chan struct{})
				entered := make(chan struct{})
				var armed atomic.Bool
				var starting atomic.Bool
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				t.Cleanup(unblock)
				hold := func(ctx context.Context) {
					if armed.CompareAndSwap(true, false) {
						close(captured)
						select {
						case <-release:
						case <-ctx.Done():
						}
					}
				}
				started := func() {
					if starting.CompareAndSwap(true, false) {
						close(entered)
					}
				}
				w, mgr := testDaemonThreadQueryWire(t, s, func(c *relay.V2SessionConfig) {
					compact, complete := c.ThreadCompactCatchUp, c.ThreadCatchUp
					c.ThreadCompactCatchUp = func(ctx context.Context, r protocol.ThreadCatchUpPayload) (thread.CompactQueryResult, error) {
						started()
						q, err := compact(ctx, r)
						hold(ctx)
						return q, err
					}
					c.ThreadCatchUp = func(ctx context.Context, r protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
						started()
						q, err := complete(ctx, r)
						hold(ctx)
						return q, err
					}
				})
				w.open("phone", false, protocol.CapabilityThread, protocol.CapabilityThreadExpansion, protocol.CapabilityThreadCompact)
				request := protocol.ThreadCatchUpPayload{ConversationID: testConvID, Limit: 256}
				if target == "" {
					request.Projection = protocol.ThreadProjectionCompact
				}
				testCompactQuery(t, w, mgr, "phone", 1, protocol.TypeThreadCatchUp, request)
				request.Projection = target
				cw := s.workers[testConvID]
				if !afterSnapshot {
					armed.Store(true)
					testDaemonThreadRequest(t, w, "phone", 2, protocol.TypeThreadCatchUp, request)
					awaitSwitchPublication(t, captured)
				} else {
					active := s.publisher.active
					s.publisher.active = func(ctx context.Context, conv string) []relay.ActiveConn {
						peers := active(ctx, conv)
						hold(ctx)
						return peers
					}
					armed.Store(true)
				}
				v := testDaemonThreadAppend(t, s.history, "tool_use", `{"turn_id":"t","parent_tool_use_id":"root","tool_use_id":"switch-late","name":"Read","input":{}}`)
				if afterSnapshot {
					awaitSwitchPublication(t, captured)
					starting.Store(true)
					testDaemonThreadRequest(t, w, "phone", 2, protocol.TypeThreadCatchUp, request)
					awaitSwitchPublication(t, entered)
					unblock()
				}
				testShadowWait(t, func() bool {
					if !cw.lockPublication(t.Context()) {
						return false
					}
					defer cw.unlockPublication()
					return cw.threadVersion >= v
				})
				unblock()
				if afterSnapshot {
					got, typ := testDaemonThreadReply(t, w, 2)
					if typ != protocol.TypeThreadItems || got.Version != v {
						t.Fatalf("post-snapshot query missed publication: %s version %d/%d", typ, got.Version, v)
					}
				} else {
					typ, _ := testCompactReply(t, w, mgr, "phone", 2)
					if typ != protocol.TypeError {
						t.Fatalf("projection switch certified stale capture: %s", typ)
					}
				}
				got, _, _ := testCompactQuery(t, w, mgr, "phone", 3, protocol.TypeThreadCatchUp, request)
				if got.Version != v || got.Projection != target {
					t.Fatalf("retry checkpoint/projection: %+v", got)
				}
				if target == protocol.ThreadProjectionCompact {
					want, err := store.CompactNewestWindow(t.Context(), testConvID, 256)
					if err != nil {
						t.Fatal(err)
					}
					testCompactHeaders(t, got.Headers, want.Headers)
				} else {
					want, err := store.NewestWindow(t.Context(), testConvID, 256)
					if err != nil {
						t.Fatal(err)
					}
					testDaemonThreadRows(t, got.Items, want.Items)
				}
			})
		}
	}
}
