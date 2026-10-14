package apps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestPublishMigrationRetirementUncertain(t *testing.T) {
	r, o, tools, c, _ := publishFixture(t, healthReply{Body: healthyBody()}, "fork")
	o.kill = func(pid int, sig syscall.Signal) error {
		if pid < 0 && sig != 0 {
			return syscall.EPERM
		}
		return syscall.Kill(pid, sig)
	}
	// Keep the denied group's real descendant alive through the evidence checks.
	defer func() {
		b, e := os.ReadFile(filepath.Join(r.root, testApp, "data/descendant"))
		if e == nil {
			pid, _ := strconv.Atoi(string(b))
			if pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				waitGone(t, pid)
			}
		}
	}()
	result, err := o.Publish(context.Background(), testHost, testApp, tools)
	var failure *PublishFailure
	if !errors.As(err, &failure) || failure.Phase != "migration" || !result.RecoveryPending || result.ActivationCommitted || result.Healthy {
		t.Fatalf("retirement uncertainty lost: %+v, %v", result, err)
	}
	if strings.Contains(err.Error(), r.root) || strings.Contains(err.Error(), "permission") {
		t.Fatal("unsafe feedback", err)
	}
	v := publishDisk(t, r).Records[0]
	if v.ActiveRelease != "" || v.State != "failed" || v.LastError == nil || v.LastError.Code != "app.io_failed" || v.PublishJournal == nil || v.PublishJournal.ActivationCommitted {
		t.Fatal("recovery evidence lost", v)
	}
	if e := VerifyBackup(context.Background(), r.root, testHost, testApp, v.PublishJournal.VerifiedBackup, tools.Node); e != nil {
		t.Fatal(e)
	}
	pid, e := strconv.Atoi(string(testBytes(t, filepath.Join(r.root, testApp, "data/descendant"))))
	if e != nil || pid < 1 || syscall.Kill(pid, 0) != nil {
		t.Fatal("regression did not retain a live migration writer", pid, e)
	}
	if _, e := os.Stat(filepath.Join(r.root, testApp, "data/migration")); e != nil {
		t.Fatal("data restored beneath live writer", e)
	}
	if _, e := os.Stat(filepath.Join(r.root, testApp, "data/ready")); !os.IsNotExist(e) {
		t.Fatal("service started despite cleanup uncertainty", e)
	}
	if _, e := o.Lookup(testHost, testApp); e == nil {
		t.Fatal("uncertain candidate exposed")
	}
	log := string(testBytes(t, c.Log))
	if _, e := o.Publish(context.Background(), testHost, testApp, tools); e == nil || string(testBytes(t, c.Log)) != log {
		t.Fatal("retry admitted through unresolved retirement", e)
	}
	if removed, e := r.Remove(testApp); removed || !errors.Is(e, ErrConflict) {
		t.Fatal("removal discarded live writer evidence", removed, e)
	}
	s := publishDisk(t, r)
	if len(s.Tombstones) != 0 || len(s.Records) != 1 || s.Records[0].PublishJournal == nil || s.Records[0].Desired != "stopped" {
		t.Fatal("removal lost stopped registration/evidence", s)
	}
}
