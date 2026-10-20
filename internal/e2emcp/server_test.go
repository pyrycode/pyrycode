package e2emcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// A controlled subprocess proves pipe/EOF/Wait ownership controls only. It is
// never used by a native scenario or counted as real MCP implementation proof.
func TestMain(m *testing.M) {
	if os.Getenv("PYRY_E2EMCP_UNIT_HELPER") == "1" {
		if len(os.Args) != 4 || os.Args[2] != "-pyry-socket" {
			os.Exit(2)
		}
		if os.Getenv("PYRY_E2EMCP_UNIT_PROXY") == "1" && os.Getenv(SocketEnv) != "" {
			files := [3]*os.File{os.Stdin, os.Stdout, os.Stderr}
			if os.Getenv("PYRY_E2EMCP_UNIT_BAD_RIGHTS") == "1" {
				files[0] = os.Stdout
			}
			if ProxyCommand(os.Getenv(SocketEnv), os.Getenv(TokenEnv), os.Args[1:], os.Environ(), files) != nil {
				os.Exit(6)
			}
			os.Exit(0)
		}
		if os.Args[1] != "mcp-files" {
			os.Exit(2)
		}
		if os.Getenv("PYRY_E2EMCP_UNIT_EARLY_EXIT") == "1" {
			os.Exit(0)
		}
		_, _ = io.WriteString(os.Stderr, "controlled helper stderr\n")
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 4096), MaxFrameBytes)
		for scanner.Scan() {
			f, err := ParseRPC(scanner.Bytes())
			if err != nil {
				os.Exit(3)
			}
			if f.Kind == Notification {
				continue
			}
			response := append([]byte(`{"jsonrpc":"2.0","id":`), f.ID...)
			if bytes.Contains(f.Params, []byte(`"unitBackpressure":true`)) {
				response = append(response, []byte(`,"result":{"text":"`+strings.Repeat("x", 2<<20)+`"}}`)...)
			} else {
				response = append(response, []byte(`,"result":{}}`)...)
			}
			if WriteLine(os.Stdout, response) != nil {
				os.Exit(4)
			}
		}
		if scanner.Err() != nil {
			os.Exit(5)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestOwnedMCPServerRequiresActualCompleteDuplexAndWait(t *testing.T) {
	b := testLaunch(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	b.Binary = exe
	b.BinarySHA256 = hex.EncodeToString(hash[:])
	c, err := NewCoordinator(func(_ Direction, raw []byte) error { _, err := ParseRPC(raw); return err })
	if err != nil || c.Register("mcp-files") != nil {
		t.Fatal("coordinator")
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outR.Close()
	defer outW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer errR.Close()
	defer errW.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		proof ServerProof
		err   error
	}
	done := make(chan result, 1)
	go func() {
		proof, err := runOwnedServer(ctx, b, "mcp-files", []string{"HOME=" + b.Home, "PYRY_SESSION_ID=" + b.SessionID, "PYRY_E2EMCP_UNIT_HELPER=1"}, [3]*os.File{inR, outW, errW}, c, func(raw []byte) error {
			if string(raw) != "controlled helper stderr\n" {
				return protocolError()
			}
			return nil
		})
		outW.Close()
		errW.Close()
		done <- result{proof, err}
	}()
	stderrDone := make(chan string, 1)
	go func() { data, _ := io.ReadAll(errR); stderrDone <- string(data) }()
	request := ` {"jsonrpc":"2.0","id":0,"method":"initialize","params":{}} `
	if WriteLine(inW, []byte(request)) != nil {
		t.Fatal("initialize write")
	}
	reader := bufio.NewReader(outR)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal("initialize read", err)
	}
	parsed, err := ParseRPC(bytes.TrimSuffix(line, []byte{'\n'}))
	if err != nil || parsed.IDKey != "number:0" {
		t.Fatal("real response", err)
	}
	if WriteLine(inW, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)) != nil || WriteLine(inW, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)) != nil {
		t.Fatal("requests")
	}
	line, err = reader.ReadBytes('\n')
	if err != nil {
		t.Fatal("list read", err)
	}
	parsed, err = ParseRPC(bytes.TrimSuffix(line, []byte{'\n'}))
	if err != nil || parsed.IDKey != "number:1" {
		t.Fatal("list response", err)
	}
	// Successful responses precede real EOF; the process cannot be sealed yet.
	inW.Close()
	actual := <-done
	if actual.err != nil || actual.proof.ServerPID <= 1 || actual.proof.ServerPID == os.Getpid() || actual.proof.ExitCode != 0 || !actual.proof.InputEOF || !actual.proof.OutputEOF || !actual.proof.StderrEOF || actual.proof.InputFrames != 3 || actual.proof.OutputFrames != 2 {
		t.Fatal("actual owned completion did not join", actual.err)
	}
	if <-stderrDone != "controlled helper stderr\n" {
		t.Fatal("stderr not forwarded")
	}
	events, err := c.Finish()
	if err != nil || len(events) != 5 || events[0].Raw != request {
		t.Fatal("complete literal ledger", err)
	}
}
func TestMCPFramingRejectsPartialAndOversizeEOF(t *testing.T) {
	for _, raw := range []string{`{"jsonrpc":"2.0","id":0,"result":{}}`, "partial\ntrailing", string(bytes.Repeat([]byte{'x'}, MaxFrameBytes+2)) + "\n"} {
		if _, eof, err := forwardLines(bytes.NewBufferString(raw), func(raw []byte) error { _, err := ParseRPC(raw); return err }); err == nil || eof {
			t.Fatal("partial or malformed producer accepted")
		}
	}
	literal := ` { "jsonrpc":"2.0", "id":0, "result":{} } `
	count, eof, err := forwardLines(bytes.NewBufferString(literal+"\n"), func(raw []byte) error {
		if string(raw) != literal {
			return protocolError()
		}
		return nil
	})
	if count != 1 || !eof || err != nil {
		t.Fatal("literal framing changed")
	}
}
func TestRPCEventStringDoesNotHideOrAlterDecodedRaw(t *testing.T) {
	// Storage is ordinary decoded text, with validation applied to the decoded
	// frame; base64 must not become a path around privacy validation.
	event := RPCEvent{Raw: ` {"jsonrpc":"2.0","id":1,"result":{"text":"<>&\u0041"}} `}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RPCEvent
	if json.Unmarshal(encoded, &decoded) != nil || decoded.Raw != event.Raw {
		t.Fatal("raw codec changed bytes")
	}
	if _, err := ParseRPC([]byte(decoded.Raw)); err != nil {
		t.Fatal("decoded raw unvalidated", err)
	}
}

func TestOwnedMCPServerRejectsEarlyExitWithoutNativeInputEOF(t *testing.T) {
	b := actualUnitBinding(t)
	c, err := NewCoordinator(func(_ Direction, raw []byte) error { _, err := ParseRPC(raw); return err })
	if err != nil || c.Register("mcp-files") != nil {
		t.Fatal("coordinator")
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outR.Close()
	defer outW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer errR.Close()
	defer errW.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	proof, err := runOwnedServer(ctx, b, "mcp-files", []string{"HOME=" + b.Home, "PYRY_SESSION_ID=" + b.SessionID, "PYRY_E2EMCP_UNIT_HELPER=1", "PYRY_E2EMCP_UNIT_EARLY_EXIT=1"}, [3]*os.File{inR, outW, errW}, c, func([]byte) error { return nil })
	if err == nil || proof.InputEOF || proof.ExitCode != 0 || ctx.Err() != nil || time.Since(started) > 2*time.Second {
		t.Fatal("early actual exit accepted or waited for long parent timeout")
	}
	if _, err := c.Finish(); err == nil {
		t.Fatal("missing genuine input EOF sealed")
	}
}
