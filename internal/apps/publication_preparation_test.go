package apps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPublishPreparationRefusal(t *testing.T) {
	for _, phase := range []string{"verification", "snapshot", "backup verification"} {
		t.Run(phase, func(t *testing.T) {
			r, o, tools, _, _ := publishFixture(t, healthReply{Body: healthyBody()}, "ok")
			data := filepath.Join(r.root, testApp, "data")
			testArtifactWrite(t, data, "sentinel", "before")
			before := testTreeBytes(t, data)
			var injectionErr error
			if phase == "verification" {
				unsubscribe := r.Subscribe(func(c Change) {
					if c.Record == nil || c.Record.PendingRelease != "" || len(c.Record.ReleaseHistory) != 1 {
						return
					}
					// Preparation recorded exact checked bytes; tamper only afterward.
					entry := filepath.Join(c.Record.ReleaseHistory[0].BuildRoot, "dist/service/main.js")
					if injectionErr = os.Chmod(entry, 0600); injectionErr == nil {
						injectionErr = os.WriteFile(entry, []byte("SECRET corrupt checked bytes"), 0600)
					}
				})
				defer unsubscribe()
			}
			if phase == "snapshot" {
				o.backupOps.hook = func(step string) error {
					if step == "copy" {
						return errors.New("SECRET snapshot " + r.root)
					}
					return nil
				}
			}
			if phase == "backup verification" {
				o.backupOps.hook = func(step string) error {
					if step != "sealed" {
						return nil
					}
					// Corrupt the completed snapshot after its own verification.
					parent := filepath.Join(r.root, testApp, "backups")
					entries, e := os.ReadDir(parent)
					if e != nil || len(entries) != 1 {
						injectionErr = errors.Join(e, ErrInvalidStorage)
						return injectionErr
					}
					injectionErr = os.WriteFile(filepath.Join(parent, entries[0].Name(), "data/sentinel"), []byte("SECRET corrupted backup"), 0600)
					return injectionErr
				}
			}
			result, err := o.Publish(context.Background(), testHost, testApp, tools)
			if injectionErr != nil {
				t.Fatal("injection failed", injectionErr)
			}
			var failure *PublishFailure
			wantPhase := phase
			if phase == "backup verification" {
				wantPhase = "snapshot"
			}
			if !errors.As(err, &failure) || failure.Phase != wantPhase || result.ActivationCommitted || result.Healthy || result.RecoveryPending || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), r.root) {
				t.Fatal("preparation refusal lost", result, err)
			}
			v := publishDisk(t, r).Records[0]
			if v.Title != "App" || v.ActiveRelease != "" || v.PendingRelease != "" || v.State != "failed" || v.Desired != "available" || v.PublishJournal != nil || v.LastError == nil || strings.Contains(v.LastError.Message, "SECRET") || strings.Contains(v.LastError.Message, r.root) {
				t.Fatal("refusal changed committed release/evidence", v)
			}
			if !reflect.DeepEqual(before, testTreeBytes(t, data)) {
				t.Fatal("refusal prepared data or started writer")
			}
			if g := recoveryGeneration(o, testApp); g != nil {
				t.Fatal("candidate service admitted", g)
			}
			if _, e := o.Lookup(testHost, testApp); e == nil {
				t.Fatal("refused candidate exposed")
			}
		})
	}
}
