package sessions

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Pause a settings install before it publishes argv, without holding a runner
// lock. Later suppression installs can proceed unless the pool serializes them.
type handoffInstallRunner struct {
	*gapRunner
	pauseStage       string
	reached, release chan struct{}
	pauseTaken       atomic.Bool
}

func (r *handoffInstallRunner) pause(stage string) {
	if stage == r.pauseStage && r.pauseTaken.CompareAndSwap(false, true) {
		close(r.reached)
		<-r.release
	}
}

func (r *handoffInstallRunner) SetSpawnPermissionMode(mode string) {
	r.pause("posture")
	r.gapRunner.SetSpawnPermissionMode(mode)
}

func (r *handoffInstallRunner) SetSpawnArgs(args []string) {
	r.pause("argv")
	r.gapRunner.SetSpawnArgs(args)
}

func (r *handoffInstallRunner) Restart(args []string) {
	r.pause("argv")
	r.gapRunner.SetSpawnArgs(args)
	r.lifecycleRunner.Restart(args)
}

func rotateDuringHandoffInstall(t *testing.T, pool *Pool, id SessionID, runner *handoffInstallRunner, stage string, update SettingsUpdate) SessionID {
	t.Helper()
	runner.pauseStage = stage
	runner.pauseTaken.Store(false)
	runner.reached, runner.release = make(chan struct{}), make(chan struct{})
	releaseCh := runner.release
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	var workers sync.WaitGroup
	t.Cleanup(func() { release(); workers.Wait() })
	updated := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		updated <- pool.UpdateSettings(id, update)
	}()
	select {
	case <-runner.reached:
	case <-time.After(2 * time.Second):
		t.Fatal("settings install did not reach gate")
	}
	type rotationResult struct {
		id  SessionID
		err error
	}
	rotated := make(chan rotationResult, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		newID, err := pool.RotateForNewSession(id)
		rotated <- rotationResult{newID, err}
	}()
	var result rotationResult
	rotationFinished := false
	// Allow rotation to finish while posture is paused, or wait for the argv
	// publication serialization. Release the gate in either case.
	select {
	case result = <-rotated:
		rotationFinished = true
	case <-time.After(time.Second):
	}
	release()
	select {
	case err := <-updated:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("settings install did not complete")
	}
	if !rotationFinished {
		select {
		case result = <-rotated:
		case <-time.After(2 * time.Second):
			t.Fatal("rotation did not complete")
		}
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	workers.Wait()
	return result.id
}

func TestPool_StaleHandoffConcurrentSettingsInstall(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires directory permission enforcement")
	}
	for _, stage := range []string{"posture", "argv"} {
		for _, model := range []string{"opus", ""} {
			branch := "in_band"
			if model == "" {
				branch = "restart"
			}
			t.Run(stage+"/"+branch, func(t *testing.T) {
				root, spawn := t.TempDir(), t.TempDir()
				reg := conversationWithPrompt(convPromptID, nil)
				pool := helperPoolWithConversations(t, filepath.Join(root, "sessions.json"), t.TempDir(), reg)
				pool.newRunner = func(cfg RunnerConfig) (Runner, error) {
					return &handoffInstallRunner{gapRunner: newGapRunner(cfg)}, nil
				}
				ctx, _ := runPoolInBackground(t, pool)
				if _, err := pool.WriteHandoffNote(convPromptID, noteText); err != nil {
					t.Fatal(err)
				}
				id, err := pool.Mint(convPromptID, spawn)
				if err != nil {
					t.Fatal(err)
				}
				initialModel := "sonnet"
				if err := pool.UpdateSettings(id, SettingsUpdate{Model: &initialModel}); err != nil {
					t.Fatal(err)
				}
				if err := pool.Activate(ctx, id); err != nil {
					t.Fatal(err)
				}
				path := systemPromptArgPath(t, waitArgvRaw(t, spawn))
				before := systemPromptText + "\n" + noteSectionOf(noteText)
				assertComposedFileHolds(t, path, "", before)
				sess, err := pool.Lookup(id)
				if err != nil {
					t.Fatal(err)
				}
				runner := sess.sup.(*handoffInstallRunner)
				if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o700) })
				if err := os.Chmod(path, 0o400); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
				if err := pool.MarkHandoffNoteStale(convPromptID); err != nil {
					t.Fatal(err)
				}
				id = rotateDuringHandoffInstall(t, pool, id, runner, stage, SettingsUpdate{Model: &model})
				assertHandoffNoteHolds(t, root, convPromptID, noteText)
				assertComposedFileHolds(t, path, "", before)
				runner.argMu.Lock()
				unsafe := slices.Contains(runner.args, path)
				runner.argMu.Unlock()
				if unsafe {
					t.Fatal("delayed settings install restored the unqualified older handoff")
				}
				if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.WriteHandoffNote(convPromptID, noteTextAfter); err != nil {
					t.Fatal(err)
				}
				effort := "high"
				rotateDuringHandoffInstall(t, pool, id, runner, stage, SettingsUpdate{Model: &model, Effort: &effort})
				assertComposedFileHolds(t, path, "", systemPromptText+"\n"+noteSectionOf(noteTextAfter))
				runner.argMu.Lock()
				args := slices.Clone(runner.args)
				runner.argMu.Unlock()
				if got := systemPromptArgPath(t, args); got != path {
					t.Fatal("recovery did not restore the prompt argv")
				}
			})
		}
	}
}
