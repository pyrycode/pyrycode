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
	for _, mutation := range []string{"complete-duplex", "complete-sockets", "quiet-input", "backpressure", "wrong-cwd", "wrong-session", "wrong-token", "wrong-tool", "wrong-parent", "bad-rights"} {
		t.Run(mutation, func(t *testing.T) {
			b := actualUnitBinding(t)
			if mutation == "wrong-parent" {
				b.NativeOwnerPID = os.Getppid()
			}
			observed := make(chan struct{})
			var once sync.Once
			c, err := NewCoordinator(func(d Direction, raw []byte) error {
				_, err := ParseRPC(raw)
				if err == nil && d == ServerToClient && len(raw) > 1<<20 {
					once.Do(func() { close(observed) })
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s, err := NewProxyServer(ctx, b, c, func(raw []byte) error {
				if string(raw) != "controlled helper stderr\n" {
					return protocolError()
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			joined := false
			defer func() {
				if !joined {
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
			if mutation == "complete-sockets" {
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
			invalid := strings.HasPrefix(mutation, "wrong-") || mutation == "bad-rights"
			if !invalid {
				line, err := bufio.NewReader(stderr).ReadString('\n')
				if err != nil || line != "controlled helper stderr\n" {
					t.Fatal("actual server did not start", err)
				}
				if mutation == "backpressure" || (mutation == "complete-duplex" || mutation == "complete-sockets") {
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
					// Validator runs under the forwarding mutex. Leave the actual large
					// response unread so its pipe write cannot release it.
					select {
					case <-observed:
					case <-ctx.Done():
						t.Fatal("actual backpressure response missing")
					}

				}
			} else {
				// Rejected before server startup, without relying on an invented receipt.
				if err := cmd.Wait(); err == nil {
					t.Fatal("foreign native proxy accepted")
				}
				waited = true
			}
			started := time.Now()
			_, err = s.Join(25 * time.Millisecond)
			joined = true
			if err == nil {
				t.Fatal("failed or incomplete actual run accepted")
			}
			if time.Since(started) > time.Second {
				t.Fatal("Join waited for parent timeout instead of cancelling owned I/O")
			}
			if !waited {
				if err := cmd.Wait(); err == nil {
					t.Fatal("cancelled proxy reported success")
				}
				waited = true
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
