package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
)

const carryTestConvID = "11111111-2222-4333-8444-555555555555"

// newTestCarry builds a carry over a one-row registry saved to a temp path, and
// returns both so a test can assert on the in-memory record and on what reached
// disk. The registry file is real because AC4's whole property is that the pending
// record survives a restart, which on this layer is Save.
func newTestCarry(t *testing.T) (*channelCarry, *conversations.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations.json")
	reg := &conversations.Registry{}
	reg.Create(conversations.Conversation{
		ID:         carryTestConvID,
		Cwd:        "/home/op/ops",
		IsPromoted: true,
		LastUsedAt: time.Date(2026, 9, 16, 8, 0, 0, 0, time.UTC),
	})
	return &channelCarry{reg: reg, path: path, logger: quietLogger()}, reg, path
}

// savedPending reads the pending record back out of the registry FILE, which is
// what a restarting daemon would see. A test asserting only on the in-memory
// registry would pass with no Save wired at all.
func savedPending(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read registry file: %v", err)
	}
	var file struct {
		Conversations []struct {
			ID                  string   `json:"id"`
			PendingChannelPosts []string `json:"pending_channel_posts"`
		} `json:"conversations"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode registry file: %v\n%s", err, raw)
	}
	for _, c := range file.Conversations {
		if c.ID == carryTestConvID {
			return c.PendingChannelPosts
		}
	}
	t.Fatalf("registry file holds no row for %s:\n%s", carryTestConvID, raw)
	return nil
}

// #2499 AC1 + AC3: the posted text goes AHEAD of the operator's reply, in post
// order, and a reply with nothing pending is the identity.
func TestComposeChannelCarry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		posts   []string
		payload string
		want    string
	}{
		{
			name:    "nothing pending is the identity",
			posts:   nil,
			payload: "just my reply",
			want:    "just my reply",
		},
		{
			name:    "empty slice is the identity",
			posts:   []string{},
			payload: "just my reply",
			want:    "just my reply",
		},
		{
			name:    "one post leads the reply",
			posts:   []string{"What are you avoiding?"},
			payload: "The tax return.",
			want:    channelCarryHeader + "\n\nWhat are you avoiding?\n\nThe tax return.",
		},
		{
			name:    "two posts keep post order",
			posts:   []string{"first question", "second question"},
			payload: "answering both",
			want:    channelCarryHeader + "\n\nfirst question\n\nsecond question\n\nanswering both",
		},
		{
			name:    "an empty payload carries the block alone",
			posts:   []string{"only a post"},
			payload: "",
			want:    channelCarryHeader + "\n\nonly a post",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := composeChannelCarry(tt.posts, []byte(tt.payload))
			if string(got) != tt.want {
				t.Errorf("composeChannelCarry:\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// #2499 AC3's second half, pinned as an ALIASING property rather than only as a
// string comparison: a reply with nothing pending must reach claude as the very
// bytes the queue handed over, not a copy that happens to match today.
func TestComposeChannelCarry_IdentityReturnsTheSameBytes(t *testing.T) {
	t.Parallel()
	payload := []byte("unchanged")
	got := composeChannelCarry(nil, payload)
	if &got[0] != &payload[0] {
		t.Errorf("the identity path copied the payload; got %q", got)
	}
}

// #2499 AC4: a post is recorded on the row AND reaches the registry file, so a
// question posted in the morning survives a restart before the evening's reply.
func TestChannelCarry_Record_PersistsToTheRegistryFile(t *testing.T) {
	t.Parallel()
	carry, reg, path := newTestCarry(t)

	carry.record(carryTestConvID, "What are you avoiding?")
	carry.record(carryTestConvID, "And why?")

	want := []string{"What are you avoiding?", "And why?"}
	if got := reg.PendingChannelPosts(carryTestConvID); !reflect.DeepEqual(got, want) {
		t.Errorf("in-memory pending = %q, want %q", got, want)
	}
	if got := savedPending(t, path); !reflect.DeepEqual(got, want) {
		t.Errorf("pending on disk = %q, want %q", got, want)
	}
}

// An unknown row writes nothing AND saves nothing: the Save sits behind the
// append's bool, which is contextUsageRecorder.record's structure.
func TestChannelCarry_Record_UnknownRowTouchesNoDisk(t *testing.T) {
	t.Parallel()
	carry, _, path := newTestCarry(t)

	carry.record("99999999-0000-4000-8000-000000000000", "orphan")

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an unknown id reached the disk: stat %s = %v", path, err)
	}
}

// The inert postures: PTY mode and every unit test that constructs no registry.
func TestChannelCarry_NilPosturesAreInert(t *testing.T) {
	t.Parallel()
	var nilCarry *channelCarry
	inert := &channelCarry{logger: quietLogger()}

	// Neither panics, and neither has anywhere to write.
	nilCarry.record(carryTestConvID, "x")
	inert.record(carryTestConvID, "x")
	nilCarry.clearDelivered(carryTestConvID, msgqueue.QueuedMessage{})
	inert.clearDelivered(carryTestConvID, msgqueue.QueuedMessage{})

	// A nil carry's decorator is the identity: the wrapped seam sees the queue's
	// own bytes, which is the pre-#2499 delivery exactly.
	var seen []byte
	deliver := nilCarry.carryPending(func(_ context.Context, _ string, payload []byte) error {
		seen = payload
		return nil
	})
	if err := deliver(context.Background(), carryTestConvID, []byte("bare")); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if string(seen) != "bare" {
		t.Errorf("a nil carry composed something: %q", seen)
	}
}

// #2499 AC1 + AC2: the delivery seam sees the composed bytes, and the confirmed
// delivery clears exactly what it composed — so a SECOND reply carries only itself.
func TestChannelCarry_CarriesOnceThenTheNextReplyIsBare(t *testing.T) {
	t.Parallel()
	carry, reg, path := newTestCarry(t)
	carry.record(carryTestConvID, "What are you avoiding?")

	var seen []string
	deliver := carry.carryPending(func(_ context.Context, _ string, payload []byte) error {
		seen = append(seen, string(payload))
		return nil
	})

	if err := deliver(context.Background(), carryTestConvID, []byte("The tax return.")); err != nil {
		t.Fatalf("first deliver: %v", err)
	}
	carry.clearDelivered(carryTestConvID, msgqueue.QueuedMessage{Text: "The tax return."})

	if err := deliver(context.Background(), carryTestConvID, []byte("Anything else?")); err != nil {
		t.Fatalf("second deliver: %v", err)
	}
	carry.clearDelivered(carryTestConvID, msgqueue.QueuedMessage{Text: "Anything else?"})

	want := []string{
		channelCarryHeader + "\n\nWhat are you avoiding?\n\nThe tax return.",
		"Anything else?",
	}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("delivered payloads:\n got %q\nwant %q", seen, want)
	}
	if got := reg.PendingChannelPosts(carryTestConvID); got != nil {
		t.Errorf("pending after both deliveries = %q, want nothing", got)
	}
	if got := savedPending(t, path); got != nil {
		t.Errorf("pending on disk after both deliveries = %q, want nothing", got)
	}
}

// #2499 AC2: a delivery that FAILS and is retried at the queue head neither loses
// the carried text nor carries it twice. The clear fires only on the confirmed
// delivery — msgqueue's own OnDelivered contract — so the retry recomposes.
func TestChannelCarry_FailedDeliveryIsRetriedWithTheTextIntact(t *testing.T) {
	t.Parallel()
	carry, reg, _ := newTestCarry(t)
	carry.record(carryTestConvID, "posted question")

	var seen []string
	attempt := 0
	deliver := carry.carryPending(func(_ context.Context, _ string, payload []byte) error {
		seen = append(seen, string(payload))
		attempt++
		if attempt == 1 {
			return errors.New("claude unavailable")
		}
		return nil
	})

	if err := deliver(context.Background(), carryTestConvID, []byte("reply")); err == nil {
		t.Fatal("the first attempt reported success; the fake seam failed it")
	}
	// Nothing was cleared: OnDelivered does not fire for a failed delivery.
	if got := reg.PendingChannelPosts(carryTestConvID); len(got) != 1 {
		t.Fatalf("pending after a failed delivery = %q, want the post still held", got)
	}
	if err := deliver(context.Background(), carryTestConvID, []byte("reply")); err != nil {
		t.Fatalf("retry: %v", err)
	}
	carry.clearDelivered(carryTestConvID, msgqueue.QueuedMessage{Text: "reply"})

	composed := channelCarryHeader + "\n\nposted question\n\nreply"
	if !reflect.DeepEqual(seen, []string{composed, composed}) {
		t.Errorf("attempts:\n got %q\nwant the same composed payload twice", seen)
	}
	if got := reg.PendingChannelPosts(carryTestConvID); got != nil {
		t.Errorf("pending after the confirmed retry = %q, want nothing", got)
	}
}

// #2499 AC2/AC3: a post landing DURING a delivery — the seam can wait a whole
// claude turn before it writes — is not swept away by the clear that follows. This
// is the compose/clear window the registry's refuse-the-newest bound exists for.
func TestChannelCarry_PostDuringDeliverySurvivesTheClear(t *testing.T) {
	t.Parallel()
	carry, reg, _ := newTestCarry(t)
	carry.record(carryTestConvID, "first question")

	var seen string
	deliver := carry.carryPending(func(_ context.Context, _ string, payload []byte) error {
		seen = string(payload)
		// The cron posts again while the head is still being delivered.
		carry.record(carryTestConvID, "second question")
		return nil
	})
	if err := deliver(context.Background(), carryTestConvID, []byte("reply")); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	carry.clearDelivered(carryTestConvID, msgqueue.QueuedMessage{Text: "reply"})

	if want := channelCarryHeader + "\n\nfirst question\n\nreply"; seen != want {
		t.Errorf("delivered payload:\n got %q\nwant %q", seen, want)
	}
	if got := reg.PendingChannelPosts(carryTestConvID); !reflect.DeepEqual(got, []string{"second question"}) {
		t.Errorf("pending after the clear = %q, want the mid-delivery post still held", got)
	}
}

// The seam fires per CONFIRMED delivery, so a clear with no composition behind it
// (another conversation's delivery, or a carry that composed nothing) must not
// reach for whatever happens to be pending.
func TestChannelCarry_ClearWithoutACompositionHoldsTheRecord(t *testing.T) {
	t.Parallel()
	carry, reg, _ := newTestCarry(t)
	carry.record(carryTestConvID, "still unanswered")

	carry.clearDelivered(carryTestConvID, msgqueue.QueuedMessage{Text: "a reply that carried nothing"})

	if got := reg.PendingChannelPosts(carryTestConvID); !reflect.DeepEqual(got, []string{"still unanswered"}) {
		t.Errorf("pending = %q, want the post still held", got)
	}
}

// #2115's record must stay first, and a nil member must not disable the seam for
// the others.
func TestDeliveredFuncs(t *testing.T) {
	t.Parallel()
	if got := deliveredFuncs(); got != nil {
		t.Error("deliveredFuncs() returned a non-nil seam for an empty set")
	}
	if got := deliveredFuncs(nil, nil); got != nil {
		t.Error("deliveredFuncs(nil, nil) returned a non-nil seam; the queue must see it disabled")
	}

	var order []string
	fan := deliveredFuncs(
		func(string, msgqueue.QueuedMessage) { order = append(order, "history") },
		nil,
		func(string, msgqueue.QueuedMessage) { order = append(order, "carry") },
	)
	fan(carryTestConvID, msgqueue.QueuedMessage{ID: 1})
	if !reflect.DeepEqual(order, []string{"history", "carry"}) {
		t.Errorf("fan-out order = %v, want history then carry", order)
	}
}

// #2499 AC4: a question posted in the morning is still carried by a reply that
// evening, across a daemon restart in between.
//
// The restart is simulated at the joint that matters — conversations.Load against
// the file the first carry saved, which is exactly what the daemon does once at
// startup — rather than by respawning a binary. The links on either side are proven
// where they live: the registry package round-trips the field through Save→Load, and
// TestChannelCarry_Record_PersistsToTheRegistryFile proves the bytes reach the file.
// This is the one joint neither of those covers: that a carry built over a RELOADED
// registry composes the pending text into the next delivery.
func TestChannelCarry_PendingSurvivesARestart(t *testing.T) {
	t.Parallel()
	morning, _, path := newTestCarry(t)
	morning.record(carryTestConvID, "What are you avoiding?")

	reloaded, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("reload the registry the way the daemon does at startup: %v", err)
	}
	evening := &channelCarry{reg: reloaded, path: path, logger: quietLogger()}

	var seen string
	deliver := evening.carryPending(func(_ context.Context, _ string, payload []byte) error {
		seen = string(payload)
		return nil
	})
	if err := deliver(context.Background(), carryTestConvID, []byte("The tax return.")); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	want := channelCarryHeader + "\n\nWhat are you avoiding?\n\nThe tax return."
	if seen != want {
		t.Errorf("after a restart the evening's reply carried:\n got %q\nwant %q", seen, want)
	}

	// And it is still carried exactly once: the clear reaches the reloaded row.
	evening.clearDelivered(carryTestConvID, msgqueue.QueuedMessage{Text: "The tax return."})
	if got := reloaded.PendingChannelPosts(carryTestConvID); got != nil {
		t.Errorf("pending after the post-restart delivery = %q, want nothing", got)
	}
}
