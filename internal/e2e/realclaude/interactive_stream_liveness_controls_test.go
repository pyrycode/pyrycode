//go:build e2e_realclaude

package realclaude

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/history"
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
		"missing_idle", "omitted_session", "foreign_session", "null_session", "queued_idle", "queued_idle_then_close", "foreign_then_valid",
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
				"omitted_session": "M2:", "foreign_session": "M2:", "null_session": "M2:",
				"whitespace_then_idle_then_text": "M1: never",
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
		return protocol.Envelope{Type: protocol.TypeTurnState, SessionID: json.RawMessage(`"fixture"`), Payload: mustJSON(t,
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
	case "omitted_session", "foreign_session", "null_session":
		end.SessionID = map[string]json.RawMessage{"omitted_session": nil, "foreign_session": json.RawMessage(`"foreign"`), "null_session": json.RawMessage(`null`)}[name]
		frames = []protocol.Envelope{d, end}
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
	phone := newCompletedSourceFrames(t, send, frames)
	got := drainForCompletedTurn(t, phone, recv, streamConvID, time.Second, scenario.SourceFacts(func(string) []history.Entry { return phone.source }))
	if got != want || len(phone.frames) != 0 {
		t.Fatalf("reply=%q remaining=%d, want %q and all frames consumed", got, len(phone.frames), want)
	}
	t.Log("drain completed with expected text")
}

func newCompletedSourceFrames(t *testing.T, send *noise.CipherState, frames []protocol.Envelope) *completedTurnFrames {
	t.Helper()
	phone := &completedTurnFrames{}
	seenText := map[string]bool{}
	var pending []history.Entry
	var sourceID uint64
	appendSource := func(env protocol.Envelope) {
		sourceID++
		pending = append(pending, history.Entry{ID: sourceID, Type: env.Type, Payload: env.Payload, Session: &history.SessionProvenance{Kind: "claude", SessionID: "fixture"}})
	}
	for _, env := range frames {
		if env.Type == protocol.TypeAssistantDelta {
			var p protocol.AssistantDeltaPayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid source fixture")
			}
			seenText[p.ConversationID] = true
			appendSource(env)
			continue
		}
		if env.Type == protocol.TypeTurnState {
			var p protocol.TurnStatePayload
			if json.Unmarshal(env.Payload, &p) != nil {
				t.Fatal("invalid state fixture")
			}
			if p.State == "idle" && seenText[p.ConversationID] {
				appendSource(protocol.Envelope{Type: protocol.TypeTurnEnd, Payload: mustJSON(t, protocol.TurnEndPayload{ConversationID: p.ConversationID, TurnID: "turn", StopReason: "end_turn"})})
			}
		}
		cipher, err := send.Encrypt(mustJSON(t, env))
		if err != nil {
			t.Fatal(err)
		}
		phone.frames = append(phone.frames, mustJSON(t, protocol.InnerFrameV2{
			Type: protocol.TypeNoiseMsg, Data: base64.StdEncoding.EncodeToString(cipher),
		}))
		phone.before = append(phone.before, pending)
		pending = nil
	}
	phone.tail = pending
	return phone
}

type completedTurnFrames struct {
	frames       [][]byte
	before       [][]history.Entry
	tail, source []history.Entry
}

func (f *completedTurnFrames) ReceiveBytes(timeout time.Duration) ([]byte, error) {
	if len(f.frames) == 0 {
		f.source = append(f.source, f.tail...)
		f.tail = nil
		time.Sleep(timeout)
		return nil, fakephone.ErrReceiveTimeout
	}
	frame := f.frames[0]
	f.source = append(f.source, f.before[0]...)
	f.before = f.before[1:]
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
