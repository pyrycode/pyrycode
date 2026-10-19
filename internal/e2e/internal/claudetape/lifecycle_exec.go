package claudetape

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// ExecLifecycleChild leaves the provider at the daemon-owned PID. A separate
// process owns the recording pipes, so killing that provider cannot erase its
// final recorded prefix. The broker gets original stdin/stdout/stderr at fds
// 3/4/5, provider stdin/stdout/stderr at 6/7/8, and a ready writer at fd 9.
// The caller supplies a closed, source-bound broker invocation and environment.
func ExecLifecycleChild(brokerArgs, brokerEnv []string, provider string, args []string, capture bool) error {
	fail := errors.New("lifecycle exec setup failed")
	if capture {
		info, err := os.Stat(provider)
		if !filepath.IsAbs(provider) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return fail
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return fail
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return fail
	}
	defer inR.Close()
	defer inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		return fail
	}
	defer outR.Close()
	defer outW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		return fail
	}
	defer errR.Close()
	defer errW.Close()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return fail
	}
	defer readyR.Close()
	defer readyW.Close()
	cmd := exec.Command(exe, brokerArgs...)
	cmd.Env = brokerEnv
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.ExtraFiles = []*os.File{os.Stdin, os.Stdout, os.Stderr, inW, outR, errR, readyW}
	if cmd.Start() != nil {
		return fail
	}
	cleanupBroker := true
	var waitDone <-chan error
	defer func() {
		if cleanupBroker {
			_ = cmd.Process.Kill()
			if waitDone != nil {
				<-waitDone
			} else {
				_ = cmd.Wait()
			}
		}
	}()
	_ = inW.Close()
	_ = outR.Close()
	_ = errR.Close()
	_ = readyW.Close()
	ready := make(chan bool, 1)
	go func() { var b [1]byte; n, e := readyR.Read(b[:]); ready <- n == 1 && e == nil && b[0] == 1 }()
	select {
	case ok := <-ready:
		if !ok {
			return fail
		}
	case <-time.After(10 * time.Second):
		return fail
	}
	for _, d := range []struct {
		file   *os.File
		target int
	}{{inR, 0}, {outW, 1}, {errW, 2}} {
		if syscall.Dup2(int(d.file.Fd()), d.target) != nil {
			return fail
		}
	}
	_ = inR.Close()
	_ = outW.Close()
	_ = errW.Close()
	if capture {
		if provider == "" {
			return fail
		}
		// Exec, rather than starting a grandchild, preserves the PID observed and
		// signalled by the unchanged original control-plane test.
		if syscall.Exec(provider, append([]string{provider}, args...), os.Environ()) != nil {
			return fail
		}
		return fail
	}
	// Replay's owned process has the same lifetime boundary. Its broker emits
	// saved stdout; the process itself holds the provider pipe open until the
	// original daemon kills it or closes stdin. No provider executes here.
	_, err = io.Copy(io.Discard, os.Stdin)
	_ = os.Stdout.Close()
	_ = os.Stderr.Close()
	done := make(chan error, 1)
	waitDone = done
	go func() { done <- cmd.Wait() }()
	select {
	case brokerErr := <-done:
		cleanupBroker = false
		if err != nil {
			return err
		}
		return brokerErr
	case <-time.After(10 * time.Second):
		return fail
	}
}
