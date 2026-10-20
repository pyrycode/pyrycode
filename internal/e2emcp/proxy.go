package e2emcp

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const maxLaunchBytes = 1 << 20

type proxyLaunch struct {
	peerPID   int
	Version   int
	Token     string
	Args, Env []string
	Cwd       string
}

// ProxyCommand is invoked only by a test-tag command-entry hook. It transfers
// the actual native-owned stdio capabilities without reading MCP stdin to EOF.
// It never writes environment, credentials, or the capability to disk/logs.
func ProxyCommand(socket, token string, args, env []string, stdio [3]*os.File) error {
	if !filepath.IsAbs(socket) || len(socket) > 100 || !validToken(token) {
		return protocolError()
	}
	if !utf8.ValidString(socket) {
		return protocolError()
	}
	payload, err := encodeProxyLaunch(token, args, env)
	if err != nil {
		return err
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return protocolError()
	}
	defer c.Close()
	if c.SetWriteDeadline(time.Now().Add(10*time.Second)) != nil {
		return protocolError()
	}
	fds := make([]int, 3)
	for i, f := range stdio {
		if f == nil {
			return protocolError()
		}
		fds[i] = int(f.Fd())
	}
	rights := syscall.UnixRights(fds...)
	n, oob, err := c.WriteMsgUnix([]byte{1}, rights, nil)
	if err != nil || n != 1 || oob != len(rights) {
		return protocolError()
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(payload)))
	if writeAll(c, length[:]) != nil || writeAll(c, payload) != nil || c.CloseWrite() != nil {
		return protocolError()
	}
	// The real MCP server can legitimately wait for a person's decision. Only
	// startup writes have a short budget; the runtime owner cancels stalled runs.
	var status [1]byte
	if _, err := io.ReadFull(c, status[:]); err != nil || status[0] != 1 {
		return protocolError()
	}
	var extra [1]byte
	n, err = c.Read(extra[:])
	if n != 0 || err != io.EOF {
		return protocolError()
	}
	return nil
}
func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
func validToken(token string) bool {
	raw, err := hex.DecodeString(token)
	return err == nil && len(raw) == 32 && token == hex.EncodeToString(raw)
}
func newToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", protocolError()
	}
	return hex.EncodeToString(token[:]), nil
}

// receiveLaunch authenticates a current kernel peer and its actual executable
// before any server is started. Its files are borrowed only on successful return;
// every rejected SCM_RIGHTS capability is closed here.
func receiveLaunch(c *net.UnixConn, b LaunchBinding, token string) (launch proxyLaunch, files [3]*os.File, err error) {
	fail := func() (proxyLaunch, [3]*os.File, error) {
		for _, f := range files {
			if f != nil {
				f.Close()
			}
		}
		return proxyLaunch{}, [3]*os.File{}, protocolError()
	}
	if !validToken(token) || c.SetReadDeadline(time.Now().Add(10*time.Second)) != nil {
		return fail()
	}
	var one [1]byte
	control := make([]byte, syscall.CmsgSpace(3*4))
	n, oob, flags, _, e := c.ReadMsgUnix(one[:], control)
	messages, parseErr := syscall.ParseSocketControlMessage(control[:oob])
	var received []int
	for _, message := range messages {
		rights, x := syscall.ParseUnixRights(&message)
		if x != nil {
			parseErr = x
		}
		received = append(received, rights...)
	}
	// Close all received descriptors even when the sender supplied an extra one.
	owned := make([]*os.File, 0, len(received))
	var modes []int
	for _, fd := range received {
		syscall.CloseOnExec(fd)
		mode, x := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if x != nil {
			syscall.Close(fd)
			parseErr = protocolError()
			continue
		}
		modes = append(modes, mode)
		if unix.SetNonblock(fd, true) != nil {
			syscall.Close(fd)
			parseErr = protocolError()
			continue
		}
		owned = append(owned, os.NewFile(uintptr(fd), "native-mcp-pipe"))
	}
	accepted := false
	defer func() {
		if !accepted {
			for _, f := range owned {
				f.Close()
			}
		}
	}()
	if e != nil || parseErr != nil || flags&(syscall.MSG_TRUNC|syscall.MSG_CTRUNC) != 0 || n != 1 || one[0] != 1 || len(messages) != 1 || len(owned) != 3 {
		return proxyLaunch{}, files, protocolError()
	}
	for i, f := range owned {
		info, x := f.Stat()
		if x != nil || info.Mode()&os.ModeNamedPipe == 0 {
			return proxyLaunch{}, files, protocolError()
		}
		for j := 0; j < i; j++ {
			other, _ := owned[j].Stat()
			if other == nil || os.SameFile(info, other) {
				return proxyLaunch{}, files, protocolError()
			}
		}
		mode := modes[i]
		want := unix.O_WRONLY
		if i == 0 {
			want = unix.O_RDONLY
		}
		if mode&unix.O_ACCMODE != want {
			return proxyLaunch{}, files, protocolError()
		}
	}
	var length [4]byte
	if _, e = io.ReadFull(c, length[:]); e != nil {
		return proxyLaunch{}, files, protocolError()
	}
	size := binary.BigEndian.Uint32(length[:])
	if size == 0 || size > maxLaunchBytes {
		return proxyLaunch{}, files, protocolError()
	}
	payload := make([]byte, int(size))
	if _, e = io.ReadFull(c, payload); e != nil {
		return proxyLaunch{}, files, protocolError()
	}
	var extra [1]byte
	n, e = c.Read(extra[:])
	if n != 0 || e != io.EOF || !uniqueJSON(payload) {
		return proxyLaunch{}, files, protocolError()
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&launch) != nil || decoder.Decode(new(any)) != io.EOF || launch.Version != 1 || subtle.ConstantTimeCompare([]byte(launch.Token), []byte(token)) != 1 {
		return proxyLaunch{}, files, protocolError()
	}
	pid, parent, exe, e := currentPeer(c)
	if e != nil || parent != b.NativeOwnerPID || exe != b.Binary || b.validate() != nil {
		return proxyLaunch{}, files, protocolError()
	}
	if launch.Cwd != b.WorkDir {
		return proxyLaunch{}, files, protocolError()
	}
	if _, e = b.serverArgs(launch.Args); e != nil {
		return proxyLaunch{}, files, protocolError()
	}
	if _, e = b.serverEnvironment(launch.Env); e != nil {
		return proxyLaunch{}, files, protocolError()
	}
	for i, f := range owned {
		files[i] = f
	}
	launch.peerPID = pid
	accepted = true
	return launch, files, nil
}

// The authenticated current-source sender observes cwd itself. It accepts no
// caller-supplied cwd that could repair a foreign native launch. Validate Unix
// strings before encoding/json can replace invalid UTF-8 with U+FFFD.
func encodeProxyLaunch(token string, args, env []string) ([]byte, error) {
	cwd, err := os.Getwd()
	if err != nil || !utf8.ValidString(cwd) {
		return nil, protocolError()
	}
	total := 0
	for _, values := range [][]string{args, env} {
		for _, value := range values {
			total += len(value)
			if !utf8.ValidString(value) || total > maxLaunchBytes {
				return nil, protocolError()
			}
		}
	}
	payload, err := json.Marshal(proxyLaunch{Version: 1, Token: token, Args: args, Env: env, Cwd: cwd})
	if err != nil || len(payload) > maxLaunchBytes {
		return nil, protocolError()
	}
	return payload, nil
}
