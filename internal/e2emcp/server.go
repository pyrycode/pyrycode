package e2emcp

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// ServerProof is private runtime evidence, not fixture process authority. Native
// proxy status is deliberately absent: its actual parent is the SDK, whereas
// this owner can Wait only its own actual same-binary MCP server.
type ServerProof struct {
	Kind                                      string
	NativeOwnerPID, NativeProxyPID, ServerPID int
	InputFrames, OutputFrames                 int
	InputEOF, OutputEOF, StderrEOF            bool
	ExitCode                                  int
	Stderr                                    string
}

type ProxyServer struct {
	binding             LaunchBinding
	coordinator         *Coordinator
	validateStderr      func([]byte) error
	listener            *net.UnixListener
	root, token         string
	ctx                 context.Context
	cancel              context.CancelFunc
	mu                  sync.Mutex
	claimed             map[string]bool
	proofs              []ServerProof
	failed              bool
	conns               map[*net.UnixConn]bool
	accepting, handling sync.WaitGroup
}

// NewProxyServer has no production caller. It accepts only actual current
// native children of the supplied role owner. Real server work stays in a
// separately owned process; replay must not claim native parent/group semantics.
func NewProxyServer(ctx context.Context, b LaunchBinding, c *Coordinator, validateStderr func([]byte) error) (*ProxyServer, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, protocolError()
	}
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.After(time.Now()) || time.Until(deadline) > 20*time.Minute || b.validate() != nil || c == nil || validateStderr == nil {
		return nil, protocolError()
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("/tmp", "pyry-mcp-")
	if err != nil {
		return nil, protocolError()
	}
	socket := filepath.Join(root, "broker.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		os.RemoveAll(root)
		return nil, protocolError()
	}
	if os.Chmod(socket, 0600) != nil {
		listener.Close()
		os.RemoveAll(root)
		return nil, protocolError()
	}
	owned, cancel := context.WithCancel(ctx)
	s := &ProxyServer{binding: b, coordinator: c, validateStderr: validateStderr, listener: listener, root: root, token: token, ctx: owned, cancel: cancel, claimed: map[string]bool{}, conns: map[*net.UnixConn]bool{}}
	s.accepting.Add(1)
	go func() {
		defer s.accepting.Done()
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns[conn] = true
			s.handling.Add(1)
			s.mu.Unlock()
			go s.handle(conn)
		}
	}()
	return s, nil
}
func (s *ProxyServer) Capability() (socket, token string) { return s.listener.Addr().String(), s.token }
func (s *ProxyServer) bad() {
	// A forwarding callback may hold the coordinator mutex while blocked in an
	// actual pipe write. Cancel/close owned I/O before waiting on that mutex.
	s.cancel()
	s.listener.Close()
	s.mu.Lock()
	s.failed = true
	for conn := range s.conns {
		conn.Close()
	}
	s.mu.Unlock()
	s.coordinator.Abort()
}
func (s *ProxyServer) handle(conn *net.UnixConn) {
	defer s.handling.Done()
	defer func() { conn.Close(); s.mu.Lock(); delete(s.conns, conn); s.mu.Unlock() }()
	launch, files, err := receiveLaunch(conn, s.binding, s.token)
	if err != nil {
		s.bad()
		return
	}
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	kind, err := s.binding.serverArgs(launch.Args)
	if err != nil {
		s.bad()
		return
	}
	env, err := s.binding.serverEnvironment(launch.Env)
	if err != nil {
		s.bad()
		return
	}
	s.mu.Lock()
	duplicate := s.claimed[kind]
	if !duplicate {
		s.claimed[kind] = true
	}
	s.mu.Unlock()
	if duplicate || s.coordinator.Register(kind) != nil {
		s.bad()
		return
	}
	proof, err := runOwnedServer(s.ctx, s.binding, kind, env, files, s.coordinator, s.validateStderr)
	proof.NativeProxyPID = launch.peerPID
	if err != nil {
		s.bad()
		return
	}
	s.mu.Lock()
	s.proofs = append(s.proofs, proof)
	s.mu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	if writeAll(conn, []byte{1}) != nil {
		// The native proxy can be killed by its genuine SDK/daemon owner during
		// cleanup. Its status is not ours to invent; real pipe EOF and server Wait
		// above remain required. The outer lifecycle owner separately joins native.
		return
	}
}

// Join stops new launches and joins actual authenticated servers. On deadline it
// cancels only exec.Cmd children owned by this server and closes its borrowed
// pipes/connections. It never signals a fixture ID or a native process group.
func (s *ProxyServer) Join(budget time.Duration) ([]ServerProof, error) {
	s.listener.Close()
	s.accepting.Wait()
	done := make(chan struct{})
	go func() { s.handling.Wait(); close(done) }()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		s.bad()
		s.cancel()
		s.mu.Lock()
		for conn := range s.conns {
			conn.Close()
		}
		s.mu.Unlock()
		<-done
	}
	cancelled := s.ctx.Err() != nil
	s.cancel()
	defer os.RemoveAll(s.root)
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancelled || s.failed || len(s.proofs) != len(s.claimed) {
		return nil, protocolError()
	}
	return append([]ServerProof(nil), s.proofs...), nil
}

