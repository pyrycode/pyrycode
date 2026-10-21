package thread

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
)

func TestSendEarlyDelivery(t *testing.T) {
	t.Parallel()
	accept := testAcceptance(1, "phone")
	hidden := false
	accept.Shown = &hidden
	message := testEntry(2, "message", `{"conversation_id":"a","role":"user","text":"delivered","accepted_entry_id":1,"queued_msg_id":99,"device_id":"wrong","message_id":"wrong"}`)
	message.Session = testSource("codex", "receiver")
	outcome := testSendOutcome(3, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":2`, "delivered")
	f := New("a")
	testFeed(t, f, accept)
	o, err := f.FeedChanges([]history.Entry{message})
	if err != nil || o.BaselineRequired || len(f.Items()) != 1 {
		t.Fatalf("message prefix: %+v %v", o, err)
	}
	prefix := testSendItem(t, f, 1, 2, 2, "delivered", false, false)
	if prefix.Session != "receiver" || prefix.Agent != "codex" {
		t.Fatal(prefix)
	}
	testContent(t, prefix, "device_id", "phone")
	testContent(t, prefix, "message_id", "same")
	before := f.Items()
	o, err = f.FeedChanges([]history.Entry{outcome})
	if err != nil || o.BaselineRequired || len(o.Changes) != 1 {
		t.Fatalf("outcome: %+v %v", o, err)
	}
	testSendItem(t, f, 1, 2, 3, "delivered", false, false)
	if !reflect.DeepEqual(before, []Item{prefix}) {
		t.Fatal("retained prefix changed")
	}
	testMainReplay(t, []history.Entry{accept, message, outcome})
}

func TestSendEarlyDeliveryReferences(t *testing.T) {
	t.Parallel()
	for _, link := range []string{
		``, `,"accepted_entry_id":0`, `,"accepted_entry_id":null`, `,"accepted_entry_id":"1"`,
		`,"accepted_entry_id":1.0`, `,"accepted_entry_id":-1`, `,"accepted_entry_id":18446744073709551616`,
		`,"accepted_entry_id":9`, `,"accepted_entry_id":3`, `,"accepted_entry_id":2`,
		`,"accepted_entry_id":1,"ACCEPTED_ENTRY_ID":2`,
	} {
		t.Run(link, func(t *testing.T) {
			f := New("a")
			testFeed(t, f, testAcceptance(1, "phone"), testEntry(2, "banner", `{"text":"wrong kind"}`))
			testFeed(t, f, testEntry(3, "message", `{"conversation_id":"a","role":"user","text":"safe"`+link+`}`))
			testSendItem(t, f, 1, 0, 1, "queued", true, true)
			// An explicit bad reference must not be reinterpreted by a later legacy outcome.
			if link != "" {
				testFeed(t, f, testSendOutcome(4, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":3`, "delivered"))
				testSendItem(t, f, 1, 0, 1, "queued", true, true)
			}
		})
	}
	entries := []history.Entry{
		testAcceptance(1, "phone"), testAcceptance(2, "desktop"),
		testEntry(3, "message", `{"conversation_id":"other","role":"user","text":"foreign","accepted_entry_id":1}`),
		testEntry(4, "message", `{"conversation_id":"a","role":"user","text":"first","accepted_entry_id":1,"queued_msg_id":7}`),
		testEntry(5, "message", `{"conversation_id":"a","role":"user","text":"duplicate","accepted_entry_id":1,"queued_msg_id":7}`),
		testSendOutcome(6, "send_delivered", `,"accepted_entry_id":2,"delivery_entry_id":4`, "delivered"),
		testSendOutcome(7, "send_dropped", `,"accepted_entry_id":1`, "removed"),
		testSendOutcome(8, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":4`, "delivered"),
		testSendOutcome(9, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":5`, "delivered"),
		testEntry(10, "message", `{"conversation_id":"a","role":"user","text":"second","accepted_entry_id":2,"queued_msg_id":7}`),
	}
	f := testMainReplay(t, entries)
	testSendItem(t, f, 1, 4, 8, "delivered", false, true)
	testSendItem(t, f, 2, 10, 10, "delivered", false, true)
	for _, id := range []uint64{1, 2} {
		testContent(t, f.Items()[id-1], "device_id", map[uint64]string{1: "phone", 2: "desktop"}[id])
	}
	for _, terminal := range []string{"send_dropped", "send_lost"} {
		t.Run(terminal, func(t *testing.T) {
			reason := map[string]string{"send_dropped": "removed", "send_lost": "daemon_restart"}[terminal]
			f := New("a")
			testFeed(t, f, testAcceptance(1, "phone"), testSendOutcome(2, terminal, `,"accepted_entry_id":1`, reason))
			before := f.Items()[0]
			testFeed(t, f, testEntry(3, "message", fmt.Sprintf(`{"conversation_id":"a","role":"user","text":"late","accepted_entry_id":%d}`, 1)))
			if !reflect.DeepEqual(before, f.Items()[0]) {
				t.Fatal("message changed terminal owner")
			}
		})
	}
}
