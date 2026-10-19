package thread

import (
	"context"
	"errors"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func TestStoreIdleReloadContinuity(t *testing.T) {
	s, h := testThreadStore(t)
	entries := testStoreAppend(t, h, testStoreA, testMessage(0), testMain(0, "assistant_delta", `,"text":"before"`))
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	base := testStoreWait(t, s, testStoreA, StateUsable, 2)
	for range 2 {
		if err := s.Unload(testStoreA); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, testStoreAppend(t, h, testStoreA,
			testMain(0, "assistant_delta", `,"text":" suffix"`),
			testMain(0, "send_accepted", `,"text":"queued"`))...)
		if err := s.Load(context.Background(), testStoreA); err != nil {
			t.Fatal(err)
		}
		next := testStoreWait(t, s, testStoreA, StateUsable, uint64(len(entries)))
		o, err := s.Changes(t.Context(), testStoreA, base.Epoch, base.Version)
		if err != nil || o.BaselineRequired || o.FromVersion != base.Version || o.Version != next.Version {
			t.Fatalf("suffix range: %+v %v", o, err)
		}
		changedExisting := false
		for _, change := range o.Changes {
			if change.ID == 2 {
				changedExisting = true
				if change.Addition != nil || change.PreviousRev != base.Items[1].Rev || change.Rev <= change.PreviousRev {
					t.Fatalf("existing row lost revision applicability: %+v", change)
				}
			}
		}
		if !changedExisting {
			t.Fatal("existing message did not advance")
		}
		applied := testApplyObservation(t, base.Items, o)
		f := New(string(testStoreA))
		testFeed(t, f, entries...)
		if !sameItems(applied, f.Items()) || !sameItems(applied, next.Items) {
			t.Fatal("suffix did not reproduce replay")
		}
		p, err := s.CompactChanges(t.Context(), testStoreA, base.Epoch, base.Version)
		if err != nil || p.ResetRequired || p.FromVersion != base.Version || len(p.Items) != 2 || len(p.Headers) != 0 {
			t.Fatalf("compact suffix: %+v %v", p, err)
		}
		for _, item := range p.Items {
			if item.ID == 1 {
				t.Fatal("unchanged inventory escaped")
			}
		}
		roots := make(map[uint64]Item, len(next.Items))
		for _, item := range base.Items {
			roots[item.ID] = item
		}
		for _, item := range p.Items {
			roots[item.ID] = item
		}
		if len(roots) != len(next.Items) {
			t.Fatal("compact suffix lost or duplicated roots")
		}
		for _, item := range next.Items {
			if !sameItems([]Item{roots[item.ID]}, []Item{item}) {
				t.Fatalf("compact suffix did not reproduce root %d", item.ID)
			}
		}
		base = next
	}
}

