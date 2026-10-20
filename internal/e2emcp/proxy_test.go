package e2emcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func actualUnitBinding(t *testing.T) LaunchBinding {
	t.Helper()
	b := testLaunch(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	b.Binary = exe
	b.BinarySHA256 = hex.EncodeToString(sum[:])
	return b
}
func TestMCPProxyRejectsInvalidUnixBytesBeforeJSONEncoding(t *testing.T) {
	for _, which := range []string{"args", "env"} {
		args := []string{"mcp-files", "-pyry-socket", "/tmp/controlled.sock"}
		env := []string{"CONTROLLED=literal"}
		if which == "args" {
			args[0] = string([]byte{'m', 0xff})
		} else {
			env[0] = string([]byte{'E', '=', 0xff})
		}
		if raw, err := encodeProxyLaunch(strings.Repeat("a", 64), args, env); err == nil || raw != nil {
			t.Fatal("invalid Unix bytes repaired before validation")
		}
	}
	literal := "CONTROLLED=spaced <>& ä\nline"
	raw, err := encodeProxyLaunch(strings.Repeat("a", 64), []string{"mcp-files"}, []string{literal})
	if err != nil {
		t.Fatal(err)
	}
	var got proxyLaunch
	cwd, err := os.Getwd()
	if err != nil || json.Unmarshal(raw, &got) != nil || got.Env[0] != literal || got.Cwd != cwd {
		t.Fatal("actual cwd or literal environment changed")
	}
}
func TestMCPProxyActualSCMTransferCancellationAndBindings(t *testing.T) {
	t.Logf("go=%s os=%s arch=%s GOMAXPROCS=%d CPUs=%d GORACE=%q GOFLAGS=%q", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0), runtime.NumCPU(), os.Getenv("GORACE"), os.Getenv("GOFLAGS"))
	for _, mutation := range []string{"complete-duplex", "complete-sockets", "quiet-input", "quiet-sockets", "backpressure", "backpressure-sockets", "backpressure-validation-delay", "backpressure-validation-delay-sockets", "wrong-cwd", "wrong-session", "wrong-token", "wrong-tool", "wrong-parent", "bad-rights"} {
		t.Run(mutation, func(t *testing.T) {
			b := actualUnitBinding(t)
			if mutation == "wrong-parent" {
				b.NativeOwnerPID = os.Getppid()
			}
			observed := make(chan struct{})
			var observedAt time.Time
			var once sync.Once
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			c, err := NewCoordinator(func(d Direction, raw []byte) error {
				_, err := ParseRPC(raw)
				if err == nil && d == ServerToClient && len(raw) > 1<<20 {
					once.Do(func() { observedAt = time.Now(); close(observed) })
					if strings.Contains(mutation, "validation-delay") {
						// Model descheduling after validation observation but before
						// forwarding. Cancellation cannot interrupt owner validation.
						timer := time.NewTimer(1200 * time.Millisecond)
						defer timer.Stop()
						select {
						case <-timer.C:
						case <-ctx.Done():
							return ctx.Err()
						}
					}
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			s, err := NewProxyServer(ctx, b, c, func(raw []byte) error {
				if string(raw) != "controlled helper stderr\n" {
					return protocolError()
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			ownedCancelled := make(chan struct{})
			var cancelledAt time.Time
			go func() {
				<-s.ctx.Done()
				cancelledAt = time.Now()
				close(ownedCancelled)
			}()
			defer func() { cancel(); <-ownedCancelled }()
			joined := false
			defer func() {
				if !joined {
					cancel()
					s.Join(time.Second)
				}
			}()
			socket, token := s.Capability()
			cwd := b.WorkDir
			session := b.SessionID
			kind := "mcp-files"
			switch mutation {
			case "wrong-cwd":
				cwd = filepath.Join(b.Home, "foreign-work")
				if os.Mkdir(cwd, 0700) != nil {
					t.Fatal("foreign cwd")
				}
			case "wrong-session":
				session = "stale-session"
			case "wrong-token":
				token = strings.Repeat("0", 64)
			case "wrong-tool":
				kind = "mcp-foreign"
			}
			cmd := exec.Command(b.Binary, kind, "-pyry-socket", b.Socket)
			cmd.Dir = cwd
			cmd.Env = []string{"HOME=" + b.Home, "PYRY_SESSION_ID=" + session, "PYRY_E2EMCP_UNIT_HELPER=1", "PYRY_E2EMCP_UNIT_PROXY=1", SocketEnv + "=" + socket, TokenEnv + "=" + token}
			if mutation == "bad-rights" {
				cmd.Env = append(cmd.Env, "PYRY_E2EMCP_UNIT_BAD_RIGHTS=1")
			}
			var input io.WriteCloser
			var output, stderr io.ReadCloser
			var socketChildren []*os.File
			if strings.HasSuffix(mutation, "-sockets") {
				pair := func() (*os.File, *os.File) {
					fds, e := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
					if e != nil {
						t.Fatal(e)
					}
					for _, fd := range fds {
						unix.CloseOnExec(fd)
						if unix.SetNonblock(fd, true) != nil {
							t.Fatal("nonblocking socketpair")
						}
					}
					parent, child := os.NewFile(uintptr(fds[0]), "parent-stdio"), os.NewFile(uintptr(fds[1]), "child-stdio")
					t.Cleanup(func() { parent.Close(); child.Close() })
					socketChildren = append(socketChildren, child)
					return parent, child
				}
				in, childIn := pair()
				out, childOut := pair()
				log, childErr := pair()
				input, output, stderr = in, out, log
				cmd.Stdin, cmd.Stdout, cmd.Stderr = childIn, childOut, childErr
			} else {
				input, err = cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				output, err = cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				stderr, err = cmd.StderrPipe()
				if err != nil {
					t.Fatal(err)
				}
			}
			defer input.Close()
			defer output.Close()
			defer stderr.Close()
			if cmd.Start() != nil {
				t.Fatal("proxy start")
			}
			for _, child := range socketChildren {
				child.Close()
			}
			waited := false
			defer func() {
				if !waited {
					cmd.Process.Kill()
					cmd.Wait()
				}
			}()
			// Bound broken cancellation independently of the live parent deadline.
			// Closing our endpoints also releases a cancellation-disabled control.
			watchdogDone := make(chan struct{})
			watchdog := time.AfterFunc(15*time.Second, func() {
				defer close(watchdogDone)
				cancel()
				input.Close()
				output.Close()
				stderr.Close()
				cmd.Process.Kill()
			})
			defer func() {
				if !watchdog.Stop() {
					<-watchdogDone
					t.Error("owned-I/O lifecycle watchdog fired")
				}
			}()
			var partialReader *bufio.Reader
			invalid := strings.HasPrefix(mutation, "wrong-") || mutation == "bad-rights"
			if !invalid {
				line, err := bufio.NewReader(stderr).ReadString('\n')
				if err != nil || line != "controlled helper stderr\n" {
					t.Fatal("actual server did not start", err)
				}
				if strings.HasPrefix(mutation, "backpressure") || (mutation == "complete-duplex" || mutation == "complete-sockets") {
					if WriteLine(input, []byte(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`)) != nil {
						t.Fatal("initialize")
					}
					r := bufio.NewReader(output)
					line, err := r.ReadBytes('\n')
					if err != nil || !bytes.Contains(line, []byte(`"id":0`)) {
						t.Fatal("initialize response", err)
					}
					params := `{"unitBackpressure":true}`
					if mutation == "complete-duplex" || mutation == "complete-sockets" {
						params = `{}`
					}
					if WriteLine(input, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)) != nil || WriteLine(input, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+params+`}`)) != nil {
						t.Fatal("tool request")
					}
					if mutation == "complete-duplex" || mutation == "complete-sockets" {
						if _, err := r.ReadBytes('\n'); err != nil {
							t.Fatal("real complete response", err)
						}
						input.Close()
						if err := cmd.Wait(); err != nil {
							t.Fatal("complete native proxy failed", err)
						}
						waited = true
						proofs, err := s.Join(time.Second)
						joined = true
						if err != nil || len(proofs) != 1 || proofs[0].NativeProxyPID != cmd.Process.Pid || proofs[0].NativeOwnerPID != os.Getpid() || proofs[0].ServerPID == cmd.Process.Pid || proofs[0].ExitCode != 0 || !proofs[0].InputEOF || !proofs[0].OutputEOF || !proofs[0].StderrEOF {
							t.Fatal("actual MCP child graph not joined", err)
						}
						if events, err := c.Finish(); err != nil || len(events) != 5 {
							t.Fatal("complete transport ledger", err)
						}
						return
					}
					select {
					case <-observed:
					case <-ctx.Done():
						t.Fatal("actual backpressure response missing")
					}
					// A byte proves forwarding began after all validation. Leave
					// the rest unread, keeping the actual large write incomplete.
					first, err := r.ReadByte()
					if err != nil || first != '{' {
						t.Fatal("large response never reached actual stdio", err)
					}
					if c.mu.TryLock() {
						c.mu.Unlock()
						t.Fatal("large forwarding write already completed")
					}
					partialReader = r
					t.Logf("validation-observed-to-first-byte=%s", time.Since(observedAt))
				}
			} else {
				// Rejected before server startup, without relying on an invented receipt.
				if err := cmd.Wait(); err == nil {
					t.Fatal("foreign native proxy accepted")
				}
				waited = true
			}
			if !invalid && s.ctx.Err() != nil {
				t.Fatal("owned run cancelled before Join budget", s.ctx.Err())
			}
			const joinBudget = 25 * time.Millisecond
			started := time.Now()
			_, err = s.Join(joinBudget)
			joined = true
			joinElapsed := time.Since(started)
			if err == nil || ctx.Err() != nil || s.ctx.Err() == nil {
				t.Fatalf("Join did not cancel independently: error=%v owned=%v parent=%v", err, s.ctx.Err(), ctx.Err())
			}
			<-ownedCancelled
			if !invalid && cancelledAt.Before(started.Add(joinBudget)) {
				t.Fatal("owned cancellation preceded Join budget expiry")
			}
			// handle removes its connection only after runOwnedServer has received
			// every pump result and the actual server's exec.Cmd.Wait result.
			s.mu.Lock()
			active := len(s.conns)
			s.mu.Unlock()
			if active != 0 {
				t.Fatal("Join returned with active owned handlers")
			}
			t.Logf("owned-cancel-observed=%s Join=%s parent-live=%v active-handlers=%d", cancelledAt.Sub(started), joinElapsed, ctx.Err() == nil, active)
			if partialReader != nil {
				// EOF comes from real descriptor/child shutdown, after cancellation.
				// A complete response would invalidate the backpressure premise.
				n, err := io.Copy(io.Discard, partialReader)
				if err != nil || n+1 >= 2<<20 {
					t.Fatal("cancelled response was not partial EOF", n+1, err)
				}
				t.Logf("partial-response-bytes=%d output-EOF-after-Join=%s", n+1, time.Since(started)-joinElapsed)
			}
			if !waited {
				waitStarted := time.Now()
				if err := cmd.Wait(); err == nil {
					t.Fatal("cancelled proxy reported success")
				}
				waited = true
				t.Logf("native-proxy-Wait=%s exited=%v", time.Since(waitStarted), cmd.ProcessState.Exited())
			}
			if ctx.Err() != nil {
				t.Fatal("parent expired during child/EOF joins", ctx.Err())
			}
			if _, err := c.Finish(); err == nil {
				t.Fatal("cancelled or foreign evidence sealed")
			}
			if invalid {
				s.mu.Lock()
				n := len(s.claimed)
				s.mu.Unlock()
				if n != 0 {
					t.Fatal("foreign cwd/session/token/tool reached server startup")
				}
			}
		})
	}
}

func TestMCPConnectedUnixStreamRejectsOtherDescriptors(t *testing.T) {
	for _, kind := range []int{unix.SOCK_STREAM, unix.SOCK_DGRAM} {
		fds, e := unix.Socketpair(unix.AF_UNIX, kind, 0)
		if e != nil {
			t.Fatal(e)
		}
		f := os.NewFile(uintptr(fds[0]), "controlled-socket")
		peer := os.NewFile(uintptr(fds[1]), "controlled-peer")
		err := connectedUnixStream(f)
		f.Close()
		peer.Close()
		if (err == nil) != (kind == unix.SOCK_STREAM) {
			t.Fatal("socket transport type misclassified")
		}
	}
	fd, e := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if e != nil {
		t.Fatal(e)
	}
	f := os.NewFile(uintptr(fd), "unconnected")
	defer f.Close()
	if connectedUnixStream(f) == nil {
		t.Fatal("unconnected local socket accepted")
	}
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	client, e := net.Dial("tcp4", listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	server, e := listener.Accept()
	if e != nil {
		t.Fatal(e)
	}
	defer server.Close()
	network, e := client.(*net.TCPConn).File()
	if e != nil {
		t.Fatal(e)
	}
	defer network.Close()
	if connectedUnixStream(network) == nil {
		t.Fatal("network stream accepted")
	}
}
