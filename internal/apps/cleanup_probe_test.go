package apps

import (
	"errors"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

func TestOwnerCleanupProbeRecovery(t *testing.T) {
	for _, tc := range []struct {
		name               string
		term, probe, final error
		wantError          bool
	}{
		{"transient permission", nil, syscall.EPERM, syscall.ESRCH, false},
		{"persistent permission", nil, syscall.EPERM, syscall.EPERM, true},
		{"failed termination", syscall.EPERM, nil, syscall.ESRCH, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				probes := 0
				o := Owner{grace: time.Second, kill: func(pid int, sig syscall.Signal) error {
					if pid != -123 {
						t.Fatalf("unexpected process group %d", pid)
					}
					switch sig {
					case syscall.SIGTERM:
						return tc.term
					case 0:
						probes++
						if probes == 1 {
							return tc.probe
						}
						return tc.final
					default:
						return nil
					}
				}}
				err := o.cleanup(123)
				if tc.wantError && !errors.Is(err, syscall.EPERM) {
					t.Fatalf("lost cleanup failure: %v", err)
				}
				if !tc.wantError && err != nil {
					t.Fatalf("reported failure after the group disappeared: %v", err)
				}
				if probes < 2 {
					t.Fatal("did not wait for a conclusive process-group result")
				}
			})
		})
	}
}
