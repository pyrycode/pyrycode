package claudetape

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func checkpointFilesRig(t *testing.T) (string, ClientCheckpoint) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "client-actions")
	if err := NewCheckpointDirectory(dir); err != nil {
		t.Fatal(err)
	}
	return dir, ClientCheckpoint{Type: ClientCheckpointDirection, Sequence: 1, Action: "interrupt_phase", ConversationID: "conv", Phase: "thinking"}
}
func waitCheckpoint(t *testing.T, dir string) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, exists, err := ReadCheckpoint(dir, 1)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			return raw
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("checkpoint not published")
	return nil
}
func completedCheckpoint(t *testing.T, dir string, c ClientCheckpoint) []byte {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- PublishCheckpoint(dir, c, 2*time.Second) }()
	raw := waitCheckpoint(t, dir)
	if err := AcknowledgeCheckpoint(dir, raw); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestCheckpointFilePublicationAndSeal(t *testing.T) {
	dir, c := checkpointFilesRig(t)
	raw := completedCheckpoint(t, dir, c)
	sealed, err := SealCheckpointDirectory(dir, [][]byte{raw})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("old publication path survived seal")
	}
	entries, err := os.ReadDir(sealed)
	if err != nil || len(entries) != 2 {
		t.Fatal("frozen checkpoint inventory changed")
	}
	if PublishCheckpoint(dir, c, time.Millisecond) == nil {
		t.Fatal("post-seal publication recreated old directory")
	}
	if _, _, err = ReadCheckpoint(dir, 1); err == nil {
		t.Fatal("post-seal read did not fail closed")
	}
	if AcknowledgeCheckpoint(dir, raw) == nil {
		t.Fatal("post-seal acknowledgement recreated old directory")
	}
	if _, err = SealCheckpointDirectory(dir, [][]byte{raw}); err == nil {
		t.Fatal("second seal accepted")
	}
}
func TestCheckpointFilesRejectIncompleteOrExtraEvidence(t *testing.T) {
	for _, name := range []string{"duplicate", "unknown", "extra ready", "missing ack", "changed ready", "changed ack", "open pending"} {
		t.Run(name, func(t *testing.T) {
			dir, c := checkpointFilesRig(t)
			raw, _ := json.Marshal(c)
			if name == "missing ack" {
				if err := publishClosedFile(dir, checkpointName(1, "ready"), raw); err != nil {
					t.Fatal(err)
				}
			} else {
				raw = completedCheckpoint(t, dir, c)
			}
			var open *os.File
			switch name {
			case "duplicate":
				if PublishCheckpoint(dir, c, time.Millisecond) == nil {
					t.Fatal("duplicate publication accepted")
				}
			case "unknown":
				if err := os.WriteFile(filepath.Join(dir, "unknown"), []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
			case "extra ready":
				second := c
				second.Sequence = 2
				data, _ := json.Marshal(second)
				if err := publishClosedFile(dir, checkpointName(2, "ready"), data); err != nil {
					t.Fatal(err)
				}
			case "changed ready":
				if err := os.WriteFile(filepath.Join(dir, checkpointName(1, "ready")), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed ack":
				if err := os.WriteFile(filepath.Join(dir, checkpointName(1, "ack")), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "open pending":
				var err error
				open, err = os.OpenFile(filepath.Join(dir, checkpointName(2, "ready")+".pending"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = open.Close() })
				if _, err = open.Write([]byte(`{"type":`)); err != nil {
					t.Fatal(err)
				}
			}
			sealed, err := SealCheckpointDirectory(dir, [][]byte{raw})
			if err == nil {
				t.Fatal("incomplete, changed or extra evidence passed seal")
			}
			if sealed == "" {
				t.Fatal("seal did not atomically revoke the publication path")
			}
			if open != nil {
				if _, err = open.Write([]byte(`"client_checkpoint"}`)); err != nil {
					t.Fatal(err)
				}
				if err = open.Close(); err != nil {
					t.Fatal(err)
				}
				if err = os.Link(filepath.Join(dir, checkpointName(2, "ready")+".pending"), filepath.Join(dir, checkpointName(2, "ready"))); err == nil {
					t.Fatal("late ready publication crossed directory seal")
				}
			}
		})
	}
}
func TestCheckpointFileReaderAndAcknowledgementRejections(t *testing.T) {
	for _, name := range []string{"partial pending", "partial ready", "symlink", "mode", "stale sequence", "noncanonical", "wrong ack", "missing ack timeout"} {
		t.Run(name, func(t *testing.T) {
			dir, c := checkpointFilesRig(t)
			raw, _ := json.Marshal(c)
			ready := filepath.Join(dir, checkpointName(1, "ready"))
			switch name {
			case "partial pending":
				if err := os.WriteFile(ready+".pending", []byte(`{`), 0600); err != nil {
					t.Fatal(err)
				}
				if _, exists, err := ReadCheckpoint(dir, 1); err != nil || exists {
					t.Fatal("partial pending became observable action")
				}
				return
			case "wrong ack", "missing ack timeout":
				done := make(chan error, 1)
				go func() { done <- PublishCheckpoint(dir, c, 100*time.Millisecond) }()
				_ = waitCheckpoint(t, dir)
				if name == "wrong ack" {
					if err := publishClosedFile(dir, checkpointName(1, "ack"), []byte(`{"type":"client_checkpoint_ack","sequence":1,"sha256":"wrong"}`)); err != nil {
						t.Fatal(err)
					}
				}
				if err := <-done; err == nil {
					t.Fatal("publication accepted wrong or absent ack")
				}
				return
			case "partial ready":
				raw = raw[:len(raw)-1]
			case "mode":
				if err := os.WriteFile(ready, raw, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(ready, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, ready); err != nil {
					t.Fatal(err)
				}
			case "stale sequence":
				c.Sequence = 2
				raw, _ = json.Marshal(c)
			case "noncanonical":
				raw = append(raw, []byte("\n")...)
			}
			if name != "mode" && name != "symlink" {
				if err := os.WriteFile(ready, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := ReadCheckpoint(dir, 1); err == nil {
				t.Fatal("invalid ready evidence accepted")
			}
		})
	}
	dir, c := checkpointFilesRig(t)
	raw := completedCheckpoint(t, dir, c)
	again, _ := os.ReadFile(filepath.Join(dir, checkpointName(1, "ready")))
	if !bytes.Equal(raw, again) {
		t.Fatal("ready content changed during ack")
	}
}
