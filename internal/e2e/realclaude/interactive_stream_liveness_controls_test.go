//go:build e2e_realclaude

package realclaude

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// The controlled drain proof must cover the drain selected by live liveness.
func TestInteractiveStreamLivenessUsesCompletedTurnDrain(t *testing.T) {
	t.Parallel()
	f, err := parser.ParseFile(token.NewFileSet(), "interactive_stream_liveness_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestInteractiveStreamLiveness" {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if ok {
				name, ok := call.Fun.(*ast.Ident)
				if ok && name.Name == "drainForCompletedTurn" {
					found = true
				}
			}
			return true
		})
		if !found {
			t.Fatal("live liveness must use the controlled one-delta drainForCompletedTurn contract")
		}
		return
	}
	t.Fatal("live liveness test not found")
}

func TestCompletedTurnDrainControls(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"single", "multiple", "empty", "whitespace", "foreign_delta", "foreign_idle",
		"missing_idle", "queued_idle", "queued_idle_then_close", "foreign_then_valid",
		"whitespace_then_idle_then_text", "unrecognized", "rate_limited", "rate_warning",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCompletedTurnDrainHelperProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PYRY_LIVENESS_DRAIN_CASE="+name)
			out, err := cmd.CombinedOutput()
			wantFailure := map[string]string{
				"empty": "M1: never", "whitespace": "M1: never", "foreign_delta": "M1: never",
				"foreign_idle": "M2:", "missing_idle": "M2:", "queued_idle": "M2:",
				"whitespace_then_idle_then_text": "M2:",
				"unrecognized":                   "a NORMAL turn produced an unrecognized_message",
				"rate_limited":                   "UNRECOGNISED rate_limited",
			}[name]
			if ctx.Err() != nil {
				t.Fatalf("helper exceeded deadline: %v\n%s", ctx.Err(), out)
			}
			if wantFailure == "" {
				if err != nil || !strings.Contains(string(out), "drain completed with expected text") {
					t.Fatalf("valid evidence did not complete: %v\n%s", err, out)
				}
			} else if err == nil || !strings.Contains(string(out), wantFailure) {
				t.Fatalf("invalid evidence must fail with %q: %v\n%s", wantFailure, err, out)
			}
		})
	}
}

func TestCompletedTurnDrainHelperProcess(t *testing.T) {
	name := os.Getenv("PYRY_LIVENESS_DRAIN_CASE")
	if name == "" {
		return // Only the parent controls launch this synthetic drain.
	}
	delta := func(conv, text string) protocol.Envelope {
		return protocol.Envelope{Type: protocol.TypeAssistantDelta, Payload: mustJSON(t,
			protocol.AssistantDeltaPayload{ConversationID: conv, TurnID: "turn", Text: text})}
	}
	idle := func(conv string) protocol.Envelope {
		return protocol.Envelope{Type: protocol.TypeTurnState, Payload: mustJSON(t,
			protocol.TurnStatePayload{ConversationID: conv, State: "idle"})}
	}
	d, end := delta(streamConvID, "hello"), idle(streamConvID)
	frames, want := []protocol.Envelope{d, end}, "hello"
	switch name {
	case "single":
	case "multiple":
		frames, want = []protocol.Envelope{d, delta(streamConvID, " world"), end}, "hello world"
	case "empty":
		frames = []protocol.Envelope{delta(streamConvID, ""), end}
	case "whitespace":
		frames = []protocol.Envelope{delta(streamConvID, " \t\n"), end}
	case "foreign_delta":
		frames = []protocol.Envelope{delta("foreign", "secret"), end}
	case "foreign_idle":
		frames = []protocol.Envelope{d, idle("foreign")}
	case "missing_idle":
		frames = []protocol.Envelope{d}
	case "queued_idle":
		frames = []protocol.Envelope{end, d}
	case "queued_idle_then_close":
		frames = []protocol.Envelope{end, d, end}
	case "whitespace_then_idle_then_text":
		frames = []protocol.Envelope{delta(streamConvID, " \t"), end, d}
	case "foreign_then_valid":
		frames = []protocol.Envelope{delta("foreign", "secret"), idle("foreign"), end, d, idle("foreign"), delta(streamConvID, " world"), end}
		want = "hello world"
	case "unrecognized":
		frames = append([]protocol.Envelope{{Type: protocol.TypeUnrecognizedMessage,
			Payload: mustJSON(t, protocol.UnrecognizedMessagePayload{MessageType: "synthetic"})}}, frames...)
	case "rate_limited", "rate_warning":
		status := "synthetic_unknown"
		if name == "rate_warning" {
			status = warnRateLimitStatus
		}
		frames = append([]protocol.Envelope{{Type: protocol.TypeRateLimited,
			Payload: mustJSON(t, protocol.RateLimitedPayload{ConversationID: "foreign", Status: status})}}, frames...)
	default:
		t.Fatalf("unknown control %q", name)
	}
	send, recv := completedTurnCipherPair(t)
	phone := &completedTurnFrames{}
	for _, env := range frames {
		cipher, err := send.Encrypt(mustJSON(t, env))
		if err != nil {
			t.Fatal(err)
		}
		phone.frames = append(phone.frames, mustJSON(t, protocol.InnerFrameV2{
			Type: protocol.TypeNoiseMsg, Data: base64.StdEncoding.EncodeToString(cipher),
		}))
	}
	got := drainForCompletedTurn(t, phone, recv, streamConvID, time.Second)
	if got != want || len(phone.frames) != 0 {
		t.Fatalf("reply=%q remaining=%d, want %q and all frames consumed", got, len(phone.frames), want)
	}
	t.Log("drain completed with expected text")
}

type completedTurnFrames struct{ frames [][]byte }

func (f *completedTurnFrames) ReceiveBytes(timeout time.Duration) ([]byte, error) {
	if len(f.frames) == 0 {
		time.Sleep(timeout)
		return nil, fakephone.ErrReceiveTimeout
	}
	frame := f.frames[0]
	f.frames = f.frames[1:]
	return frame, nil
}

func completedTurnCipherPair(t *testing.T) (*noise.CipherState, *noise.CipherState) {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	init, err := noise.NewInitiator(key.Bytes(), peer.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := noise.NewResponder(peer.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	msg, err := init.WriteInit(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resp.ReadInit(msg); err != nil {
		t.Fatal(err)
	}
	msg, send, _, err := resp.WriteResp(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, recv, err := init.ReadResp(msg)
	if err != nil {
		t.Fatal(err)
	}
	return send, recv
}