func TestStoreIdleReloadRejectsContinuity(t *testing.T) {
	for _, mode := range []string{"missing boundary", "version", "epoch", "checksum", "watermark", "failed retirement", "dirty checkpoint", "invalid checkpoint", "history gap", "suppression", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			s, h := testThreadStore(t)
			testStoreAppend(t, h, testStoreA, testAcceptance(0, "phone"), testMessage(0))
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			base := testStoreWait(t, s, testStoreA, StateUsable, 2)
			if err := s.Unload(testStoreA); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			p := s.records[testStoreA]
			switch mode {
			case "missing boundary":
				delete(s.records, testStoreA)
			case "version":
				p.Version--
				s.records[testStoreA] = p
			case "epoch":
				p.Epoch = "other"
				s.records[testStoreA] = p
			case "checksum":
				p.Checksum = "other"
				s.records[testStoreA] = p
			case "watermark":
				p.LastShownVersion++
				s.records[testStoreA] = p
			case "failed retirement":
				p.Err = ErrPersistence
				s.records[testStoreA] = p
			}
			s.mu.Unlock()
			if mode == "dirty checkpoint" || mode == "invalid checkpoint" {
				var saved checkpointFileState
				if err := s.readCacheFile(testStoreA, checkpointStateName, &saved); err != nil {
					t.Fatal(err)
				}
				if mode == "dirty checkpoint" {
					saved.Certificate.Clean = false
				} else {
					saved.Certificate.Checksum = "wrong"
				}
				if err := s.writeCacheFile(testStoreA, checkpointStateName, saved); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unavailable" {
				s.beforeFold = func(context.Context, conversations.ConversationID) error { return errors.New("unreadable suffix") }
			}
			if mode == "suppression" {
				testStoreAppend(t, h, testStoreA, testSendOutcome(0, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":2`, "delivered"))
			} else {
				testStoreAppend(t, h, testStoreA, testMessage(0), testMessage(0))
			}
			if mode == "history gap" {
				forward := s.forward
				s.forward = func(id conversations.ConversationID) (storeReader, error) {
					r, err := forward(id)
					return testReloadGapReader{r}, err
				}
			}
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			state := StateUsable
			if mode == "unavailable" {
				state = StateUnavailable
			}
			version := uint64(4)
			if mode == "suppression" {
				version = 3
			}
			if mode == "unavailable" {
				version = 0
			}
			testStoreWait(t, s, testStoreA, state, version)
			o, err := s.Changes(t.Context(), testStoreA, base.Epoch, base.Version)
			if err != nil || (o.State == StateUsable && !o.BaselineRequired) || len(o.Changes) != 0 {
				t.Fatalf("invalid complete continuity: %+v %v", o, err)
			}
			c, err := s.CompactChanges(t.Context(), testStoreA, base.Epoch, base.Version)
			if err != nil || (c.State == StateUsable && !c.ResetRequired) || len(c.Items)+len(c.Headers) != 0 {
				t.Fatalf("invalid compact continuity: %+v %v", c, err)
			}
		})
	}
}

func TestStoreIdleReloadNestedCompact(t *testing.T) {
	s, h := testThreadStore(t)
	entries := testStoreAppend(t, h, testStoreA, testMessage(0),
		testMain(0, "tool_use", `,"tool_use_id":"root","name":"Agent"`),
		testMain(0, "tool_use", `,"parent_tool_use_id":"root","tool_use_id":"nested","name":"Read"`))
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	base := testStoreWait(t, s, testStoreA, StateUsable, 3)
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal(err)
	}
	entries = append(entries, testStoreAppend(t, h, testStoreA,
		testMain(0, "tool_result", `,"parent_tool_use_id":"root","tool_use_id":"nested","is_error":false,"result_detail":"done"`))...)
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	next := testStoreWait(t, s, testStoreA, StateUsable, 4)
	o, err := s.Changes(t.Context(), testStoreA, base.Epoch, base.Version)
	if err != nil || o.BaselineRequired || !sameItems(testApplyObservation(t, base.Items, o), next.Items) {
		t.Fatalf("nested changes: %+v %v", o, err)
	}
	p, err := s.CompactChanges(t.Context(), testStoreA, base.Epoch, base.Version)
	if err != nil || p.ResetRequired || len(p.Items) != 0 || len(p.Headers) != 1 {
		t.Fatalf("nested compact: %+v %v", p, err)
	}
	header := p.Headers[0]
	if header.ID != 2 || header.SubtreeVersion == nil || *header.SubtreeVersion != 4 || header.PublicChildCount == nil || *header.PublicChildCount != 1 {
		t.Fatalf("affected ancestor header: %+v", header)
	}
	f := New(string(testStoreA))
	testFeed(t, f, entries...)
	if !sameItems(next.Items, f.Items()) {
		t.Fatal("nested replay mismatch")
	}
}

type testReloadGapReader struct{ storeReader }

func (r testReloadGapReader) Walk(ctx context.Context, bound uint64, feed func([]history.Entry) error) error {
	return r.storeReader.Walk(ctx, bound, func(entries []history.Entry) error {
		var kept []history.Entry
		for _, e := range entries {
			if e.ID != 3 {
				kept = append(kept, e)
			}
		}
		return feed(kept)
	})
}
