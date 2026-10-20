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
func retainLifecycleWitnesses(t *testing.T, home, destination, scenario, revision, sourceSHA string) {
	t.Helper()
	paths := []string{"receipt", "finish", "recording-endpoint.json"}
	for _, role := range claudetape.LifecycleProfiles()[scenario].Roles {
		paths = append(paths, filepath.Join("recording-roles", role+".json"))
		for _, suffix := range []string{".job", ".action", ".action.ready", ".receipt", ".prefix", ".stopped", ".completed", ".broker-pid"} {
			paths = append(paths, filepath.Join("recording-children", role+suffix))
		}
	}
	manifest := struct {
		Revision, SourceBinarySHA256 string
		Present, Missing             []string
	}{Revision: revision, SourceBinarySHA256: sourceSHA}
	for _, path := range paths {
		var raw json.RawMessage
		err := claudetape.ReadLifecycleControl(filepath.Join(home, path), claudetape.MaxBytes, &raw)
		if os.IsNotExist(err) {
			manifest.Missing = append(manifest.Missing, path)
			continue
		}
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
