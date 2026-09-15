//go:build e2e_realclaude

package realclaude

// #2451 AC-2. Claude expands a CLAUDE.md `@` import that resolves outside the
// session's working directory only when hasClaudeMdExternalIncludesApproved is
// true on the ~/.claude.json entry of the folder that OWNS the CLAUDE.md — not
// the folder the child was spawned in. Production held false on the workspace
// root, so every conversation spawned in a subfolder received the bare root
// CLAUDE.md with all eight import lines unexpanded, and nothing said so: the
// approval dialog is unanswerable headless and skipped imports are not logged.
//
// The check is a two-arm differential because a one-arm pass is also consistent
// with imports expanding unconditionally. The marked arm runs the unit under
// test, trust.MarkWorkdirTrusted, on the workspace root; the control arm
// hand-writes that root's entry into the pre-fix production state (trusted, but
// includes not approved). Same CLAUDE.md shape, same spawn subfolder, opposite
// expectations.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyrycode/tui-driver/pkg/tuidriver"

	"github.com/pyrycode/pyrycode/internal/agentrun"
	"github.com/pyrycode/pyrycode/internal/agentrun/trust"
)

// The sentinel lives ONLY in the imported file, never in CLAUDE.md itself.
// That is what makes a raw-transcript containment check sound rather than a
// guess at the instructions block's schema: an unexpanded import puts the
// literal "@./brain.md" line into the block and leaves the sentinel nowhere,
// so the sentinel's presence is evidence of expansion and of nothing else.
// The prompt asks for a single-word reply, so the model has no reason to emit
// it either. Distinct per arm so cross-contamination between the two
// transcripts fails loudly instead of passing quietly.
const (
	externalIncludeSentinelMarked  = "PYRY-2451-EXTERNAL-INCLUDE-SENTINEL-MARKED-7f3a9c"
	externalIncludeSentinelControl = "PYRY-2451-EXTERNAL-INCLUDE-SENTINEL-CONTROL-4b18de"
)

// writeExternalIncludeWorkspace builds a workspace shaped like production: a
// root CLAUDE.md whose sole import resolves to a sibling file in the root, and
// an empty subfolder to spawn in. From the spawn cwd (<root>/default) the
// import target (<root>/brain.md) is outside the session's working directory,
// which is the condition the approval flag gates. Returns the root and the
// spawn subfolder.
func writeExternalIncludeWorkspace(t *testing.T, home, name, sentinel string) (root, spawnDir string) {
	t.Helper()
	root = filepath.Join(home, name)
	spawnDir = filepath.Join(root, "default")
	if err := os.MkdirAll(spawnDir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", spawnDir, err)
	}
	claudeMD := "# Workspace brain\n\n@./brain.md\n"
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(claudeMD), 0o600); err != nil {
		t.Fatalf("write CLAUDE.md in %s: %v", root, err)
	}
	brain := "# Imported brain file\n\nThe import marker is " + sentinel + ".\n"
	if err := os.WriteFile(filepath.Join(root, "brain.md"), []byte(brain), 0o600); err != nil {
		t.Fatalf("write brain.md in %s: %v", root, err)
	}
	return root, spawnDir
}

