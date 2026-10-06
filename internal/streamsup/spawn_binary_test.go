package streamsup

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

type spawnBinaryWitness struct {
	Args, Env []string
	Cwd       string
}

func waitSpawnBinaryWitness(t *testing.T, out *safeBuffer, count int) spawnBinaryWitness {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		var witnesses []spawnBinaryWitness
		for _, line := range lines {
			var w spawnBinaryWitness
			if json.Unmarshal([]byte(line), &w) == nil && len(w.Args) > 0 {
				witnesses = append(witnesses, w)
			}
		}
		if len(witnesses) >= count {
			return witnesses[count-1]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("missing executable witness %d: %s", count, out.String())
	return spawnBinaryWitness{}
}

func writeSpawnBinarySelection(t *testing.T, file, bin string) {
	t.Helper()
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(bin+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, file); err != nil {
		t.Fatal(err)
	}
}
