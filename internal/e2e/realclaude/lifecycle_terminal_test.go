//go:build e2e_realclaude

package realclaude

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

func publishLifecycleTerminalReceipt(path, root string, frames int, receipt claudetape.LifecycleReceipt, poll func() error) error {
	return daemonRecordingWrite(path, receipt)
}

func TestLifecycleTerminalPublicationWaitsForPrefixAudit(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil { t.Fatal(err) }
	path, root := filepath.Join(home, "terminal"), filepath.Join(home, "root")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	receipt := claudetape.LifecycleReceipt{Success: true, Frames: 3, OwnerPID: 101, BrokerPID: 102}
	go func() {
		done <- publishLifecycleTerminalReceipt(path, root, 2, receipt, func() error { close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
		if _, err := os.Lstat(path); !os.IsNotExist(err) { t.Fatal("terminal published before pinned prefix") }
	case err := <-done:
		t.Fatal("terminal publication did not await prefix audit", err)
	}
	if err := claudetape.PublishLifecycleControl(root, daemonRecordingReceipt{Success: true, Frames: 2, ArtifactSHA256: strings.Repeat("a", 64)}); err != nil { t.Fatal(err) }
	unblock()
	if err := <-done; err != nil { t.Fatal(err) }
	var got claudetape.LifecycleReceipt
	if claudetape.ReadLifecycleControl(path, 16384, &got) != nil || got != receipt { t.Fatal("actual terminal receipt changed") }
}

func TestLifecycleTerminalPublicationRejectsInvalidPrefix(t *testing.T) {
	for _, variant := range []string{"cancelled", "failed-prefix", "foreign-count", "invalid-digest"} {
		t.Run(variant, func(t *testing.T) {
			home := t.TempDir()
			if err := os.Chmod(home, 0700); err != nil { t.Fatal(err) }
			path, root := filepath.Join(home, "terminal"), filepath.Join(home, "root")
			ack := daemonRecordingReceipt{Success: true, Frames: 2, ArtifactSHA256: strings.Repeat("a", 64)}
			switch variant {
			case "failed-prefix": ack.Success = false
			case "foreign-count": ack.Frames++
			case "invalid-digest": ack.ArtifactSHA256 = "unknown"
			}
			if variant != "cancelled" { if err := claudetape.PublishLifecycleControl(root, ack); err != nil { t.Fatal(err) } }
			if publishLifecycleTerminalReceipt(path, root, 2, claudetape.LifecycleReceipt{Success: true}, func() error { return os.ErrDeadlineExceeded }) == nil { t.Fatal("invalid prefix allowed terminal publication") }
			if _, err := os.Lstat(path); !os.IsNotExist(err) { t.Fatal("invalid prefix published terminal") }
		})
	}
}