// writeUnapprovedEntry puts the pre-fix production state on one projects entry:
// trusted, so claude starts, but external includes not approved. It merges into
// the existing ~/.claude.json rather than replacing it — WithWorktreeAuthenticated
// seeds that file from the operator's own, and clobbering it would drop the
// onboarding state the live tier depends on.
func writeUnapprovedEntry(t *testing.T, home, realpath string) {
	t.Helper()
	dataPath := filepath.Join(home, ".claude.json")
	data, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("read %s: %v", dataPath, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	root := map[string]any{}
	if err := dec.Decode(&root); err != nil {
		t.Fatalf("decode %s: %v", dataPath, err)
	}
	projects, ok := root["projects"].(map[string]any)
	if !ok {
		projects = map[string]any{}
		root["projects"] = projects
	}
	projects[realpath] = map[string]any{
		"hasTrustDialogAccepted":              true,
		"hasClaudeMdExternalIncludesApproved": false,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		t.Fatalf("encode %s: %v", dataPath, err)
	}
	if err := os.WriteFile(dataPath, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write %s: %v", dataPath, err)
	}
}

// runExternalIncludeChild spawns one real child in spawnDir and returns its
// transcript bytes. A single-word reply keeps the turn cheap and keeps the
// model from echoing anything that could be mistaken for an expanded import.
func runExternalIncludeChild(t *testing.T, home, spawnDir string) []byte {
	t.Helper()
	result := RunPyryAgentRun(t, RunOpts{
		Workdir:      spawnDir,
		Prompt:       "Reply with the single word OK and nothing else.",
		SystemPrompt: "You are a test fixture. Answer in one word.",
		AllowedTools: []string{"Read"},
		MaxTurns:     1,
		Effort:       "low",
		Model:        "claude-haiku-4-5",
	})
	if result.ExitCode != 0 {
		t.Fatalf("agent-run exit = %d, want 0\nstdout:\n%s\nstderr:\n%s",
			result.ExitCode, result.Stdout, result.Stderr)
	}
	if result.SessionID == "" {
		t.Fatalf("no session id in stdout:\n%s", result.Stdout)
	}
	path := tuidriver.SessionJSONLPath(home, spawnDir, result.SessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript %s: %v", path, err)
	}
	if len(data) == 0 {
		t.Fatalf("transcript %s is empty; the arm proves nothing", path)
	}
	return data
}

func TestClaudeMdExternalIncludes_SubfolderChildGetsRootImports(t *testing.T) {
	home := WithWorktreeAuthenticated(t)

	markedRoot, markedSpawn := writeExternalIncludeWorkspace(t, home, "ws-marked", externalIncludeSentinelMarked)
	controlRoot, controlSpawn := writeExternalIncludeWorkspace(t, home, "ws-control", externalIncludeSentinelControl)

	// Both arms mutate the same ~/.claude.json, so establish both states
	// before either spawn reads it.
	markedRealpath, err := trust.MarkWorkdirTrusted(markedRoot)
	if err != nil {
		t.Fatalf("MarkWorkdirTrusted(%q): %v", markedRoot, err)
	}
	if markedRealpath == "" {
		t.Fatal("MarkWorkdirTrusted returned an empty realpath")
	}
	// Same realpath rule the helper applies, so the hand-written key is the one
	// claude actually looks up (the macOS /var → /private/var symlink and the
	// on-disk-case fold both matter here).
	controlRealpath, err := agentrun.ResolveWorkdir(controlRoot)
	if err != nil {
		t.Fatalf("ResolveWorkdir(%q): %v", controlRoot, err)
	}
	writeUnapprovedEntry(t, home, controlRealpath)

	// Non-parallel subtests: HOME is pinned by t.Setenv on the parent, which
	// forbids parallel descendants.
	t.Run("marked_root_expands_external_import", func(t *testing.T) {
		data := runExternalIncludeChild(t, home, markedSpawn)
		if !bytes.Contains(data, []byte(externalIncludeSentinelMarked)) {
			t.Fatalf("transcript for %s does not carry the imported sentinel %q; "+
				"the root CLAUDE.md's external import was not expanded even though "+
				"MarkWorkdirTrusted approved includes on %s",
				markedSpawn, externalIncludeSentinelMarked, markedRealpath)
		}
	})

	// The control arm also pins the ticket's second measurement: agent-run
	// pre-marks the folder it spawns in, so this arm's <root>/default entry is
	// fully approved while its root entry is not. Marking the spawn folder must
	// not rescue the import. If claude ever starts honouring the spawn folder's
	// entry this reddens — which is the correct signal, not a flake.
	t.Run("unapproved_root_drops_external_import", func(t *testing.T) {
		data := runExternalIncludeChild(t, home, controlSpawn)
		if bytes.Contains(data, []byte(externalIncludeSentinelControl)) {
			t.Fatalf("transcript for %s carries the imported sentinel %q even though "+
				"%s has hasClaudeMdExternalIncludesApproved=false; the flag is not "+
				"what gates external-import expansion, so the marked arm proves nothing",
				controlSpawn, externalIncludeSentinelControl, controlRealpath)
		}
	})
}
