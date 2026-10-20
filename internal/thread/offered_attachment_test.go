package thread

import "testing"

func TestOfferedAttachmentIsVisibleAndRetainsItsSource(t *testing.T) {
	t.Parallel()
	source := testEntry(1, "attachment_offered", `{"conversation_id":"a","attachment_id":"file","filename":"offered.txt"}`)
	source.Session = testSource("claude", "source-session")
	fold := New("a")
	testFeed(t, fold, source)
	items := fold.Items()
	if len(items) != 1 || !items[0].Shown || items[0].Subtype != "attachment_offered" || items[0].Session != "source-session" {
		t.Fatalf("visible owning-session offer missing: %#v", items)
	}
	rebuilt := New("a")
	testFeed(t, rebuilt, source)
	if rebuilt.Items()[0].ID != items[0].ID || rebuilt.Items()[0].Order != items[0].Order {
		t.Fatal("fresh fold changed offered file identity or order")
	}
}
