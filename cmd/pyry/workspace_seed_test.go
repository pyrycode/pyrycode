package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// newSeedHarness points $HOME at a temp dir, installs the identity trustMark,
// and returns an empty registry with its save path, the workspace root the seed
// is handed, a creator over a stub mint, and the buffer the seed logs into.
func newSeedHarness(t *testing.T) (home string, reg *conversations.Registry, path, root string, mint *stubMint, create func(cwd, name string) (string, error), logs *bytes.Buffer) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	installIdentityTrustMark(t)
	reg, path = newChannelTestRegistry(t, t.TempDir())
	root = filepath.Join(home, "pyry-workspace")
	mint = &stubMint{returnID: "sess-general"}
	logs = &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	create = channelCreator(reg, mint.mint, path, nil, logger)
	return home, reg, path, root, mint, create, logs
}

func seedLogger(logs *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(logs, nil))
}

// assertSeedLinesPathless fails if any seed event line names a filesystem path.
// channelCreator's own lines are out of scope: they are the channel.new verb's,
// and they log the wrapped detail on purpose.
func assertSeedLinesPathless(t *testing.T, logs *bytes.Buffer, home string) {
	t.Helper()
	for _, line := range strings.Split(logs.String(), "\n") {
		if !strings.Contains(line, "conversations.seed") {
			continue
		}
		if strings.Contains(line, home) || strings.Contains(line, "pyry-workspace") || strings.Contains(line, "conversations.json") {
			t.Errorf("seed log line names a path: %s", line)
		}
	}
}

func loadSeeded(t *testing.T, path string) *conversations.Registry {
	t.Helper()
	back, err := conversations.Load(path)
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	return back
}

// TestSeedDefaultWorkspace_EmptyRegistry covers #2569 AC1 and AC4: a first
// start creates one promoted General channel in the realpath of
// $HOME/pyry-workspace/default, labels exactly that cwd Default workspace, and
// persists the marker beside them.
func TestSeedDefaultWorkspace_EmptyRegistry(t *testing.T) {
	home, reg, path, root, mint, create, logs := newSeedHarness(t)

	seedDefaultWorkspace(reg, path, root, create, seedLogger(logs))

	if mint.calls != 1 {
		t.Fatalf("mint calls = %d, want 1", mint.calls)
	}
	wantCwd, err := filepath.EvalSymlinks(filepath.Join(home, "pyry-workspace", "default"))
	if err != nil {
		t.Fatalf("seeded folder missing: %v", err)
	}

	back := loadSeeded(t, path)
	rows := back.List()
	if len(rows) != 1 {
		t.Fatalf("persisted conversations = %d, want 1", len(rows))
	}
	got := rows[0]
	if !got.IsPromoted || got.IsArchived {
		t.Errorf("General promoted=%v archived=%v, want promoted and active", got.IsPromoted, got.IsArchived)
	}
	if got.Name == nil || *got.Name != "General" {
		t.Errorf("name = %v, want General", got.Name)
	}
	if got.Cwd != wantCwd {
		t.Errorf("cwd = %q, want %q", got.Cwd, wantCwd)
	}
	if label, ok := back.WorkspaceLabel(got.Cwd); !ok || label != "Default workspace" {
		t.Errorf("label under the stored cwd = (%q, %v), want (\"Default workspace\", true)", label, ok)
	}
	if !back.Seeded() {
		t.Error("marker not persisted")
	}
	assertSeedLinesPathless(t, logs, home)
}

// TestSeedDefaultWorkspace_MarkedRegistryLeftAlone covers #2569 AC2: once the
// marker is set, a start creates nothing whatever became of General.
func TestSeedDefaultWorkspace_MarkedRegistryLeftAlone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, reg *conversations.Registry, id conversations.ConversationID)
	}{
		{"renamed", func(t *testing.T, reg *conversations.Registry, id conversations.ConversationID) {
			renamed := "Lobby"
			reg.Update(id, func(c *conversations.Conversation) { c.Name = &renamed })
		}},
		{"archived", func(t *testing.T, reg *conversations.Registry, id conversations.ConversationID) {
			reg.SetArchived(id, true)
		}},
		{"deleted", func(t *testing.T, reg *conversations.Registry, id conversations.ConversationID) {
			reg.Delete(id)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, reg, path, root, mint, create, logs := newSeedHarness(t)
			seedDefaultWorkspace(reg, path, root, create, seedLogger(logs))
			rows := reg.List()
			if len(rows) != 1 {
				t.Fatalf("first seed left %d rows, want 1", len(rows))
			}
			id := rows[0].ID
			tc.change(t, reg, id)
			if err := reg.Save(path); err != nil {
				t.Fatalf("Save: %v", err)
			}
			before := reg.List()

			restarted := loadSeeded(t, path)
			create = channelCreator(restarted, mint.mint, path, nil, seedLogger(logs))
			seedDefaultWorkspace(restarted, path, root, create, seedLogger(logs))

			if mint.calls != 1 {
				t.Errorf("mint calls after restart = %d, want 1", mint.calls)
			}
			after := loadSeeded(t, path).List()
			if len(after) != len(before) {
				t.Fatalf("rows after restart = %d, want %d", len(after), len(before))
			}
			for i := range after {
				if after[i].ID != before[i].ID || after[i].IsArchived != before[i].IsArchived || *after[i].Name != *before[i].Name {
					t.Errorf("row %d changed across restart: before %+v after %+v", i, before[i], after[i])
				}
			}
		})
	}
}

