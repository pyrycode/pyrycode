package sessions

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestReadFolderSentence_Pinned pins the read-folder sentence (#2711) against an
// independent transcription, TestSystemPromptText_Pinned's reason: it is part
// of the daemon-wide text every session is spawned with, so a change to it must
// be a visible, deliberate diff. If this went red, ask whether every word is
// still true of the reader the daemon runs, not merely what the new text is.
func TestReadFolderSentence_Pinned(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		folders []string
		want    string
	}{
		{"nil", nil, ""},
		{"empty", []string{}, ""},
		{
			"one",
			[]string{"/Users/op/vault"},
			"This daemon serves markdown files under `/Users/op/vault` to a client " +
				"that asks for one by absolute path.\n",
		},
		{
			"two",
			[]string{"/Users/op/vault", "/srv/notes"},
			"This daemon serves markdown files under `/Users/op/vault` and `/srv/notes` " +
				"to a client that asks for one by absolute path.\n",
		},
		{
			"three, one holding a comma and the word and",
			[]string{"/a", "/b, and c", "/d"},
			"This daemon serves markdown files under `/a`, `/b, and c` and `/d` " +
				"to a client that asks for one by absolute path.\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := readFolderSentence(tc.folders); got != tc.want {
				t.Errorf("readFolderSentence(%q) =\n%q\nwant\n%q", tc.folders, got, tc.want)
			}
		})
	}
}

// TestDaemonPromptText: with no folders the daemon-wide text is the constant byte
// for byte, which is what keeps an unconfigured daemon's prompt files unchanged;
// with folders it is the constant, a blank line, then the sentence.
func TestDaemonPromptText(t *testing.T) {
	t.Parallel()
	if got := daemonPromptText(nil); got != systemPromptText {
		t.Errorf("daemonPromptText(nil) =\n%q\nwant the constant\n%q", got, systemPromptText)
	}
	want := systemPromptText + "\n" +
		"This daemon serves markdown files under `/v` to a client that asks for one by absolute path.\n"
	if got := daemonPromptText([]string{"/v"}); got != want {
		t.Errorf("daemonPromptText([/v]) =\n%q\nwant\n%q", got, want)
	}
}

// TestComposeSystemPromptForOn_SentenceOrder: the read-folder sentence sits after
// systemPromptText and before the client, handoff-note and operator sections, so
// the operator's text stays last. The wanted bytes are assembled from the order
// rule; each section is pinned by its own test.
func TestComposeSystemPromptForOn_SentenceOrder(t *testing.T) {
	t.Parallel()
	folders := []string{"/v"}
	client := ClientIdentity{Name: "Juhanas-MacBook", Version: "0.4.1"}
	const note = "We were halfway through the parser."
	const operator = "Answer in Finnish."

	got := composeSystemPromptForOn(daemonPromptText(folders), operator, []ClientIdentity{client}, note)
	want := systemPromptText + "\n" + readFolderSentence(folders) + "\n" +
		clientSection([]ClientIdentity{client}) + "\n" + handoffNoteSection(note) + "\n" + operator
	if got != want {
		t.Errorf("composed =\n%q\nwant\n%q", got, want)
	}
}

// TestPool_ReadFolders_NamedInEveryComposition (#2711 AC #1) asserts on the
// files, at all three production writes: the bootstrap file New writes, the
// session file buildSession writes at Mint, and the file writeComposedPrompt
// recomposes at Activate. Each must carry the sentence after the constant, and
// the operator's prompt must stay last.
func TestPool_ReadFolders_NamedInEveryComposition(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skipf("benign binary not available: %v", err)
	}
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	spawnDir := t.TempDir()
	folders := []string{"/Users/op/vault", "/srv/notes"}

	operator := "Answer in Finnish."
	reg := conversationWithPrompt("conv-2711", &operator)
	pool, err := New(Config{
		RunnerFactory: recordingRunnerFactory,
		Bootstrap: SessionConfig{
			ClaudeBin:      "/bin/sh",
			ClaudeArgs:     argvRecorderTemplate,
			WorkDir:        tplWorkDir,
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     10 * time.Millisecond,
			BackoffReset:   1 * time.Second,
		},
		Logger:                    slog.New(slog.NewTextHandler(io.Discard, nil)),
		RegistryPath:              regPath,
		ConversationsRegistry:     reg,
		ConversationsRegistryPath: filepath.Join(dir, "conversations.json"),
		ReadFolders:               folders,
	})
	if err != nil {
		t.Fatalf("sessions.New: %v", err)
	}
	client := ClientIdentity{Name: "Pixel 8", Version: "1.2.0"}
	holder := &clientResolverHolder{}
	holder.set(client)
	pool.SetClientIdentityResolver(holder.resolve)

	head := systemPromptText + "\n" +
		"This daemon serves markdown files under `/Users/op/vault` and `/srv/notes` " +
		"to a client that asks for one by absolute path.\n"

	assertFileHolds := func(what, path, want string) {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: read %q: %v", what, path, err)
		}
		if string(raw) != want {
			t.Errorf("%s: %q content =\n%q\nwant\n%q", what, path, raw, want)
		}
	}

	assertFileHolds("bootstrap (New)", promptPathOf(dir), head)

	ctx, _ := runPoolInBackground(t, pool)
	waitArgvRaw(t, tplWorkDir)

	id, err := pool.Mint("conv-2711", spawnDir)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	assertFileHolds("minted (buildSession)", sessionPromptPathOf(dir, id), head+"\n"+operator)

	if err := pool.Activate(ctx, id); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	path := systemPromptArgPath(t, waitArgvRaw(t, spawnDir))
	assertFileHolds("activated (writeComposedPrompt)", path,
		head+"\n"+clientSection([]ClientIdentity{client})+"\n"+operator)
}
