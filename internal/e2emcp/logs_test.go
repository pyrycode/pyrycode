package e2emcp

import (
	"bytes"
	"errors"
	"testing"
)

type refusedLogWriter struct{}

func (refusedLogWriter) Write([]byte) (int, error) { return 0, errors.New("controlled refused pipe") }
func TestServerLogSnapshotRequiresActualSuccessfulForward(t *testing.T) {
	for _, refused := range []bool{false, true} {
		logs := newServerLogs([]string{"mcp-files"})
		var retained, destination bytes.Buffer
		observe := func(raw []byte) error { return logs.append("mcp-files", raw) }
		var e error
		if refused {
			e = copyStderr(&retained, refusedLogWriter{}, bytes.NewBufferString("controlled source stderr\n"), observe)
		} else {
			e = copyStderr(&retained, &destination, bytes.NewBufferString("controlled source stderr\n"), observe)
		}
		snapshot, se := logs.Snapshot()
		if se != nil {
			t.Fatal(se)
		}
		if refused {
			if e == nil || snapshot["mcp-files"] != "" {
				t.Fatal("unforwarded source became prefix evidence")
			}
			continue
		}
		if e != nil || retained.String() != destination.String() || snapshot["mcp-files"] != destination.String() {
			t.Fatal("full successful stderr not retained")
		}
		snapshot["mcp-files"] = "mutated caller copy"
		next, se := logs.Snapshot()
		if se != nil || next["mcp-files"] != destination.String() {
			t.Fatal("caller changed retained source")
		}
	}
	logs := newServerLogs([]string{"mcp-files"})
	if logs.append("foreign", []byte("unowned")) == nil {
		t.Fatal("foreign producer")
	}
	if _, e := logs.Snapshot(); e == nil {
		t.Fatal("poisoned stderr accepted")
	}
}
