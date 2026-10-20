package e2emcp

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testLaunch(t *testing.T) LaunchBinding {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if os.Chmod(home, 0700) != nil || os.Mkdir(filepath.Join(home, "work"), 0700) != nil {
		t.Fatal("private home")
	}
	path := filepath.Join(home, "source-binary")
	body := []byte("controlled non-executed source bytes")
	if os.WriteFile(path, body, 0700) != nil {
		t.Fatal("source")
	}
	sum := sha256.Sum256(body)
	return LaunchBinding{NativeOwnerPID: os.Getpid(), Binary: path, BinarySHA256: hex.EncodeToString(sum[:]), Home: home, WorkDir: filepath.Join(home, "work"), Socket: filepath.Join(home, "control.sock"), SessionID: "current-session"}
}
func TestMCPLaunchRequiresCurrentExactSourceAndIdentities(t *testing.T) {
	b := testLaunch(t)
	if b.validate() != nil {
		t.Fatal("valid source binding")
	}
	env := []string{"HOME=" + b.Home, "PYRY_SESSION_ID=" + b.SessionID, "PATH=/usr/bin:/bin", SocketEnv + "=/tmp/private-test.sock", TokenEnv + "=" + strings.Repeat("a", 64), "CONTROLLED_SECRET=memory-only"}
	got, err := b.serverEnvironment(env)
	if err != nil || len(got) != 4 || got[3] != "CONTROLLED_SECRET=memory-only" {
		t.Fatal("environment changed beyond intercept capabilities")
	}
	for _, entry := range got {
		if strings.HasPrefix(entry, SocketEnv+"=") || strings.HasPrefix(entry, TokenEnv+"=") {
			t.Fatal("recursive proxy capability retained")
		}
	}
	for _, kind := range []string{"mcp-files", "mcp-approve"} {
		if actual, err := b.serverArgs([]string{kind, "-pyry-socket", b.Socket}); err != nil || actual != kind {
			t.Fatal("exact current command rejected")
		}
	}
	for _, args := range [][]string{{"mcp-files", "-pyry-socket", b.Socket, "foreign"}, {"mcp-files", "-pyry-socket", b.Socket + ".foreign"}, {"mcp-files", "-pyry-name", "foreign"}, {"mcp-other", "-pyry-socket", b.Socket}, {"mcp-files"}} {
		if _, err := b.serverArgs(args); err == nil {
			t.Fatal("foreign server command accepted")
		}
	}
	for _, bad := range [][]string{{"HOME=" + b.Home, "HOME=" + b.Home, "PYRY_SESSION_ID=" + b.SessionID}, {"HOME=" + b.Home}, {"PYRY_SESSION_ID=" + b.SessionID}, {"HOME=" + b.Home + ".foreign", "PYRY_SESSION_ID=" + b.SessionID}, {"HOME=" + b.Home, "PYRY_SESSION_ID=old-session"}, {"HOME=" + b.Home, "PYRY_SESSION_ID=" + b.SessionID, "MALFORMED"}} {
		if _, err := b.serverEnvironment(bad); err == nil {
			t.Fatal("foreign or ambiguous environment accepted")
		}
	}
	if os.WriteFile(b.Binary, []byte("changed source"), 0700) != nil || b.validate() == nil {
		t.Fatal("changed source accepted")
	}
}
func TestMCPLaunchRejectsLinkedOrPublicRig(t *testing.T) {
	for _, mutation := range []string{"home-public", "work-public", "source-symlink", "source-nonexec", "source-bad-digest", "owner-unset", "work-foreign"} {
		t.Run(mutation, func(t *testing.T) {
			b := testLaunch(t)
			switch mutation {
			case "home-public":
				if os.Chmod(b.Home, 0755) != nil {
					t.Fatal("chmod")
				}
			case "work-public":
				if os.Chmod(b.WorkDir, 0755) != nil {
					t.Fatal("chmod")
				}
			case "source-symlink":
				original := b.Binary
				b.Binary += "-link"
				if os.Symlink(original, b.Binary) != nil {
					t.Fatal("link")
				}
			case "source-nonexec":
				if os.Chmod(b.Binary, 0600) != nil {
					t.Fatal("chmod")
				}
			case "source-bad-digest":
				b.BinarySHA256 = strings.Repeat("0", 64)
			case "owner-unset":
				b.NativeOwnerPID = 0
			case "work-foreign":
				b.WorkDir += "-foreign"
			}
			if b.validate() == nil {
				t.Fatal("unowned launch rig accepted")
			}
		})
	}
}
func TestMCPPeerIdentityComesFromCurrentKernelConnection(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "mcp-peer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	socket := filepath.Join(root, "peer.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	type identity struct {
		pid, parent int
		exe         string
		err         error
	}
	done := make(chan identity, 1)
	go func() {
		peer, e := listener.AcceptUnix()
		if e != nil {
			done <- identity{err: e}
			return
		}
		defer peer.Close()
		pid, parent, exe, e := currentPeer(peer)
		done <- identity{pid, parent, exe, e}
	}()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	actual := <-done
	exe, err := os.Executable()
	if err != nil || actual.err != nil || actual.pid != os.Getpid() || actual.parent != os.Getppid() || actual.exe != exe {
		t.Fatal("kernel identity mismatch")
	}
}
