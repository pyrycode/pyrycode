//go:build e2e_realclaude

package realclaude

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

func publishLifecycleTerminalReceipt(path, root string, frames int, receipt claudetape.LifecycleReceipt, poll func() error) error {
	// Real EOF may precede the prefix audit. Keep its closed inventory stable
	// until the original root publishes the digest it actually audited.
	for receipt.Success {
		if _, err := os.Lstat(root); os.IsNotExist(err) {
			if err := poll(); err != nil {
				return err
			}
			continue
		}
		var ack daemonRecordingReceipt
		err := claudetape.ReadLifecycleControl(root, 16384, &ack)
		if err == nil {
			digest, err := hex.DecodeString(ack.ArtifactSHA256)
			if err != nil || len(digest) != 32 || !ack.Success || ack.Frames != frames {
				return os.ErrInvalid
			}
			break
		}
		return err
	}
	return daemonRecordingWrite(path, receipt)
}

func TestLifecycleTerminalPublicationWaitsForPrefixAudit(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	path, root := filepath.Join(home, "terminal"), filepath.Join(home, "root")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	finished := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer func() { unblock(); <-finished }()
	receipt := claudetape.LifecycleReceipt{Success: true, Frames: 3, OwnerPID: 101, BrokerPID: 102}
	go func() {
		defer close(finished)
		done <- publishLifecycleTerminalReceipt(path, root, 2, receipt, func() error { close(entered); <-release; _, err := os.Lstat(root); return err })
	}()
	select {
	case <-entered:
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("terminal published before pinned prefix")
		}
	case err := <-done:
		t.Fatal("terminal publication did not await prefix audit", err)
	}
	if err := claudetape.PublishLifecycleControl(root, daemonRecordingReceipt{Success: true, Frames: 2, ArtifactSHA256: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var got claudetape.LifecycleReceipt
	if claudetape.ReadLifecycleControl(path, 16384, &got) != nil || got != receipt {
		t.Fatal("actual terminal receipt changed")
	}
}

func TestLifecycleTerminalPublicationRejectsInvalidPrefix(t *testing.T) {
	for _, variant := range []string{"cancelled", "failed-prefix", "foreign-count", "invalid-digest", "failed-terminal"} {
		t.Run(variant, func(t *testing.T) {
			home := t.TempDir()
			if err := os.Chmod(home, 0700); err != nil {
				t.Fatal(err)
			}
			path, root := filepath.Join(home, "terminal"), filepath.Join(home, "root")
			ack := daemonRecordingReceipt{Success: true, Frames: 2, ArtifactSHA256: strings.Repeat("a", 64)}
			switch variant {
			case "failed-prefix":
				ack.Success = false
			case "foreign-count":
				ack.Frames++
			case "invalid-digest":
				ack.ArtifactSHA256 = "unknown"
			}
			if variant != "cancelled" && variant != "failed-terminal" {
				if err := claudetape.PublishLifecycleControl(root, ack); err != nil {
					t.Fatal(err)
				}
			}
			err := publishLifecycleTerminalReceipt(path, root, 2, claudetape.LifecycleReceipt{Success: variant != "failed-terminal"}, func() error { return os.ErrDeadlineExceeded })
			if variant == "failed-terminal" {
				if err != nil {
					t.Fatal("failed terminal was delayed", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid prefix allowed terminal publication")
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("invalid prefix published terminal")
			}
		})
	}
}
