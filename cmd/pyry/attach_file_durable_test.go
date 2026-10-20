package main

import (
	"encoding/json"
	"errors"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

func TestFileAttacher_DurableOfferSurvivesANewHistoryReader(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t)
	workspace := t.TempDir()
	conversation := f.bindConversation("source-session", workspace)
	path := writeFile(t, workspace, "offered.txt", "controlled attachment")
	attachment, err := f.attach("source-session", path)
	if err != nil {
		t.Fatal(err)
	}
	store := history.New(f.instanceDir)
	page, err := store.Page(conversation, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("durable offers = %d, want one after a fresh reader", len(page.Entries))
	}
	entry := page.Entries[0]
	if entry.Type != protocol.TypeAttachmentOffered {
		t.Fatalf("type = %q", entry.Type)
	}
	if entry.Session == nil || entry.Session.Kind != "claude" || entry.Session.SessionID != "source-session" {
		t.Fatalf("offer lost producing session: %#v", entry.Session)
	}
	if entry.Shown == nil || !*entry.Shown {
		t.Fatal("offered file must be displayable")
	}
	var offer protocol.AttachmentOfferedPayload
	if err := json.Unmarshal(entry.Payload, &offer); err != nil {
		t.Fatal(err)
	}
	if offer.ConversationID != string(conversation) || offer.AttachmentID != attachment || offer.Filename != filepath.Base(path) {
		t.Fatal("saved offer differs from the stored file")
	}
	if len(f.offers()) != 1 {
		t.Fatal("legacy offer announcement missing or duplicated")
	}
}

func TestFileAttacher_DurableFailureDoesNotAnnounceSuccess(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t)
	workspace := t.TempDir()
	f.bindConversation("source", workspace)
	path := writeFile(t, workspace, "failed.txt", "controlled")
	calls := 0
	attach := fileAttacher(f.reg, func(sessions.SessionID) error { return nil }, f.instanceDir,
		func(string, string, string, string) error { return errors.New("private storage failure") },
		func(string, string, string) { calls++ }, nil)
	id, err := attach("source", path)
	if !errors.Is(err, errAttachStoreFailed) || id != "" || calls != 0 {
		t.Fatal("failed publication announced success")
	}
	assertContentFree(t, err.Error(), path, "failed.txt", "private storage failure")
}

func TestFileAttacher_ConcurrentOffersStayDurableWithoutRelay(t *testing.T) {
	t.Parallel()
	f := newAttachFixtureWithoutAnnounce(t)
	workspace := t.TempDir()
	conversation := f.bindConversation("source", workspace)
	path := writeFile(t, workspace, "shared.txt", "controlled")
	var wg sync.WaitGroup
	ids := make(chan string, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := f.attach("source", path)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	unique := map[string]bool{}
	for id := range ids {
		unique[id] = true
	}
	page, err := history.New(f.instanceDir).Page(conversation, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(unique) != 4 || len(page.Entries) != 4 {
		t.Fatal("concurrent distinct offers lost or duplicated")
	}
	for i, entry := range page.Entries {
		if entry.ID != uint64(4-i) {
			t.Fatal("history order is not stable")
		}
		var offer protocol.AttachmentOfferedPayload
		if err := json.Unmarshal(entry.Payload, &offer); err != nil {
			t.Fatal(err)
		}
		if !unique[offer.AttachmentID] {
			t.Fatal("history contains a foreign offer")
		}
		delete(unique, offer.AttachmentID)
	}
	if len(unique) != 0 || len(f.offers()) != 0 {
		t.Fatal("missing durable identity or unexpected relay delivery")
	}
}

func TestAttachmentOfferRecorderPreservesProducingHarnessAndRejectsUnavailableSource(t *testing.T) {
	t.Parallel()
	f := newAttachFixture(t)
	conversation := f.bindConversation("source", t.TempDir())
	for _, kind := range []string{"claude", "codex"} {
		store := history.New(t.TempDir())
		record := attachmentOfferRecorder(store, func(id sessions.SessionID) (string, error) {
			if id != "source" {
				t.Fatal("source identity changed")
			}
			return kind, nil
		})
		if err := record(string(conversation), "source", offerAttID, "safe.txt"); err != nil {
			t.Fatal(err)
		}
		page, err := store.Page(conversation, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) != 1 || page.Entries[0].Session.Kind != kind {
			t.Fatal("producing harness not retained")
		}
	}
	store := history.New(t.TempDir())
	record := attachmentOfferRecorder(store, func(sessions.SessionID) (string, error) { return "", sessions.ErrSessionNotFound })
	if err := record(string(conversation), "retired", offerAttID, "private.txt"); err == nil {
		t.Fatal("unavailable source admitted")
	}
	page, err := store.Page(conversation, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 0 {
		t.Fatal("unavailable source published content")
	}
}
