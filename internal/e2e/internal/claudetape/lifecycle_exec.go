package claudetape

import (
	"errors"
	"io"
	"net"
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
	return execLifecycleChild(brokerArgs, brokerEnv, provider, args, capture, "", "", nil)
}

// ExecLifecycleChildViaBroker delegates the pipe capability to the outer test
// parent, so production's native descendant reaper cannot kill the recorder.
func ExecLifecycleChildViaBroker(socket, job, provider string, args []string, capture bool) error {
	return execLifecycleChild(nil, nil, provider, args, capture, socket, job, nil)
}

// ExecLifecycleChildViaBrokerReady extends only the private startup pipe. The
// setup callback must validate the complete current-source capability before
// returning; capture still execs the provider at its original owned PID.
func ExecLifecycleChildViaBrokerReady(socket, job, provider string, args []string, capture bool, setup func(io.Reader) error) error {
	return execLifecycleChild(nil, nil, provider, args, capture, socket, job, setup)
}
func execLifecycleChild(brokerArgs, brokerEnv []string, provider string, args []string, capture bool, socket, job string, setup func(io.Reader) error) error {
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
	var cmd *exec.Cmd
	var connection *net.UnixConn
	if socket == "" {
		cmd = exec.Command(exe, brokerArgs...)
		cmd.Env = brokerEnv
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.ExtraFiles = []*os.File{os.Stdin, os.Stdout, os.Stderr, inW, outR, errR, readyW}
		if cmd.Start() != nil {
			return fail
		}
	} else {
		if !filepath.IsAbs(socket) || !filepath.IsAbs(job) || len(job) >= 4096 {
			return fail
		}
		connection, err = net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
		if err != nil {
			return fail
		}
		defer connection.Close()
		_ = connection.SetWriteDeadline(time.Now().Add(10 * time.Second))
		files := []*os.File{os.Stdin, os.Stdout, os.Stderr, inW, outR, errR, readyW}
		var fds []int
		for _, f := range files {
			fds = append(fds, int(f.Fd()))
		}
		n, oob, err := connection.WriteMsgUnix([]byte(job), syscall.UnixRights(fds...), nil)
		if err != nil || n != len(job) || oob != len(syscall.UnixRights(fds...)) {
			return fail
		}
		if connection.CloseWrite() != nil {
			return fail
		}
	}
	cleanupBroker := true
	var waitDone <-chan error
	defer func() {
		if cleanupBroker && cmd != nil {
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
	ready := make(chan error, 1)
	go func() { ready <- receiveLifecycleReady(readyR, setup) }()
	select {
	case err := <-ready:
		if err != nil {
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
	go func() {
		if cmd != nil {
			done <- cmd.Wait()
			return
		}
		_ = connection.SetReadDeadline(time.Now().Add(10 * time.Second))
		var status [1]byte
		_, e := io.ReadFull(connection, status[:])
		if e != nil || status[0] != 1 {
			done <- fail
		} else {
			done <- nil
		}
	}()
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

func receiveLifecycleReady(reader *os.File, setup func(io.Reader) error) error {
	if reader.SetReadDeadline(time.Now().Add(10*time.Second)) != nil {
		return errors.New("lifecycle readiness deadline unavailable")
	}
	var marker [1]byte
	if _, err := io.ReadFull(reader, marker[:]); err != nil || marker[0] != 1 {
		return errors.New("lifecycle readiness failed")
	}
	if setup != nil {
		return setup(reader)
	}
	return nil
}
