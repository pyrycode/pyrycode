package thread

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func testReferenceSendEntries() []history.Entry {
	return []history.Entry{
		testEntry(1, "send_accepted", `{"text":" queued <&>\n","DEVICE_ID":"first","device_id":"last","MESSAGE_ID":"sender","queued_msg_id":18446744073709551615,"accepted_at":"2026-01-01T00:00:00Z","client_sent_at":"then"}`),
		testEntry(2, "message", `{"role":"user","text":" delivered <&>\u2003\u0001","DEVICE_ID":"receiver","message_id":"receiver","QUEUED_MSG_ID":99,"future":[1, 2]}`),
		testSendOutcome(3, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":2`, "delivered"),
	}
}

func TestReferenceSendRendering(t *testing.T) {
	for _, terminal := range []string{"queued", "delivered", "dropped", "lost"} {
		for _, handle := range []bool{false, true} {
			t.Run(fmt.Sprint(terminal, handle), func(t *testing.T) {
				entries := testReferenceSendEntries()
				if !handle {
					entries[0] = testAcceptance(1, "phone")
				}
				entries[0].Session, entries[1].Session = testSource("claude", "sender"), testSource("none", "")
				if terminal == "queued" {
					entries = entries[:2]
				} else if terminal != "delivered" {
					reason := "removed"
					if terminal == "lost" {
						reason = "daemon_restart"
					}
					entries[2] = testSendOutcome(3, "send_"+terminal, `,"accepted_entry_id":1`, reason)
				}
				entries = append(entries,
					testSendOutcome(4, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":0`, "delivered"),
					testSendOutcome(5, "send_lost", `,"accepted_entry_id":1`, "daemon_restart"),
					testEntry(6, "send_accepted", `{"text":"reused","queued_msg_id":18446744073709551615,"message_id":"sender"}`),
				)
				f, source := testReferenceFold(entries)
				legacy := New("a")
				for i, e := range entries {
					source.chunk = entries[i : i+1]
					want, err := legacy.FeedChanges([]history.Entry{e})
					if err != nil {
						t.Fatal(err)
					}
					got, err := f.FeedChanges([]history.Entry{e})
					if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(f.touches, legacy.touches) {
						t.Fatalf("entry %d observation/touches differ: %v\ngot %#v\nwant %#v", e.ID, err, got, want)
					}
					source.remember(source.chunk)
					source.chunk = nil
					for _, item := range f.items {
						if len(item.Content) != 0 || item.Summary != "" {
							t.Fatalf("send retained content/summary: %d", item.ID)
						}
						for _, field := range f.recipes[item.ID].fields {
							if len(field.value) != 0 {
								t.Fatal("send recipe retained payload")
							}
						}
					}
				}
				for id := uint64(7); id < 300; id++ {
					source.remember([]history.Entry{testEntry(id, "ignored", `{}`)})
				}
				got, err := f.Render()
				if err != nil || !reflect.DeepEqual(got, legacy.Items()) || f.Version() != legacy.Version() || f.lastShown != legacy.lastShown {
					t.Fatal("eviction changed items/versions", err)
				}
				got[0].Content[0] = 'X'
				if !reflect.DeepEqual(f.Items(), legacy.Items()) {
					t.Fatal("returned content was not detached")
				}
			})
		}
	}
}

func TestReferenceSendSourceFailure(t *testing.T) {
	for _, missing := range []uint64{1, 2, 3} {
		for _, failure := range []string{"missing", "unreadable", "malformed", "null"} {
			t.Run(fmt.Sprint(missing, failure), func(t *testing.T) {
				entries := testReferenceSendEntries()
				f, source := testReferenceFold(entries)
				source.chunk = entries
				testFeed(t, f, entries...)
				source.remember(entries)
				source.chunk = nil
				for id := uint64(4); id < 300; id++ {
					source.remember([]history.Entry{testEntry(id, "ignored", `{}`)})
				}
				lookup := source.lookup
				source.lookup = func(ids []uint64) ([]history.Entry, error) {
					out, err := lookup(ids)
					if failure == "unreadable" {
						return nil, errors.New("private content/path")
					}
					if failure == "missing" {
						return slices.DeleteFunc(out, func(e history.Entry) bool { return e.ID == missing }), err
					}
					for i := range out {
						if out[i].ID == missing {
							out[i].Payload = []byte(`{"private"`)
							if failure == "null" {
								out[i].Payload = []byte(`null`)
							}
						}
					}
					return out, err
				}
				if items, err := f.Render(); items != nil || err != ErrUnavailable {
					t.Fatal("source failure exposed partial result", err)
				}
				if o := f.Observe(); o.State != StateUnavailable || o.Err != ErrUnavailable || o.Items != nil || o.Version != 0 || o.LastShownVersion != 0 {
					t.Fatal("source failure exposed usable observation")
				}
				source.lookup = lookup
				if _, err := f.Render(); err != nil {
					t.Fatal("repair failed", err)
				}
			})
		}
	}
}

func TestStoreReferenceSendRetry(t *testing.T) {
	for _, missing := range []uint64{1, 2, 3} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			if missing == 3 {
				// The immutable outcome is only reread after eviction on replay;
				// its terminal tail publication resolves it from the current chunk.
				testStoreReferenceRetry(t, testReferenceSendEntries(), missing, "replay")
				return
			}
			testStoreReferenceRetry(t, testReferenceSendEntries(), missing)
		})
	}
}