// TestSeedDefaultWorkspace_ExistingConversationsOnlyMarked covers #2569 AC3: a
// host that already holds conversations gets the marker and no channel.
func TestSeedDefaultWorkspace_ExistingConversationsOnlyMarked(t *testing.T) {
	home, reg, path, root, mint, create, logs := newSeedHarness(t)
	reg.Create(conversations.Conversation{
		ID:         "11111111-2222-4333-8444-555555555555",
		Cwd:        filepath.Join(home, "proj"),
		LastUsedAt: time.Now().UTC(),
	})

	seedDefaultWorkspace(reg, path, root, create, seedLogger(logs))

	if mint.calls != 0 {
		t.Errorf("mint calls = %d, want 0", mint.calls)
	}
	back := loadSeeded(t, path)
	if n := len(back.List()); n != 1 {
		t.Errorf("persisted conversations = %d, want the 1 pre-existing", n)
	}
	if !back.Seeded() {
		t.Error("marker not persisted for an existing host")
	}
	if _, err := os.Stat(filepath.Join(home, "pyry-workspace")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("existing host grew a pyry-workspace folder (stat err %v)", err)
	}
}

// TestSeedDefaultWorkspace_CreateFailureRetriesNextStart covers #2569 AC5's
// create arm: a failed create leaves no marker, and the next start seeds.
func TestSeedDefaultWorkspace_CreateFailureRetriesNextStart(t *testing.T) {
	home, reg, path, root, mint, create, logs := newSeedHarness(t)
	mint.returnErr = errors.New("pool not running")

	seedDefaultWorkspace(reg, path, root, create, seedLogger(logs))

	if reg.Seeded() {
		t.Error("marker set in memory after a failed create")
	}
	if len(reg.List()) != 0 {
		t.Errorf("failed create left %d rows", len(reg.List()))
	}
	if back := loadSeeded(t, path); back.Seeded() {
		t.Error("marker persisted after a failed create")
	}
	if !strings.Contains(logs.String(), "event=conversations.seed_failed") {
		t.Errorf("no seed_failed event logged:\n%s", logs.String())
	}
	assertSeedLinesPathless(t, logs, home)

	mint.returnErr = nil
	seedDefaultWorkspace(reg, path, root, create, seedLogger(logs))
	if back := loadSeeded(t, path); !back.Seeded() || len(back.List()) != 1 {
		t.Errorf("retry: seeded=%v rows=%d, want seeded with 1 row", back.Seeded(), len(back.List()))
	}
}

// TestSeedDefaultWorkspace_SaveFailureLeavesNoMarker covers #2569 AC5's save
// arm: the registry path sits under a regular file, so every Save fails and
// nothing — marker included — reaches disk. The daemon keeps running, which in
// a unit test means the call returns.
func TestSeedDefaultWorkspace_SaveFailureLeavesNoMarker(t *testing.T) {
	home, reg, _, root, mint, _, logs := newSeedHarness(t)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	badPath := filepath.Join(blocker, "conversations.json")
	create := channelCreator(reg, mint.mint, badPath, nil, seedLogger(logs))

	seedDefaultWorkspace(reg, badPath, root, create, seedLogger(logs))

	if _, err := os.Stat(badPath); err == nil {
		t.Error("registry file exists despite every Save failing")
	}
	if !strings.Contains(logs.String(), "stage=save") {
		t.Errorf("no save-stage failure logged:\n%s", logs.String())
	}
	assertSeedLinesPathless(t, logs, home)
}

// TestSeedDefaultWorkspace_NoRootOrRowLeavesNoMarker covers the two defensive
// arms: no workspace root (no absolute $HOME), and a created row that is gone
// before the label is keyed on it.
func TestSeedDefaultWorkspace_NoRootOrRowLeavesNoMarker(t *testing.T) {
	for _, tc := range []struct {
		name      string
		root      string
		create    func(cwd, name string) (string, error)
		wantStage string
	}{
		{"no root", "", func(string, string) (string, error) {
			t.Error("create called without a root")
			return "", nil
		}, "stage=root"},
		{"row vanished", "/unused", func(string, string) (string, error) {
			return "11111111-2222-4333-8444-555555555555", nil
		}, "stage=readback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, path := newChannelTestRegistry(t, t.TempDir())
			logs := &bytes.Buffer{}
			seedDefaultWorkspace(reg, path, tc.root, tc.create, seedLogger(logs))
			if reg.Seeded() {
				t.Error("marker set")
			}
			if !strings.Contains(logs.String(), tc.wantStage) {
				t.Errorf("want %s in logs:\n%s", tc.wantStage, logs.String())
			}
		})
	}
}

// TestSeedWhenReady covers the timing contract: the seed runs only once the
// pool is ready — before that Mint orphans a session — and never when the
// daemon context ends first. Either way the returned channel closes.
func TestSeedWhenReady(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		ready := make(chan struct{})
		ran := make(chan struct{}, 2)
		done := seedWhenReady(context.Background(), ready, func() { ran <- struct{}{} })
		select {
		case <-ran:
			t.Fatal("seed ran before the pool was ready")
		case <-time.After(20 * time.Millisecond):
		}
		close(ready)
		<-done
		if len(ran) != 1 {
			t.Errorf("seed ran %d times, want 1", len(ran))
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ran := false
		done := seedWhenReady(ctx, make(chan struct{}), func() { ran = true })
		cancel()
		<-done
		if ran {
			t.Error("seed ran after the daemon context ended")
		}
	})
}
