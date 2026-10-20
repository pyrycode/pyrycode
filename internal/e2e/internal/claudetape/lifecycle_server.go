package claudetape

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// LifecycleBrokerServer owns brokers outside the provider's descendant tree.
// Production may reap every native descendant before signalling that provider.
// Only the kernel-identified provider can transfer its seven actual pipe ends
// for a closed, source-bound launch. No fixture supplies a host process target.
type LifecycleBrokerServer struct {
	listener  *net.UnixListener
	launch    func(string, int) (*exec.Cmd, error)
	mu        sync.Mutex
	claimed   map[string]bool
	allowed   map[string]bool
	conns     map[*net.UnixConn]bool
	children  map[*exec.Cmd]bool
	failed    bool
	aborting  bool
	accepting sync.WaitGroup
	handling  sync.WaitGroup
}

func NewLifecycleBrokerServer(socket string, jobs []string, launch func(string, int) (*exec.Cmd, error)) (*LifecycleBrokerServer, error) {
	fail := errors.New("invalid lifecycle broker server")
	if !filepath.IsAbs(socket) || len(jobs) == 0 || launch == nil || checkpointDirectory(filepath.Dir(socket)) != nil {
		return nil, fail
	}
	allowed := map[string]bool{}
	for _, path := range jobs {
		if !filepath.IsAbs(path) || allowed[path] {
			return nil, fail
		}
		allowed[path] = true
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		return nil, fail
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return nil, fail
	}
	if os.Chmod(socket, 0600) != nil {
		l.Close()
		return nil, fail
	}
	s := &LifecycleBrokerServer{listener: l, launch: launch, allowed: allowed, claimed: map[string]bool{}, conns: map[*net.UnixConn]bool{}, children: map[*exec.Cmd]bool{}}
	s.accepting.Add(1)
	go func() {
		defer s.accepting.Done()
		for {
			c, err := l.AcceptUnix()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns[c] = true
			s.handling.Add(1)
			s.mu.Unlock()
			go s.handle(c)
		}
	}()
	return s, nil
}
func (s *LifecycleBrokerServer) handle(c *net.UnixConn) {
	defer s.handling.Done()
	defer func() { c.Close(); s.mu.Lock(); delete(s.conns, c); s.mu.Unlock() }()
	bad := func() { s.mu.Lock(); s.failed = true; s.mu.Unlock() }
	if c.SetReadDeadline(time.Now().Add(10*time.Second)) != nil {
		bad()
		return
	}
	data := make([]byte, 4096)
	control := make([]byte, syscall.CmsgSpace(7*4))
	n, oobn, flags, _, err := c.ReadMsgUnix(data, control)
	messages, parseErr := syscall.ParseSocketControlMessage(control[:oobn])
	var fds []int
	for _, m := range messages {
		rights, e := syscall.ParseUnixRights(&m)
		if e != nil {
			parseErr = e
		}
		fds = append(fds, rights...)
	}
	var files []*os.File
	for _, fd := range fds {
		syscall.CloseOnExec(fd)
		files = append(files, os.NewFile(uintptr(fd), "owned-lifecycle-pipe"))
	}
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	if err != nil || parseErr != nil || flags&(syscall.MSG_CTRUNC|syscall.MSG_TRUNC) != 0 || len(messages) != 1 || len(files) != 7 || n == 0 || n == len(data) {
		bad()
		return
	}
	var extra [1]byte
	if n, err := c.Read(extra[:]); n != 0 || err != io.EOF {
		bad()
		return
	}
	peer, err := lifecyclePeerPID(c)
	if err != nil || peer <= 1 {
		bad()
		return
	}
	job := string(data[:n])
	s.mu.Lock()
	valid := s.allowed[job] && !s.claimed[job]
	if valid {
		s.claimed[job] = true
	}
	s.mu.Unlock()
	if !valid {
		bad()
		return
	}
	var identities []os.FileInfo
	for i, f := range files {
		info, e := f.Stat()
		if e != nil || info.Mode()&os.ModeNamedPipe == 0 {
			bad()
			return
		}
		for _, old := range identities {
			if os.SameFile(old, info) {
				bad()
				return
			}
		}
		identities = append(identities, info)
		flags, e := unix.FcntlInt(f.Fd(), unix.F_GETFL, 0)
		want := unix.O_WRONLY
		if i == 0 || i == 4 || i == 5 {
			want = unix.O_RDONLY
		}
		if e != nil || flags&unix.O_ACCMODE != want {
			bad()
			return
		}
	}
	cmd, err := s.launch(job, peer)
	if err != nil || cmd == nil || len(cmd.ExtraFiles) != 0 || cmd.Process != nil {
		bad()
		return
	}
	cmd.ExtraFiles = files
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	s.mu.Lock()
	if s.aborting {
		s.failed = true
		s.mu.Unlock()
		return
	}
	if cmd.Start() != nil {
		s.failed = true
		s.mu.Unlock()
		return
	}
	s.children[cmd] = true
	s.mu.Unlock()
	// Close the receiving parent's copies immediately; otherwise they themselves
	// keep EOF from reaching either provider or original daemon.
	for _, f := range files {
		f.Close()
	}
	files = nil
	err = cmd.Wait()
	s.mu.Lock()
	delete(s.children, cmd)
	if err != nil {
		s.failed = true
	}
	s.mu.Unlock()
	_ = c.SetWriteDeadline(time.Now().Add(time.Second))
	status := byte(1)
	if err != nil {
		status = 0
	}
	_, _ = c.Write([]byte{status}) // Capture exec has already closed this connection.
}

// Join stops accepting new transfers and waits for every owned broker. A timeout
// kills only the actual exec.Cmd children this server started, never native PIDs.
func (s *LifecycleBrokerServer) Join(budget time.Duration) error {
	_ = s.listener.Close()
	s.accepting.Wait()
	done := make(chan struct{})
	go func() { s.handling.Wait(); close(done) }()
	select {
	case <-done:
		s.mu.Lock()
		failed := s.failed
		s.mu.Unlock()
		if failed {
			return errors.New("lifecycle broker server rejected a transfer or child")
		}
		return nil
	case <-time.After(budget):
		s.mu.Lock()
		s.aborting = true
		for c := range s.conns {
			c.Close()
		}
		for cmd := range s.children {
			_ = cmd.Process.Kill()
		}
		s.failed = true
		s.mu.Unlock()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		return errors.New("lifecycle broker join timed out")
	}
}
