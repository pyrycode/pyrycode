//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

// retainLifecycleWitnesses copies only closed control artifacts, never transcripts.
// It runs after broker cleanup and before the disposable home is removed.
func retainLifecycleWitnesses(t *testing.T, home, destination, scenario, revision, sourceSHA, daemonSHA string) {
	t.Helper()
	paths := []string{"receipt", "finish", "recording-endpoint.json"}
	for _, role := range claudetape.LifecycleProfiles()[scenario].Roles {
		paths = append(paths, filepath.Join("recording-roles", role+".json"))
		for _, suffix := range []string{".job", ".action", ".action.ready", ".receipt", ".prefix", ".stopped", ".completed", ".broker-pid"} {
			paths = append(paths, filepath.Join("recording-children", role+suffix))
		}
	}
	manifest := struct {
		Revision, SourceBinarySHA256, DaemonSHA256 string
		Present, Missing                           []string
	}{Revision: revision, SourceBinarySHA256: sourceSHA, DaemonSHA256: daemonSHA}
	for _, path := range paths {
		if _, err := os.Lstat(filepath.Join(home, path)); os.IsNotExist(err) {
			manifest.Missing = append(manifest.Missing, path)
			continue
		}
		var raw json.RawMessage
		err := claudetape.ReadLifecycleControl(filepath.Join(home, path), claudetape.MaxBytes, &raw)
		if err != nil {
			t.Errorf("cannot retain lifecycle witness %s: %v", path, err)
			continue
		}
		name := filepath.Join(destination, filepath.Base(filepath.Dir(path))+"-"+filepath.Base(path))
		file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Error(err)
			continue
		}
		_, writeErr := file.Write(raw)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Errorf("cannot retain lifecycle witness %s", path)
		}
		manifest.Present = append(manifest.Present, path)
	}
	file, err := os.OpenFile(filepath.Join(destination, "source-and-inventory.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Error(err)
		return
	}
	if err := json.NewEncoder(file).Encode(manifest); err != nil {
		t.Error(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
}

func TestRetainLifecycleWitnesses(t *testing.T) {
	home, destination := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "recording-children"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := claudetape.PublishLifecycleControl(filepath.Join(home, "recording-children", "bound.action"), claudetape.LifecycleKill{PID: 42, Signal: "rotate-session"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "recording-children", "bound.json"), []byte("private transcript"), 0600); err != nil {
		t.Fatal(err)
	}
	retainLifecycleWitnesses(t, home, destination, "clear-reset", "revision", "source", "daemon")
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 2 {
		t.Fatalf("retained inventory: files=%d err=%v", len(entries), err)
	}
	var action claudetape.LifecycleKill
	if err := claudetape.ReadLifecycleControl(filepath.Join(destination, "recording-children-bound.action"), 16384, &action); err != nil || action.PID != 42 {
		t.Fatal("original action missing", err)
	}
	var manifest struct {
		Revision, SourceBinarySHA256, DaemonSHA256 string
		Present, Missing                           []string
	}
	if err := claudetape.ReadLifecycleControl(filepath.Join(destination, "source-and-inventory.json"), 16384, &manifest); err != nil || manifest.DaemonSHA256 != "daemon" || len(manifest.Present) != 1 || len(manifest.Missing) == 0 {
		t.Fatal("incomplete source/missing inventory", err)
	}
}
