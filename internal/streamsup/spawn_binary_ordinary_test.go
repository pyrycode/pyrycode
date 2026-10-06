//go:build !e2e_realclaude

package streamsup

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestSpawnBinaryOrdinaryIgnoresActivation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "selection")
	writeSpawnBinarySelection(t, file, "/does/not/exist")
	t.Setenv("PYRY_E2E_CLAUDE_BIN_FILE", file)
	out := &safeBuffer{}
	cfg := helperRunCfg(t, "spawn_selection_witness", out, &safeBuffer{}, "SELECTION_SENTINEL=unchanged")
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cancel, join := runInBackground(t, r)
	defer func() { cancel(); join() }()
	w := waitSpawnBinaryWitness(t, out, 1)
	if w.Args[0] != cfg.ClaudeBin || w.Cwd != cfg.WorkDir || !slices.Contains(w.Env, "SELECTION_SENTINEL=unchanged") {
		t.Fatal("wrong launch: executable, environment or working directory changed")
	}
}
