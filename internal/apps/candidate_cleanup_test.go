package apps

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"testing/synctest"
)

func TestCandidateCleanupProbeRecovery(t *testing.T) {
	for _, name := range []string{"transient", "until kill", "after kill", "persistent", "denied term", "denied kill", "live"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				probes, forcedProbes, forced := 0, 0, false
				kill := func(pid int, sig syscall.Signal) error {
					if pid != -123 {
						t.Fatalf("wrong group %d", pid)
					}
					forced = forced || sig == syscall.SIGKILL
					if name == "denied term" && sig == syscall.SIGTERM || name == "denied kill" && sig == syscall.SIGKILL {
						return syscall.EPERM
					}
					return nil
				}
				probe := func(pid int) (bool, error) {
					if pid != 123 {
						t.Fatalf("wrong probe group %d", pid)
					}
					probes++
					if forced {
						forcedProbes++
					}
					switch name {
					case "persistent":
						return false, syscall.EPERM
					case "live", "denied kill":
						return true, nil
					case "until kill":
						if !forced {
							return false, syscall.EPERM
						}
					case "after kill":
						if !forced {
							return true, nil
						}
						if forcedProbes < 3 {
							return false, syscall.EPERM
						}
					case "transient":
						if probes == 1 {
							return false, syscall.EPERM
						}
					}
					return false, nil
				}
				err := candidateCleanupWithProbe(123, kill, probe)
				switch name {
				case "persistent", "denied term", "denied kill":
					if !errors.Is(err, syscall.EPERM) {
						t.Fatalf("lost uncertainty/denied signal: %v", err)
					}
				case "live":
					if !errors.Is(err, ErrUnavailable) || !forced {
						t.Fatalf("accepted live group: %v", err)
					}
				default:
					if err != nil || probes < 2 {
						t.Fatalf("did not confirm retirement after uncertainty: probes=%d forced=%v error=%v", probes, forced, err)
					}
				}
			})
		})
	}
}

func TestCandidateBuildProbeRecovery(t *testing.T) {
	r, tools, _, _ := testCandidate(t)
	stage, removeStage, err := candidateStaging(context.Background(), filepath.Join(r.root, testApp, "source"))
	backupOK(t, err)
	defer removeStage()
	env, removeEnv, err := candidateEnvironment(stage, tools.Node)
	backupOK(t, err)
	defer removeEnv()
	probes := 0
	cleanup := func(pid int) error {
		return candidateCleanupWithProbe(pid, syscall.Kill, func(pid int) (bool, error) {
			probes++
			if probes == 1 {
				return false, syscall.EPERM
			}
			return candidateGroupLive(pid)
		})
	}
	_, err = candidateCommandWithCleanup(context.Background(), stage, env, tools.Node, []string{tools.NPM, "run", "build"}, false, cleanup)
	if err != nil || probes < 2 {
		t.Fatalf("successful build rejected on transient inspection: probes=%d error=%v", probes, err)
	}
	_, err = ValidateArtifacts(context.Background(), stage, testHost, testApp)
	backupOK(t, err)
}
