package sessions

import (
	"fmt"
	"os"
	"path/filepath"
)

// systemPromptText is the text every interactive claude is spawned with, via
// --append-system-prompt-file. APPEND, never replace: the replacing form would
// discard claude's own system prompt, which nobody here intends to discard.
//
// It exists because an assistant reasons about the surface its words land on —
// whether output renders as markdown, whether the operator can interrupt, whether
// there is a terminal at all — and told nothing, it assumes the surface is the
// machine it executes on. Those are different machines. Observed 2026-09-04: a
// markdown rendering fault in the operator's client was diagnosed as Claude
// Code's own renderer, a library "not ours to switch", and a terminal-width
// problem, all of it about the wrong process (#2093).
//
// EVERY SENTENCE IS AN ARCHITECTURE FACT, and that is a constraint on what may be
// added here, not an observation about what is. The text asserts nothing about
// what any client can render or do, because such a claim rots: "this client
// cannot render tables" is false the day the client ships the plugin that renders
// them, and nothing would ever bring the two back into line. Two live instances
// of exactly that cost real time on 2026-09-04. What is stated here is true of
// every client that will ever connect.
//
// TestSystemPromptText_Pinned pins these bytes against an independent
// transcription, which is what makes a future capability claim a visible,
// deliberate diff rather than drift.
//
// The client's own name and version are deliberately absent — the handshake's
// device_name carries a hostname rather than a product name, and the daemon does
// not retain the field at all. That is #2148's, and it is why this ticket ships
// the half that cannot rot.
const systemPromptText = "You are running as a supervised child of the pyry daemon. " +
	"Your replies are not displayed in a terminal: they leave this process as a " +
	"structured stream and are rendered for the operator by a separate client " +
	"application, which may be running on a different machine than the one your " +
	"tools execute on. More than one client can be attached to a session, and " +
	"which one is attached can change while the session runs.\n"

// writeSystemPrompt writes text as the daemon's appended system-prompt file and
// returns its absolute path. Two branches, selected by registryPath, mirroring
// writeMCPSettings:
//
//   - registryPath != "" — the file is <dataDir>/system-prompt.txt, where dataDir
//     is the absolutised parent of registryPath. The name is FIXED rather than
//     derived from a session id because the file is daemon-scoped: the text is
//     identical for every session, so one file serves them all and there is no
//     per-session lifecycle to manage. It is deliberately NOT placed under
//     session-settings/, whose *.json contents are documented to count sessions
//     exactly. The directory is created here because on a cold start nothing has
//     created the data dir yet — saveRegistryLocked has not necessarily run when
//     Pool.New writes this.
//   - registryPath == "" — persistence is disabled (the test-only mode Pool.dataDir
//     reports as ""), so the file goes to os.TempDir with a random name. Most of
//     this package's tests build a pool with no RegistryPath.
//
// text is a PARAMETER rather than a read of systemPromptText, and that is the
// join seam. #2094 (a per-conversation operator prompt) and #2148 (the client's
// name and version) both want to append to the same session's system prompt;
// whichever lands second inherits an append site rather than building one, by
// changing what its caller composes rather than this function.
//
// The write is atomic — scratch file in the target directory, fsync, rename —
// and the mode is os.CreateTemp's 0600, preserved by the rename. Same recipe and
// same reasons as writeMCPSettings: a rename hands a live child either the
// complete old file or the complete new one rather than a truncated prefix, and
// it replaces a symlink at the destination instead of writing through it. The
// 0600 matters for integrity, not confidentiality — the payload is a public
// constant, but anyone who could write this file would control text pyry hands
// claude as a system prompt.
//
// The caller — not this helper — removes the file, at daemon shutdown (Pool.Run's
// defer) and on every error return between the write and its own success. It is
// NOT removed per session: Pool.Remove deletes a session's own --settings file,
// and doing the same to this one would delete it out from under every other live
// session.
func writeSystemPrompt(registryPath, text string) (string, error) {
	// dir == "" selects os.CreateTemp's own os.TempDir contract and final == ""
	// means "keep the scratch name" — the persistence-disabled branch leaves both
	// empty and the data-dir branch sets both.
	var dir, final string
	if registryPath != "" {
		dataDir, err := filepath.Abs(filepath.Dir(registryPath))
		if err != nil {
			return "", fmt.Errorf("sessions: resolve system prompt dir: %w", err)
		}
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return "", fmt.Errorf("sessions: mkdir system prompt dir: %w", err)
		}
		dir = dataDir
		final = filepath.Join(dataDir, "system-prompt.txt")
	}

	pattern := "pyry-system-prompt-*.txt"
	if final != "" {
		// Dotted .tmp suffix so a scratch file left by a SIGKILL inside the write
		// window is never mistaken for the prompt file itself. Mirrors
		// writeMCPSettings' ".settings-*.json.tmp".
		pattern = ".system-prompt-*.txt.tmp"
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("sessions: create system prompt tempfile: %w", err)
	}
	tmpName := f.Name()

	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("sessions: write system prompt: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("sessions: fsync system prompt: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("sessions: close system prompt: %w", err)
	}
	if final == "" {
		return tmpName, nil
	}
	if err := os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("sessions: rename system prompt: %w", err)
	}
	return final, nil
}
