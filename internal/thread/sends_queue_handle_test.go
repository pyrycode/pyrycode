package thread

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestSendQueueHandleValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		field string
		valid bool
	}{
		{``, true}, {`,"queued_msg_id":1`, true}, {`,"queued_msg_id":18446744073709551615`, true},
		{`,"queued_msg_id":0`, false}, {`,"queued_msg_id":null`, false}, {`,"queued_msg_id":-1`, false},
		{`,"queued_msg_id":1.5`, false}, {`,"queued_msg_id":1.0`, false}, {`,"queued_msg_id":1e0`, false},
		{`,"queued_msg_id":"7"`, false}, {`,"queued_msg_id":true`, false}, {`,"queued_msg_id":{}`, false},
		{`,"queued_msg_id":18446744073709551616`, false}, {`,"QUEUED_MSG_ID":0,"queued_msg_id":7`, false},
	} {
		t.Run(tc.field, func(t *testing.T) {
			e := testEntry(100, "send_accepted", `{"text":"safe"`+tc.field+`}`)
			f := testMainReplay(t, []history.Entry{e})
			if (len(f.Items()) == 1) != tc.valid || f.Version() != 100 {
				t.Fatalf("validation: %+v", f.Items())
			}
			if tc.valid && tc.field != "" {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(f.Items()[0].Content, &fields); err != nil {
					t.Fatal(err)
				}
				if string(fields["queued_msg_id"]) != strings.TrimPrefix(tc.field, `,"queued_msg_id":`) {
					t.Fatal("handle precision lost")
				}
			}
		})
	}
}

func TestSendQueueHandleLifetime(t *testing.T) {
	t.Parallel()
	for _, handle := range []string{"", `,"queued_msg_id":7`, `,"QUEUED_MSG_ID":18446744073709551615`} {
		for _, terminal := range []string{"delivered", "dropped", "lost"} {
			t.Run(handle+terminal, func(t *testing.T) {
				accept := testEntry(100, "send_accepted", `{"text":"queued","device_id":"phone","message_id":"same"`+handle+`}`)
				other := testEntry(101, "send_accepted", `{"text":"queued","device_id":"desktop","message_id":"same","queued_msg_id":8}`)
				message := testEntry(102, "message", `{"role":"user","text":"delivered","queued_msg_id":99,"QUEUED_MSG_ID":98}`)
				typ, reason, links := "send_delivered", "delivered", `,"accepted_entry_id":100,"delivery_entry_id":102`
				if terminal == "dropped" {
					typ, reason = "send_dropped", "removed"
				}
				if terminal == "lost" {
					typ, reason = "send_lost", "daemon_restart"
				}
				outcome := testSendOutcome(103, typ, links, reason)
				reused := testEntry(104, "send_accepted", `{"text":"new","queued_msg_id":7}`)
				f := testMainReplay(t, []history.Entry{accept, other, message, outcome, reused, testSendOutcome(105, "send_dropped", `,"accepted_entry_id":100`, "removed")})
				order := uint64(0)
				if terminal == "delivered" {
					order = 102
				}
				item := testSendItem(t, f, 100, order, 103, terminal, false, terminal != "dropped")
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(item.Content, &fields); err != nil {
					t.Fatal(err)
				}
				found := 0
				for k, v := range fields {
					if strings.EqualFold(k, "queued_msg_id") {
						found++
						want := "7"
						if strings.Contains(handle, "184467") {
							want = "18446744073709551615"
						}
						if string(v) != want {
							t.Fatalf("changed handle: %s", v)
						}
					}
				}
				if (found == 1) != (handle != "") || found > 1 {
					t.Fatalf("handle presence: %s", item.Content)
				}
				testContent(t, testSendItem(t, f, 101, 0, 101, "queued", true, true), "queued_msg_id", float64(8))
				testContent(t, testSendItem(t, f, 104, 0, 104, "queued", true, true), "queued_msg_id", float64(7))
			})
		}
	}
}

func TestSendQueueHandleCaseVariants(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		field string
		valid bool
	}{
		{``, true},
		{`,"QUEUED_MSG_ID":7`, true},
		{`,"queued_msg_id":7,"QUEUED_MSG_ID":7`, true},
		{`,"QUEUED_MSG_ID":7,"queued_msg_id":7`, true},
		{`,"queued_msg_id":7,"QUEUED_MSG_ID":8`, false},
		{`,"QUEUED_MSG_ID":8,"queued_msg_id":7`, false},
	} {
		t.Run(tc.field, func(t *testing.T) {
			accept := testEntry(100, "send_accepted", `{"text":"queued"`+tc.field+`}`)
			f := testMainReplay(t, []history.Entry{accept})
			if (len(f.Items()) == 1) != tc.valid || f.Version() != 100 {
				t.Fatalf("case variant validation: %+v", f.Items())
			}
			if !tc.valid {
				return
			}
			want := uint64(7)
			if tc.field == "" {
				want = 0
			}
			check := func(item Item) {
				t.Helper()
				var content struct {
					Handle *uint64 `json:"queued_msg_id"`
				}
				if err := json.Unmarshal(item.Content, &content); err != nil {
					t.Fatal(err)
				}
				if (content.Handle == nil) != (want == 0) || (content.Handle != nil && *content.Handle != want) {
					t.Fatalf("changed handle: %s", item.Content)
				}
			}
			check(testSendItem(t, f, 100, 0, 100, "queued", true, true))
			testFeed(t, f,
				testEntry(101, "message", `{"role":"user","text":"delivered","queued_msg_id":99}`),
				testSendOutcome(102, "send_delivered", `,"accepted_entry_id":100,"delivery_entry_id":101`, "delivered"),
			)
			check(testSendItem(t, f, 100, 101, 102, "delivered", false, true))
		})
	}
}

func TestStoreQueueHandleCacheRules(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		rules   int
		missing bool
	}{
		{"rules_1_missing_handle", 1, true},
		{"rules_2_matching_rows", 2, false},
		{"rules_2_missing_handle", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := history.New(t.TempDir())
			s := NewStore(h)
			testStoreAppend(t, h, testStoreA, testEntry(1, "send_accepted", `{"text":"queued","queued_msg_id":7}`))
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			before := testStoreWait(t, s, testStoreA, StateUsable, 1)
			if err := s.Shutdown(); err != nil {
				t.Fatal(err)
			}
			dir, err := h.LogDir(testStoreA)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, cacheName)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var c cacheRecord
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			c.Rules = tc.rules
			if tc.missing {
				c.Items[0].Content = json.RawMessage(`{"text":"queued"}`)
			}
			raw, err = json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			next := NewStore(h)
			t.Cleanup(func() { _ = next.Shutdown() })
			if err := next.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			after := testStoreWait(t, next, testStoreA, StateUsable, 1)
			if after.Epoch == before.Epoch {
				t.Fatal("old rules retained epoch")
			}
			testContent(t, after.Items[0], "queued_msg_id", float64(7))
		})
	}
}