func runOwnedServer(ctx context.Context, b LaunchBinding, kind string, env []string, files [3]*os.File, c *Coordinator, validateStderr func([]byte) error) (ServerProof, error) {
	proof := ServerProof{Kind: kind, NativeOwnerPID: b.NativeOwnerPID, ExitCode: -1}
	if ctx == nil || ctx.Err() != nil || b.validate() != nil {
		return proof, protocolError()
	}
	if _, err := b.serverArgs([]string{kind, "-pyry-socket", b.Socket}); err != nil {
		return proof, protocolError()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	inR, inW, err := os.Pipe()
	if err != nil {
		return proof, protocolError()
	}
	defer inR.Close()
	defer inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		return proof, protocolError()
	}
	defer outR.Close()
	defer outW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		return proof, protocolError()
	}
	defer errR.Close()
	defer errW.Close()
	cmd := exec.CommandContext(ctx, b.Binary, kind, "-pyry-socket", b.Socket)
	cmd.Dir = b.WorkDir
	cmd.Env = env
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = errW
	if cmd.Start() != nil {
		return proof, protocolError()
	}
	proof.ServerPID = cmd.Process.Pid
	inR.Close()
	outW.Close()
	errW.Close()
	// Files received after SetNonblock are pollable; cancellation can interrupt a
	// quiet native pipe without a blocked Close or leaked reader goroutine.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			for _, f := range files {
				f.Close()
			}
			inW.Close()
			outR.Close()
			errR.Close()
		case <-stop:
		}
	}()
	type result struct {
		kind   string
		count  int
		eof    bool
		stderr string
		err    error
	}
	done := make(chan result, 4)
	inputEOF := make(chan struct{})
	pump := func(name string, reader io.Reader, direction Direction, writer io.Writer) {
		count, eof, err := forwardLines(reader, func(raw []byte) error {
			return c.ForwardRPC(kind, direction, raw, func(raw []byte) error { return WriteLine(writer, raw) })
		})
		if direction == ClientToServer {
			if err == nil && eof {
				close(inputEOF)
			}
			inW.Close()
		}
		done <- result{kind: name, count: count, eof: eof, err: err}
	}
	go pump("input", files[0], ClientToServer, inW)
	go pump("output", outR, ServerToClient, files[1])
	go func() {
		var body bytes.Buffer
		err := copyStderr(&body, files[2], errR)
		if err == nil {
			err = validateStderr(body.Bytes())
		}
		done <- result{kind: "stderr", eof: err == nil, stderr: body.String(), err: err}
	}()
	go func() {
		err := cmd.Wait()
		// A server that exits with native stdin still open did not observe genuine
		// input EOF. Interrupt that remaining read, so the missing EOF fails promptly.
		files[0].Close()
		select {
		case <-inputEOF:
		default:
			// Even exit zero is premature while client input has not reached
			// genuine EOF. Wake any scheduler waiting for an absent reply.
			if err == nil {
				err = protocolError()
			}
			cancel()
			c.Abort()
		}
		done <- result{kind: "wait", err: err}
	}()
	failed := false
	for i := 0; i < 4; i++ {
		v := <-done
		if v.err != nil {
			failed = true
			cancel()
			c.Abort()
		}
		switch v.kind {
		case "input":
			proof.InputFrames = v.count
			proof.InputEOF = v.eof
		case "output":
			proof.OutputFrames = v.count
			proof.OutputEOF = v.eof
		case "stderr":
			proof.StderrEOF = v.eof
			proof.Stderr = v.stderr
		case "wait":
			if v.err == nil {
				proof.ExitCode = 0
			} else {
				if cmd.ProcessState != nil {
					proof.ExitCode = cmd.ProcessState.ExitCode()
				}
			}
			if !proof.InputEOF && v.err != nil {
				cancel()
			}
		}
	}
	if ctx.Err() != nil || failed || proof.ExitCode != 0 || !proof.StderrEOF || c.CloseRPC(kind, proof.InputEOF, proof.OutputEOF, nil) != nil {
		return proof, protocolError()
	}
	return proof, nil
}
func forwardLines(reader io.Reader, forward func([]byte) error) (count int, eof bool, err error) {
	r := bufio.NewReaderSize(reader, 64<<10)
	var pending []byte
	for {
		part, e := r.ReadSlice('\n')
		if len(pending)+len(part) > MaxFrameBytes+1 {
			return count, false, protocolError()
		}
		pending = append(pending, part...)
		if e == bufio.ErrBufferFull {
			continue
		}
		if e == io.EOF {
			if len(pending) != 0 {
				return count, false, protocolError()
			}
			return count, true, nil
		}
		if e != nil || len(pending) == 0 || pending[len(pending)-1] != '\n' {
			return count, false, protocolError()
		}
		if err := forward(pending[:len(pending)-1]); err != nil {
			return count, false, err
		}
		count++
		pending = nil
	}
}
func copyStderr(retained *bytes.Buffer, destination io.Writer, source io.Reader) error {
	var block [4096]byte
	for {
		n, err := source.Read(block[:])
		if n > 0 {
			if retained.Len()+n > MaxFrameBytes {
				return protocolError()
			}
			retained.Write(block[:n])
			if writeAll(destination, block[:n]) != nil {
				return protocolError()
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return protocolError()
		}
	}
}
