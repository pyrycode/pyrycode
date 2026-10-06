package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

func TestPool_StaleHandoffDelayedActivation(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "retained", true: "recreated"}[restart], func(t *testing.T) {
			root, spawn := t.TempDir(), t.TempDir()
			registry := filepath.Join(root, "sessions.json")
			reg := conversationWithPrompt(convPromptID, nil)
			pool := helperPoolWithConversations(t, registry, t.TempDir(), reg)
			ctx, _ := runPoolInBackground(t, pool)
			id, err := pool.Mint(convPromptID, spawn)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.WriteHandoffNote(convPromptID, noteText); err != nil {
				t.Fatal(err)
			}
			if err := pool.MarkHandoffNoteStale(convPromptID); err != nil {
				t.Fatal(err)
			}
			assertHandoffNoteHolds(t, root, convPromptID, noteText)
			if restart {
				pool = helperPoolWithConversations(t, registry, t.TempDir(), reg)
				ctx, _ = runPoolInBackground(t, pool)
				if _, err := pool.Revive(id, convPromptID, spawn); err != nil {
					t.Fatal(err)
				}
			}
			if err := pool.Activate(ctx, id); err != nil {
				t.Fatal(err)
			}
			path := systemPromptArgPath(t, waitArgvRaw(t, spawn))
			assertComposedFileHolds(t, path, "", systemPromptText+"\n"+staleHandoffWarning+noteSectionOf(noteText))
			if _, err := pool.WriteHandoffNote(convPromptID, noteTextAfter); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.RotateForNewSession(id); err != nil {
				t.Fatal(err)
			}
			assertComposedFileHolds(t, path, "", systemPromptText+"\n"+noteSectionOf(noteTextAfter))
		})
	}
}

func TestPool_HandoffFreshnessIsolationAndUncertainty(t *testing.T) {
	root := t.TempDir()
	registry := filepath.Join(root, "sessions.json")
	pool := handoffPool(t, registry)
	other := conversations.ConversationID(convPromptID)
	if _, err := pool.WriteHandoffNote(handoffConvID, noteText); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.WriteHandoffNote(other, noteTextAfter); err != nil {
		t.Fatal(err)
	}
	if err := pool.MarkHandoffNoteStale(handoffConvID); err != nil {
		t.Fatal(err)
	}
	pool = handoffPool(t, registry)
	if _, fresh := pool.handoffNoteWithFreshness(string(handoffConvID)); fresh {
		t.Fatal("restart lost stale state")
	}
	if _, fresh := pool.handoffNoteWithFreshness(string(other)); !fresh {
		t.Fatal("another conversation lost freshness")
	}
	runPoolInBackground(t, pool)
	for _, conv := range []conversations.ConversationID{handoffConvID, other} {
		id, err := pool.Mint(string(conv), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.RotateForNewSession(id); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(sessionPromptPathOf(root, id))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), staleHandoffWarning) != (conv == handoffConvID) {
			t.Fatal("stale warning leaked across conversation prompt files")
		}
	}
	notePath := handoffNotePathOf(root, other)
	cert := handoffFreshnessPath(notePath)
	info, err := os.Lstat(cert)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("certificate mode: %v, %v", info, err)
	}
	for _, mode := range []string{"missing", "malformed", "symlink", "directory", "mismatched"} {
		t.Run(mode, func(t *testing.T) {
			if err := os.Remove(cert); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "malformed":
				err = os.WriteFile(cert, []byte("fresh"), 0o600)
			case "symlink":
				err = os.Symlink(notePath, cert)
			case "directory":
				err = os.Mkdir(cert, 0o700)
			case "mismatched":
				err = os.WriteFile(cert, []byte(noteTextAfter), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, fresh := pool.handoffNoteWithFreshness(string(other)); fresh {
				t.Fatal("uncertain metadata treated as fresh")
			}
			if mode != "missing" {
				if err := os.Remove(cert); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := pool.WriteHandoffNote(other, noteTextAfter); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPool_HandoffCertificateFailurePreservesOlderBytes(t *testing.T) {
	root := t.TempDir()
	pool := handoffPool(t, filepath.Join(root, "sessions.json"))
	if _, err := pool.WriteHandoffNote(handoffConvID, noteText); err != nil {
		t.Fatal(err)
	}
	cert := handoffFreshnessPath(handoffNotePathOf(root, handoffConvID))
	if err := os.Remove(cert); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cert, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.WriteHandoffNote(handoffConvID, noteTextAfter); err == nil {
		t.Fatal("certificate failure reported written")
	}
	assertHandoffNoteHolds(t, root, handoffConvID, noteText)
	if _, fresh := pool.handoffNoteWithFreshness(string(handoffConvID)); fresh {
		t.Fatal("failed write left a fresh older note")
	}
	pool = handoffPool(t, filepath.Join(root, "sessions.json"))
	if _, fresh := pool.handoffNoteWithFreshness(string(handoffConvID)); fresh {
		t.Fatal("restart treated failed metadata as fresh")
	}
}

func TestPool_StaleHandoffNoUsableNote(t *testing.T) {
	for _, text := range []string{"", " ", "\xff", handoffNoteEndTag} {
		t.Run(text, func(t *testing.T) {
			root := t.TempDir()
			pool := handoffPool(t, filepath.Join(root, "sessions.json"))
			runPoolInBackground(t, pool)
			if _, err := pool.WriteHandoffNote(handoffConvID, text); err != nil {
				t.Fatal(err)
			}
			if err := pool.MarkHandoffNoteStale(handoffConvID); err != nil {
				t.Fatal(err)
			}
			id, err := pool.Mint(string(handoffConvID), "")
			if err != nil {
				t.Fatal(err)
			}
			sess, err := pool.Lookup(id)
			if err != nil {
				t.Fatal(err)
			}
			pool.refreshSystemPromptForRotation(sess)
			raw, err := os.ReadFile(sess.systemPromptPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), staleHandoffWarning) || strings.Contains(string(raw), handoffNoteBegin) {
				t.Fatalf("unusable note rendered: %q", raw)
			}
		})
	}
}

func TestPool_HandoffFreshnessDirectoryFailureSurvivesRestart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires directory permission enforcement")
	}
	root := t.TempDir()
	registry := filepath.Join(root, "sessions.json")
	pool := handoffPool(t, registry)
	if _, err := pool.WriteHandoffNote(handoffConvID, noteText); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(handoffFreshnessPath(handoffNotePathOf(root, handoffConvID)))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := pool.MarkHandoffNoteStale(handoffConvID); err == nil {
		t.Fatal("invalidation refusal not reported")
	}
	if _, err := pool.WriteHandoffNote(handoffConvID, noteTextAfter); err == nil {
		t.Fatal("failed invalidation reported written")
	}
	assertHandoffNoteHolds(t, root, handoffConvID, noteText)
	pool = handoffPool(t, registry)
	if _, fresh := pool.handoffNoteWithFreshness(string(handoffConvID)); fresh {
		t.Fatal("readable old certificate overrode failed invalidation after restart")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.WriteHandoffNote(handoffConvID, noteTextAfter); err != nil {
		t.Fatal(err)
	}
	pool = handoffPool(t, registry)
	if _, fresh := pool.handoffNoteWithFreshness(string(handoffConvID)); !fresh {
		t.Fatal("successful recovery did not clear persisted stale fallback")
	}
}
