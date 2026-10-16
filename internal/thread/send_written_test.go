package thread

import (
	"github.com/pyrycode/pyrycode/internal/history"
	"testing"
)

func TestWrittenSendKeepsAcceptanceUntilPlacement(t *testing.T) {
	f := New("a")
	accept := testAcceptance(1, "phone")
	written := testSendOutcome(2, "send_written", `,"accepted_entry_id":1`, "written")
	testFeed(t, f, accept, written)
	testSendItem(t, f, 1, 0, 2, "written", true, true)
	testFeed(t, f, testSendOutcome(3, "send_written", `,"accepted_entry_id":1`, "written"))
	testSendItem(t, f, 1, 0, 2, "written", true, true)
	testFeed(t, f, testMessage(4), testSendOutcome(5, "send_delivered", `,"accepted_entry_id":1,"delivery_entry_id":4`, "delivered"))
	testSendItem(t, f, 1, 4, 5, "delivered", false, true)
	testFeed(t, f, testSendOutcome(6, "send_written", `,"accepted_entry_id":1`, "written"))
	testSendItem(t, f, 1, 4, 5, "delivered", false, true)
}

func TestWrittenSendCanBecomeLostButCannotBeDropped(t *testing.T) {
	for _, outcome := range []struct {
		typ, reason, status string
		rev                 uint64
		active              bool
	}{
		{"send_lost", "daemon_restart", "lost", 3, false}, {"send_dropped", "removed", "written", 2, true},
	} {
		entries := []history.Entry{testAcceptance(1, "phone"), testSendOutcome(2, "send_written", `,"accepted_entry_id":1`, "written"),
			testSendOutcome(3, outcome.typ, `,"accepted_entry_id":1`, outcome.reason)}
		f := testMainReplay(t, entries)
		testSendItem(t, f, 1, 0, outcome.rev, outcome.status, outcome.active, true)
	}
}
