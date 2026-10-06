package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/msgqueue"
)

func TestParseConversationPostArgs(t *testing.T) {
	for _, tt := range []struct {
		args []string
		bad  bool
	}{
		{[]string{"--id", "id", "--text", "  hi\n"}, false},
		{[]string{"--id=id", "--file=path"}, false},
		{[]string{"--id=id", "--text="}, false},
		{[]string{"--id=id", "--file="}, false},
		{[]string{}, true}, {[]string{"--id=", "--text=x"}, true},
		{[]string{"--id=id"}, true},
		{[]string{"--id=id", "--text=x", "--file=x"}, true},
		{[]string{"--id", "--text=x"}, true},
		{[]string{"--id=id", "--text", "--file=x"}, true},
		{[]string{"--id=id", "--file", "--text=x"}, true},
		{[]string{"--id=id", "--text"}, true},
		{[]string{"--id=id", "--file"}, true},
		{[]string{"--id=id", "--text=x", "extra"}, true},
		{[]string{"--id=id", "--text=x", "--unknown"}, true},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			id, text, file, err := parseConversationPostArgs(tt.args)
			if (err != nil) != tt.bad {
				t.Fatalf("parse = %q %q %q %v", id, text, file, err)
			}
			if !tt.bad && id != "id" {
				t.Fatalf("id = %q", id)
			}
			if !tt.bad && len(tt.args) == 4 && text != "  hi\n" {
				t.Fatalf("changed text: %q", text)
			}
		})
	}
}

func TestConversationSubmitter_Admission(t *testing.T) {
	for _, tt := range []struct {
		name, binding, wantErr   string
		promoted, full, saveFail bool
	}{
		{name: "chat", binding: dormantWriteBootID},
		{name: "channel", binding: dormantWriteBootID, promoted: true},
		{name: "save failure", binding: dormantWriteBootID, saveFail: true},
		{name: "unknown", wantErr: "unknown conversation"},
		{name: "unbound", wantErr: "conversation session is unavailable"},
		{name: "invalid binding", binding: "private-binding", wantErr: "conversation session is unavailable"},
		{name: "full", binding: dormantWriteBootID, full: true, wantErr: "conversation queue is full"},
		{name: "revival", binding: "28860000-0000-4000-8000-000000000001"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			installIdentityTrustMark(t)
			pool, _ := newDormantWritePool(t, "")
			runPoolReady(t, pool)
			reg, path := newChannelTestRegistry(t, home)
			id := conversations.ConversationID("private-id")
			old := time.Now().Add(-time.Hour).UTC()
			if tt.name != "unknown" {
				reg.Create(conversations.Conversation{ID: id, Cwd: home, CurrentSessionID: tt.binding, IsPromoted: tt.promoted, LastUsedAt: old})
			}
			q, err := msgqueue.New(msgqueue.Config{MaxQueuedPerConversation: 1, Deliver: func(context.Context, string, []byte) error { return errors.New("not running") }, Logger: discardLogger()})
			if err != nil {
				t.Fatal(err)
			}
			if tt.full {
				q.Enqueue(string(id), "backlog")
			}
			before := q.Snapshot(string(id))
			sessionsBefore := len(pool.List())
			if tt.saveFail {
				path = filepath.Join(home, "absent", "registry.json")
			}
			var log bytes.Buffer
			submit := conversationSubmitter(reg, (sessionRouter{pool: pool, convReg: reg}).resolve, q.Enqueue, path, slog.New(slog.NewTextHandler(&log, nil)))
			text := " private-message\n"
			err = submit(string(id), text)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("refusal = %v", err)
				}
				if !reflect.DeepEqual(before, q.Snapshot(string(id))) || len(pool.List()) != sessionsBefore {
					t.Fatal("refusal mutated backlog/sessions")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				got := q.Snapshot(string(id))
				if len(got) != 1 || got[0].Text != text || got[0].ID != 1 {
					t.Fatalf("enqueue = %+v", got)
				}
			}
			row, ok := reg.Get(id)
			if tt.name == "unknown" {
				if ok || len(reg.List()) != 0 || len(q.SnapshotAll()) != 0 {
					t.Fatal("unknown id created state")
				}
			} else if tt.wantErr != "" {
				if !row.LastUsedAt.Equal(old) {
					t.Fatal("refusal touched last-used")
				}
			} else {
				if !row.LastUsedAt.After(old) {
					t.Fatal("acceptance did not touch last-used")
				}
				if !tt.saveFail {
					saved := readSavedConversations(t, path)
					if len(saved) != 1 || !saved[0].LastUsedAt.Equal(row.LastUsedAt) {
						t.Fatal("last-used not persisted")
					}
				}
			}
			if strings.Contains(log.String(), string(id)) || strings.Contains(log.String(), text) || strings.Contains(log.String(), "private-binding") {
				t.Fatalf("input leaked in logs: %s", log.String())
			}
		})
	}
}
