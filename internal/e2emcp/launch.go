package e2emcp

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const SocketEnv = "PYRY_E2E_MCP_SOCKET"
const TokenEnv = "PYRY_E2E_MCP_TOKEN"

// LaunchBinding is an in-memory current-source declaration supplied by the
// runtime owner. Never reconstruct it from recorded process IDs.
type LaunchBinding struct {
	NativeOwnerPID                   int
	Binary, BinarySHA256             string
	Home, WorkDir, Socket, SessionID string
}

func (b LaunchBinding) validate() error {
	if b.NativeOwnerPID <= 1 || !filepath.IsAbs(b.Binary) || filepath.Clean(b.Binary) != b.Binary || !filepath.IsAbs(b.Home) || filepath.Clean(b.Home) != b.Home || b.WorkDir != filepath.Join(b.Home, "work") || !filepath.IsAbs(b.Socket) || b.SessionID == "" || strings.ContainsAny(b.SessionID, "\x00\r\n") {
		return protocolError()
	}
	for _, path := range []string{b.Home, b.WorkDir} {
		st, err := os.Lstat(path)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm() != 0700 {
			return protocolError()
		}
	}
	return checkSourceBinary(b.Binary, b.BinarySHA256)
}
func checkSourceBinary(path, want string) error {
	digest, err := hex.DecodeString(want)
	if err != nil || len(digest) != sha256.Size || want != strings.ToLower(want) {
		return protocolError()
	}
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 || st.Size() <= 0 || st.Size() > 512<<20 {
		return protocolError()
	}
	f, err := os.Open(path)
	if err != nil {
		return protocolError()
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return protocolError()
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, (512<<20)+1))
	if err != nil || n != st.Size() || hex.EncodeToString(hash.Sum(nil)) != want {
		return protocolError()
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(actual, after) || after.Size() != actual.Size() || !after.ModTime().Equal(actual.ModTime()) {
		return protocolError()
	}
	return nil
}

// Native environment crosses private IPC in memory only. Preserve it for the
// actual same server, except for this test proxy's two capabilities. Required
// identities must be singular and equal the current owner.
func (b LaunchBinding) serverEnvironment(env []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" || strings.ContainsAny(name, "\x00\r\n") || strings.ContainsRune(value, 0) || seen[name] {
			return nil, protocolError()
		}
		seen[name] = true
		if name == "HOME" && value != b.Home || name == "PYRY_SESSION_ID" && value != b.SessionID {
			return nil, protocolError()
		}
		if name != SocketEnv && name != TokenEnv {
			out = append(out, entry)
		}
	}
	if !seen["HOME"] || !seen["PYRY_SESSION_ID"] {
		return nil, protocolError()
	}
	return out, nil
}
func (b LaunchBinding) serverArgs(args []string) (string, error) {
	if len(args) != 3 || args[1] != "-pyry-socket" || args[2] != b.Socket || args[0] != "mcp-files" && args[0] != "mcp-approve" {
		return "", protocolError()
	}
	return args[0], nil
}
