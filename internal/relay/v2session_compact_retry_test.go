package relay

import (
	"context"
	"maps"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/thread"
)

// TestThreadCompactPushCompletionBoundary holds the refusal writer so the live
// row cannot reach the hosted head until after FlushPushes and query admission.
func TestThreadCompactPushCompletionBoundary(t *testing.T) {
	provider := make(chan context.Context, 2)
	writerEntered, releaseWriter := make(chan struct{}), make(chan struct{})
	var hold atomic.Bool
	var calls testCompactCalls
	p := testHostedStartCaps(t, &testAppDiscovery{}, compactCaps, func(c *V2SessionConfig) {
		testCompactConfig(&calls, nil, nil)(c)
		c.ThreadCatchUp = func(ctx context.Context, _ protocol.ThreadCatchUpPayload) (thread.QueryResult, error) {
			provider <- ctx
			<-ctx.Done()
			return thread.QueryResult{}, ctx.Err()
		}
		send := c.HostedAppsSend
		c.HostedAppsSend = func(ctx context.Context, env protocol.RoutingEnvelope) error {
			err := send(ctx, env)
			if err != nil || !hold.CompareAndSwap(true, false) {
				return err
			}
			close(writerEntered)
			select {
			case <-releaseWriter:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
	release := func() {
		select {
		case <-releaseWriter:
		default:
			close(releaseWriter)
		}
	}
	t.Cleanup(release)
	joins := map[string]threadJoin{catchUpConversation: {"epoch", 20, protocol.ThreadProjectionCompact}}
	testCompactSeed(p, joins)
	awaitProvider := func() context.Context {
		t.Helper()
		select {
		case ctx := <-provider:
			return ctx
		case <-time.After(3 * time.Second):
			t.Fatal("complete query did not reach provider")
			return nil
		}
	}
	testCompactCatchUp(p, 40, catchUpConversation, 0, "")
	first := awaitProvider()
	hold.Store(true)
	live := testCompactItemChanged(catchUpConversation, 91)
	// Split pushSource's cancellation from its enqueue to pin the refusal first.
	p.mgr.cancelThreadQuery(p.conn, live)
	testHostedAwait(t, first.Done())
	testHostedAwait(t, writerEntered)
	testCatchUpError(t, p, 40)
	if err := p.mgr.Push(t.Context(), p.conn, live); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := p.mgr.FlushPushes(ctx); err != nil {
		t.Fatal(err)
	}
	testCompactCatchUp(p, 41, catchUpConversation, 0, "")
	// This control runs on Run after the query's admission, even while its
	// provider worker is still awaiting the first refusal's delivery outcome.
	p.request(42, protocol.TypeAppCancel, `{"request_id":999}`)
	p.waitEvent("v2.hosted.queued", protocol.TypeError)
	p.mgr.hostedMu.Lock()
	query := p.mgr.hostedInboxes[p.conn].query
	p.mgr.hostedMu.Unlock()
	if query == nil || query.id != 41 {
		t.Fatal("premature query was not admitted")
	}
	premature := query.ctx
	if err := premature.Err(); err != nil {
		t.Fatalf("query cancelled before hosted writer release: %v", err)
	}
	if got := testCompactJoins(p); !maps.Equal(got, joins) {
		t.Fatalf("refusal or flush changed compact join: %v", got)
	}
	release()
	testHostedAwait(t, premature.Done())
	if e, _ := p.next(); e.InReplyTo == nil || *e.InReplyTo != 42 {
		t.Fatalf("control admission sentinel: %+v", e)
	}
	// This deliberately premature request diagnoses the invalid boundary; the
	// valid retry after acknowledged handling is covered by LiveInFlight.
	testCatchUpError(t, p, 41)
	if got := testCompactJoins(p); !maps.Equal(got, joins) {
		t.Fatalf("premature query changed compact join: %v", got)
	}
	testCompactQuiet(t, p, 50)
}

// testCompactHostedFence acknowledges all earlier hosted output after a push
// flush. The foreign unjoined compact row is withheld without cancelling the
// tested query, mutating its join or emitting a wire frame.
func testCompactHostedFence(t *testing.T, p *testHostedPeer) {
	t.Helper()
	if err := testDeliverLive(t, p, testCompactUpdate(t, compactOther, "epoch", 91, "progress")); err != ErrThreadWithheld {
		t.Fatalf("hosted completion fence: %v", err)
	}
}
