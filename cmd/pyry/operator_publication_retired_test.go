package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/history"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
	"github.com/pyrycode/pyrycode/internal/thread"
)

func TestOperatorPublicationRetired(t *testing.T) {
	dir := t.TempDir()
	store := history.New(dir)
	h := testSendHistory(store)
	m := msgqueue.QueuedMessage{ID: 1, MessageID: "app", DeviceID: "device", DeviceName: "phone", ClientVersion: "1", TS: historyTS, Text: "safe text", AttachmentIDs: []string{"attachment"}}
	h.accepted(testConvID, m)
	queued := testCanonicalUsers(t, store, testConvID)
	if len(queued) != 1 || queued[0].Order != 0 || queued[0].Status != "queued" {
		t.Fatalf("accepted canonical item: %+v", queued)
	}
	h.written(testConvID, m)
	written := testCanonicalUsers(t, store, testConvID)
	if len(written) != 1 || written[0].ID != queued[0].ID || written[0].Order != 0 || written[0].Status != "written" {
		t.Fatalf("written canonical item: %+v", written)
	}
	operatorMessageHistory(store, nil, discardLogger(), h)(testConvID, m)
	if len(historyEntries(t, store, testConvID)) != 4 {
		t.Fatal("acceptance, written, delivery source and outcome must survive")
	}
	users := testCanonicalUsers(t, store, testConvID)
	if len(users) != 1 || users[0].ID != queued[0].ID || users[0].Status != "delivered" || users[0].Order == 0 {
		t.Fatalf("delivered canonical item: %+v", users)
	}
	var content map[string]any
	if json.Unmarshal(users[0].Content, &content) != nil || content["device_id"] != "device" || content["text"] != "safe text" {
		t.Fatal("canonical sender/content lost")
	}
	if !reflect.DeepEqual(users, testCanonicalUsers(t, history.New(dir), testConvID)) {
		t.Fatal("reopened canonical item differs")
	}
	if len(testCanonicalUsers(t, store, testConvIDB)) != 0 {
		t.Fatal("cross-conversation user")
	}
}

func testCanonicalUsers(t *testing.T, store *history.Store, conv string) []thread.Item {
	t.Helper()
	f := thread.New(conv)
	if err := f.Feed(historyEntries(t, store, conv)); err != nil {
		t.Fatal(err)
	}
	var users []thread.Item
	for _, item := range f.Items() {
		if item.Kind == "user_message" {
			users = append(users, item)
		}
	}
	return users
}
