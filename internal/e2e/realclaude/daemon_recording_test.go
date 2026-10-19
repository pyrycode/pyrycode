//go:build e2e_realclaude

package realclaude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/claudetape"
)

type daemonRecordingConfig struct {
	Process                claudetape.PoolProcessConfig
	Root, Receipt, Wrapper string
	DaemonSHA256           string
	Daemon                 string
}
type daemonRecordingReceipt struct {
	Success        bool
	Frames         int
	ArtifactSHA256 string
}
type daemonRecordingEndpoint struct{ Daemon, Socket string }
type daemonRecordingRole struct{ Role, ConversationID, SessionID, WorkDir string }

func readDaemonRecordingConfig(path string) (daemonRecordingConfig, error) {
	var c daemonRecordingConfig
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 16384 {
		return c, os.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return c, os.ErrInvalid
	}
	profile, ok := claudetape.DaemonSuiteProfiles()[c.Process.Scenario]
	if !ok || profile.Root != c.Root || poolRecordedRevision == "" || poolRecordedRevision != c.Process.Producer.Revision {
		return c, os.ErrInvalid
	}
	return c, nil
}
func daemonRecordingCurrent(t *testing.T) (daemonRecordingConfig, bool) {
	t.Helper()
	path := os.Getenv("PYRY_DAEMON_RECORDING_CONFIG")
	if path == "" {
		return daemonRecordingConfig{}, false
	}
	c, err := readDaemonRecordingConfig(path)
	if err != nil || c.Root != t.Name() {
		t.Fatal("daemon recording does not own this original root")
	}
	return c, true
}
func daemonProviderEnvironment(t *testing.T) (string, string) {
	t.Helper()
	if bin, home, ok := lifecycleProviderEnvironment(t); ok {
		return bin, home
	}
	c, recording := daemonRecordingCurrent(t)
	if !recording {
		bin, err := exec.LookPath("claude")
		if err != nil {
			t.Skipf("realclaude: claude not on PATH: %v", err)
		}
		return bin, liveHome(t)
	}
	for _, p := range []string{c.Process.Home, filepath.Join(c.Process.Home, "recording-roles")} {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatal("daemon recording directory is not private")
		}
	}
	if c.Process.Mode == "replay" {
		for _, key := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR"} {
			if os.Getenv(key) != "" {
				t.Fatal("daemon replay inherited provider credentials/configuration")
			}
		}
	} else if c.Process.Mode != "capture" || (os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "") {
		t.Fatal("daemon capture requires explicit credentials")
	}
	return c.Wrapper, c.Process.Home
}
func daemonRecordingNonce(t *testing.T, live int64) int64 {
	if _, ok := lifecycleRecordingCurrent(t); ok {
		return 2718281828
	}
	if _, ok := daemonRecordingCurrent(t); ok {
		return 2718281828
	}
	return live
}
func daemonRecordingWrite(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func daemonRecordingRead(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		return os.ErrInvalid
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		return os.ErrInvalid
	}
	return nil
}
func daemonRecordingRegisterSocket(t *testing.T, home, daemon, socket string) {
	t.Helper()
	lifecycleRecordingRegisterSocket(t, home, daemon, socket)
	c, ok := daemonRecordingCurrent(t)
	if !ok {
		return
	}
	raw, err := os.ReadFile(daemon)
	sum := sha256.Sum256(raw)
	if err != nil || hex.EncodeToString(sum[:]) != c.DaemonSHA256 || home != c.Process.Home || daemon != c.Daemon || !filepath.IsAbs(socket) {
		t.Fatal("daemon recording endpoint changed")
	}
	if daemonRecordingWrite(filepath.Join(home, "recording-endpoint.json"), daemonRecordingEndpoint{daemon, socket}) != nil {
		t.Fatal("daemon endpoint was rebound")
	}
}
func daemonRecordingRegisterConversation(t *testing.T, conv string) {
	t.Helper()
	lifecycleRecordingRegisterConversation(t, conv)
	c, ok := daemonRecordingCurrent(t)
	if !ok {
		return
	}
	var registry struct {
		Conversations []struct {
			ID, Cwd string
			Session string `json:"current_session_id"`
		} `json:"conversations"`
	}
	raw, err := os.ReadFile(filepath.Join(c.Process.Home, ".pyry", "test", "conversations.json"))
	if err != nil || json.Unmarshal(raw, &registry) != nil {
		t.Fatal("cannot validate minted conversation ownership")
	}
	role := daemonRecordingRole{ConversationID: conv}
	for _, row := range registry.Conversations {
		if row.ID == conv {
			if role.SessionID != "" {
				t.Fatal("duplicate minted conversation")
			}
			role.SessionID = row.Session
			role.WorkDir = row.Cwd
		}
	}
	if role.SessionID == "" || role.SessionID == livePerConvBootstrapUUID || !filepath.IsAbs(role.WorkDir) {
		t.Fatal("created conversation lacks its independent session")
	}
	dir := filepath.Join(c.Process.Home, "recording-roles")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		var prior daemonRecordingRole
		if daemonRecordingRead(filepath.Join(dir, entry.Name()), &prior) != nil || prior.SessionID == role.SessionID || prior.ConversationID == conv {
			t.Fatal("conversation role identity reused")
		}
	}
	profile := claudetape.DaemonSuiteProfiles()[c.Process.Scenario]
	index := len(entries) + 1
	if index >= len(profile.Roles) {
		t.Fatal("unrecorded extra conversation")
	}
	role.Role = profile.Roles[index]
	if daemonRecordingWrite(filepath.Join(dir, role.Role+".json"), role) != nil {
		t.Fatal("conversation role rebound")
	}
}
func finishDaemonRecording(t *testing.T) {
	t.Helper()
	finishLifecycleRecording(t)
	c, ok := daemonRecordingCurrent(t)
	if !ok || t.Failed() {
		return
	}
	if _, err := os.Stat(filepath.Join(c.Process.Home, "recording-unexpected")); err == nil {
		t.Fatal("unowned daemon invocation or helper failure")
	}
	profile := claudetape.DaemonSuiteProfiles()[c.Process.Scenario]
	entries, err := os.ReadDir(filepath.Join(c.Process.Home, "recording-roles"))
	if err != nil || len(entries) != len(profile.Roles)-1 {
		t.Fatal("daemon role inventory incomplete")
	}
	if os.WriteFile(c.Process.Finish, nil, 0600) != nil {
		t.Fatal("cannot seal daemon roles")
	}
	for _, role := range profile.Roles {
		prefix := filepath.Join(c.Process.Home, "recording-children", role)
		deadline := time.Now().Add(15 * time.Second)
		for {
			var r poolRecordingReceipt
			err := daemonRecordingRead(prefix+".receipt", &r)
			if err == nil {
				if !r.Success || r.Frames == 0 {
					t.Fatalf("daemon child role did not seal: %s", r.Error)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("daemon child role seal timed out")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := os.Stat(prefix + ".claim.duplicate"); err == nil {
			t.Fatal("additional invocation of daemon role")
		}

	}
	if _, err := os.Stat(filepath.Join(c.Process.Home, "recording-unexpected")); err == nil {
		t.Fatal("unowned daemon invocation before seal")
	}
	children, err := os.ReadDir(filepath.Join(c.Process.Home, "recording-children"))
	if err != nil || len(children) != 3*len(profile.Roles) {
		t.Fatal("daemon child artifact inventory changed")
	}
	expected := map[string]bool{}
	for _, role := range profile.Roles {
		for _, suffix := range []string{".claim", ".receipt", ".json"} {
			expected[role+suffix] = true
		}
	}
	for _, entry := range children {
		if !expected[entry.Name()] {
			t.Fatal("extra daemon child artifact")
		}
	}

	digest, err := claudetape.AuditDaemonSuiteArtifacts(c.Process.Home, c.Process.Scenario)
	if err != nil {
		t.Fatal(err)
	}
	if daemonRecordingWrite(c.Receipt, daemonRecordingReceipt{Success: true, Frames: len(profile.Roles), ArtifactSHA256: digest}) != nil {
		t.Fatal("daemon suite receipt already exists")
	}
}
func TestDaemonTapeHelperProcess(t *testing.T) {
	path := os.Getenv("PYRY_DAEMON_RECORDING_CONFIG")
	if path == "" {
		return
	}
	c, err := readDaemonRecordingConfig(path)
	if err != nil {
		os.Exit(2)
	}
	stage := "source identity"
	fail := func() {
		_ = os.WriteFile(filepath.Join(c.Process.Home, "recording-unexpected"), []byte(stage), 0600)
		os.Exit(2)
	}
	if c.Process.Mode == "capture" {
		exe, err := os.Executable()
		if err != nil {
			fail()
		}
		f, err := os.Open(exe)
		if err != nil {
			fail()
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		_ = f.Close()
		if err != nil || hex.EncodeToString(h.Sum(nil)) != c.Process.Producer.BinarySHA256 {
			fail()
		}
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(os.Stdout, c.Process.Producer.ClaudeVersion)
		os.Exit(0)
	}
	stage = "session identity"
	session := ""
	for i, arg := range args {
		if arg == "--session-id" && i+1 < len(args) {
			if session != "" {
				fail()
			}
			session = args[i+1]
		}
	}
	stage = "role inventory"
	role := daemonRecordingRole{}
	if session == livePerConvBootstrapUUID {
		role = daemonRecordingRole{Role: "bootstrap", SessionID: session, WorkDir: filepath.Join(c.Process.Home, "work")}
	} else {
		entries, err := os.ReadDir(filepath.Join(c.Process.Home, "recording-roles"))
		if err != nil {
			fail()
		}
		for _, entry := range entries {
			var r daemonRecordingRole
			if daemonRecordingRead(filepath.Join(c.Process.Home, "recording-roles", entry.Name()), &r) != nil {
				fail()
			}
			if r.SessionID == session {
				if role.Role != "" {
					fail()
				}
				role = r
			}
		}
	}
	profile := claudetape.DaemonSuiteProfiles()[c.Process.Scenario]
	valid := false
	for _, name := range profile.Roles {
		valid = valid || role.Role == name
	}
	stage = "unknown invocation flags:"
	for _, arg := range args {
		if strings.HasPrefix(arg, "--") && !strings.ContainsAny(arg, " =/\\\n\r\t") && len(arg) < 64 {
			stage += " " + arg
		}
	}
	if session == "" || !valid {
		fail()
	}
	stage = "endpoint identity"
	var endpoint daemonRecordingEndpoint
	if daemonRecordingRead(filepath.Join(c.Process.Home, "recording-endpoint.json"), &endpoint) != nil || endpoint.Daemon != c.Daemon {
		fail()
	}
	prefix := filepath.Join(c.Process.Home, "recording-children", role.Role)
	process := c.Process
	process.Scenario += "-" + role.Role
	process.Path = prefix + ".json"
	process.Claim = prefix + ".claim"
	process.WorkDir = role.WorkDir
	stage = "native child failure"
	frames, err := claudetape.RunDaemonProcess(claudetape.DaemonProcessConfig{PoolProcessConfig: process, Daemon: endpoint.Daemon, Socket: endpoint.Socket}, args, os.Stdin, os.Stdout, os.Stderr)
	r := poolRecordingReceipt{Success: err == nil, Frames: frames}
	if err != nil {
		r.Error = err.Error()
	}
	if _, dup := os.Stat(process.Claim + ".duplicate"); dup == nil {
		os.Exit(1)
	}
	if daemonRecordingWrite(prefix+".receipt", r) != nil {
		fail()
	}
	if err != nil {
		os.Exit(1)
	}

	// Keep the supervised wrapper alive through daemon cleanup. The sealed
	// prefix is immutable; later pipe activity is outside the asserted prefix.
	// This prevents sealing one role from causing a replacement launch while
	// the other roles are still sealing.
	fd := int(os.Stdin.Fd())
	if syscall.SetNonblock(fd, true) != nil {
		fail()
	}
	deadline := time.Now().Add(30 * time.Second)
	var discard [4096]byte
	for time.Now().Before(deadline) {
		n, e := syscall.Read(fd, discard[:])
		if n == 0 && e == nil {
			os.Exit(0)
		}
		if e != nil && e != syscall.EAGAIN && e != syscall.EWOULDBLOCK && e != syscall.EINTR {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(0)
}
