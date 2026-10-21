package thread

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestSendAttachmentFacts(t *testing.T) {
	t.Parallel()
	const usable = `[{"attachment_id":"b","filename":"second.txt"},{"attachment_id":"a","filename":"first.txt"}]`
	for _, tc := range []struct{ name, field, want string }{
		{"recorded order", `,"attachments":` + usable, usable},
		{"missing", ``, ``},
		{"null", `,"attachments":null`, ``},
		{"object", `,"attachments":{"attachment_id":"a","filename":"wrong.txt"}`, ``},
		{"scalar", `,"attachments":"wrong"`, ``},
		{"empty", `,"attachments":[]`, ``},
		{"unusable", `,"attachments":[null,4,false,[],{},{"attachment_id":"a","filename":null}]`, ``},
		{"mixed", `,"attachments":[null,{"attachment_id":"b","filename":"second.txt","path":"/private/host"},4,{"attachment_id":8,"filename":"bad.txt"},{"attachment_id":"a","filename":false},{"attachment_id":"a"},{"filename":"missing.txt"},{"attachment_id":"other","filename":"unrelated.txt"},{"attachment_id":"a","filename":"/private/host"},{"attachment_id":"a","filename":"..\\host"},{"attachment_id":"a","filename":"C:host"},{"attachment_id":"a","filename":"."},{"attachment_id":"a","filename":".."},{"attachment_id":"a","filename":""},{"attachment_id":"a","filename":"first.txt"}]`, usable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accept := testEntry(1, "send_accepted", `{"text":"queued","attachment_ids":["a","b"],"device_id":"phone","message_id":"accepted","queued_msg_id":7,"accepted_at":"2026-01-01T00:00:00Z","client_sent_at":"then"`+tc.field+`}`)
			accept.Session = testSource("claude", "sender")
			message := testEntry(3, "message", `{"role":"user","text":"safe delivered","attachment_ids":["a","b"],"attachments":[{"attachment_id":"a","filename":"forged.txt"}],"ATTACHMENTS":[{"attachment_id":"b","filename":"/host/path"}],"queued_msg_id":99,"device_id":"wrong"}`)
			message.Session = testSource("codex", "receiver")
			entries := []history.Entry{accept, testSendOutcome(2, "send_written", `,"accepted_entry_id":1`, "written"), message, testSendOutcome(4, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":3`, "delivered"), testSendOutcome(5, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":3`, "delivered")}
			ref, source := testReferenceFold(entries)
			inline := New("a")
			for i, e := range entries {
				source.chunk = entries[i : i+1]
				testFeed(t, inline, e)
				testFeed(t, ref, e)
				source.remember(source.chunk)
				source.chunk = nil
				if !reflect.DeepEqual(ref.Items(), inline.Items()) {
					t.Fatalf("fold modes differ after %s", e.Type)
				}
				if e.ID == 2 {
					item := testSendItem(t, inline, 1, 0, 2, "written", true, true)
					var fields map[string]json.RawMessage
					_ = json.Unmarshal(item.Content, &fields)
					var original map[string]json.RawMessage
					_ = json.Unmarshal(accept.Payload, &original)
					if !reflect.DeepEqual(fields["attachments"], original["attachments"]) {
						t.Fatal("written revision changed acceptance facts")
					}
				}
			}
			// Force later rendering to resolve the acceptance from history again.
			for id := uint64(6); id < 300; id++ {
				source.remember([]history.Entry{testEntry(id, "ignored", `{}`)})
			}
			for _, f := range []*Fold{inline, ref} {
				item := testSendItem(t, f, 1, 3, 4, "delivered", false, true)
				if item.Session != "receiver" || item.Agent != "codex" || item.Summary != "safe delivered" {
					t.Fatal("delivery placement/provenance changed", item)
				}
				for key, want := range map[string]any{"text": "safe delivered", "attachment_ids": []any{"a", "b"}, "device_id": "phone", "message_id": "accepted", "queued_msg_id": float64(7), "accepted_at": "2026-01-01T00:00:00Z", "client_sent_at": "then"} {
					testContent(t, item, key, want)
				}
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(item.Content, &fields)
				if _, exists := fields["ATTACHMENTS"]; exists {
					t.Fatal("delivery supplied case-variant attachment names")
				}
				var got, want any
				_ = json.Unmarshal(fields["attachments"], &got)
				_ = json.Unmarshal([]byte(tc.want), &want)
				if !reflect.DeepEqual(got, want) || (tc.want == "" && fields["attachments"] != nil) {
					t.Fatalf("attachment facts = %s, want %s", fields["attachments"], tc.want)
				}
			}
		})
	}
}

func TestStoreAttachmentCacheRules(t *testing.T) {
	t.Parallel()
	if foldingRules <= 6 {
		t.Fatal("attachment semantics require rules newer than merged main's 6")
	}
	h := history.New(t.TempDir())
	s := NewStore(h)
	entries := []history.Entry{
		testEntry(1, "send_accepted", `{"text":"queued","attachment_ids":["a"],"attachments":[{"attachment_id":"a","filename":"recorded.txt"}]}`),
		testEntry(2, "message", `{"role":"user","text":"safe delivered","attachment_ids":["a"]}`),
		testSendOutcome(3, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":2`, "delivered"),
	}
	for _, e := range entries {
		testStoreAppend(t, h, testStoreA, e)
	}
	if err := s.Load(t.Context(), testStoreA); err != nil {
		t.Fatal(err)
	}
	before := testStoreWait(t, s, testStoreA, StateUsable, 3)
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	dir, err := h.LogDir(testStoreA)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve matching checksum/version/watermark on both authority surfaces.
	for _, leaf := range []string{cacheName, checkpointStateName} {
		path := filepath.Join(dir, leaf)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var c cacheRecord
		var state checkpointFileState
		if leaf == cacheName {
			err = json.Unmarshal(raw, &c)
			c.Rules = 6
		} else {
			err = json.Unmarshal(raw, &state)
			state.Certificate.Rules = 6
		}
		if err != nil {
			t.Fatal(err)
		}
		var value any = c
		if leaf == checkpointStateName {
			value = state
		}
		raw, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	next := NewStore(h)
	t.Cleanup(func() { _ = next.Shutdown() })
	if err := next.Load(t.Context(), testStoreA); err != nil {
		t.Fatal(err)
	}
	after := testStoreWait(t, next, testStoreA, StateUsable, 3)
	if after.Epoch == before.Epoch {
		t.Fatal("prior attachment rules retained epoch")
	}
	testContent(t, after.Items[0], "attachments", []any{map[string]any{"attachment_id": "a", "filename": "recorded.txt"}})
}
