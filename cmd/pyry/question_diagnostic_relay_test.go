package main

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/identity"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/relay"
)

type questionLogCapture struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	completed chan struct{}
}

func (c *questionLogCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, err := c.buf.Write(p)
	if bytes.Contains(p, []byte(`"event":"v2.question.completed"`)) {
		select {
		case c.completed <- struct{}{}:
		default:
		}
	}
	return n, err
}

func TestQuestionResolverV2_RelayDiagnosticRecords(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{protocol.TypeQuestionAnswer, protocol.TypeQuestionRefused} {
		for _, outcome := range []string{"resolved", "missing_correlation", "unknown", "empty"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				t.Parallel()
				capture := &questionLogCapture{completed: make(chan struct{}, 4)}
				logger := slog.New(slog.NewJSONHandler(capture, nil))
				f := newQuestionFixture(t, testConvID, logger)
				_, _, id := surfacedQuestion(t, f, "ZZ2801QUESTIONTEXTZZ")
				wantReason := outcome
				switch outcome {
				case "missing_correlation":
					delete(f.bridge.byQuestion, id)
				case "unknown":
					id, wantReason = "never-surfaced", "unknown_or_retired_batch"
				case "empty":
					id, wantReason = "", "unknown_or_retired_batch"
				}
				serverKey, err := ecdh.X25519().GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				phoneKey, err := ecdh.X25519().GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				const token = "ZZ2801DEVICETOKENZZ"
				paired := &devices.Registry{}
				paired.Add(devices.Device{TokenHash: devices.HashToken(token), Name: "phone", PairedAt: time.Now().UTC()})
				frames := make(chan protocol.RoutingEnvelope, 4)
				outbound := make(chan protocol.RoutingEnvelope, 8)
				mgr, err := relay.NewV2SessionManager(relay.V2SessionConfig{
					Frames: frames, Outbound: func(env protocol.RoutingEnvelope) error { outbound <- env; return nil },
					StaticPriv: serverKey.Bytes(), Devices: paired, ServerID: string(identity.NewServerID()),
					Logger: logger, QuestionResolver: gatedResolver(f, logger),
				})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() { defer close(done); _ = mgr.Run(ctx) }()
				t.Cleanup(func() {
					cancel()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("relay did not stop")
					}
				})
				marshal := func(v any) []byte {
					t.Helper()
					b, err := json.Marshal(v)
					if err != nil {
						t.Fatal(err)
					}
					return b
				}
				const connID = "question-diagnostic-conn"
				wrap := func(kind string, data []byte) protocol.RoutingEnvelope {
					return protocol.RoutingEnvelope{ConnID: connID, Frame: marshal(protocol.InnerFrameV2{
						Version: protocol.V2Version, Type: kind, Data: base64.StdEncoding.EncodeToString(data),
					})}
				}
				initiator, err := noise.NewInitiator(phoneKey.Bytes(), serverKey.PublicKey().Bytes())
				if err != nil {
					t.Fatal(err)
				}
				hello := marshal(protocol.Envelope{ID: 1, Type: protocol.TypeHello, TS: time.Now().UTC(), Payload: marshal(protocol.HelloClientPayload{
					Role: "client", DeviceName: "phone", ClientVersion: "v2-test", ProtocolVersions: []string{"v2"}, Token: token,
					Capabilities: []string{protocol.CapabilityInteractive},
				})})
				initMsg, err := initiator.WriteInit(hello)
				if err != nil {
					t.Fatal(err)
				}
				frames <- wrap(protocol.TypeNoiseInit, initMsg)
				var response protocol.RoutingEnvelope
				select {
				case response = <-outbound:
				case <-time.After(5 * time.Second):
					t.Fatal("no handshake response")
				}
				var inner protocol.InnerFrameV2
				if err := json.Unmarshal(response.Frame, &inner); err != nil {
					t.Fatal(err)
				}
				if inner.Type != protocol.TypeNoiseResp {
					t.Fatalf("response type = %q", inner.Type)
				}
				responseBytes, err := base64.StdEncoding.DecodeString(inner.Data)
				if err != nil {
					t.Fatal(err)
				}
				_, send, _, err := initiator.ReadResp(responseBytes)
				if err != nil {
					t.Fatal(err)
				}
				payload := map[string]any{
					"question_batch_id": id, "answer_token": testAnswerToken,
					"answers":  []protocol.QuestionAnswerEntry{{QuestionIndex: 0, Values: []string{"ZZ2801ANSWERVALUEZZ"}}, {QuestionIndex: 1, Values: []string{"ZZ2801ANSWERVALUEZZ"}}},
					"question": "ZZ2801QUESTIONTEXTZZ", "options": []string{"ZZ2801OPTIONLABELZZ"},
				}
				sealed, err := send.Encrypt(marshal(protocol.Envelope{ID: 2, Type: kind, TS: time.Now().UTC(), Payload: marshal(payload)}))
				if err != nil {
					t.Fatal(err)
				}
				frames <- wrap(protocol.TypeNoiseMsg, sealed)
				select {
				case <-capture.completed:
				case <-time.After(5 * time.Second):
					t.Fatal("no terminal record")
				}
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("relay did not stop after completion")
				}
				if outcome == "resolved" {
					waitPush(t, f.pushed)
				}
				capture.mu.Lock()
				log := capture.buf.String()
				capture.mu.Unlock()
				var received, completed int
				for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
					var rec map[string]any
					if err := json.Unmarshal([]byte(line), &rec); err != nil {
						t.Fatal(err)
					}
					if rec["event"] == "v2.question.received" {
						received++
						if rec["level"] != "INFO" || rec["conn_id"] != connID || rec["frame_kind"] != kind {
							t.Errorf("receipt = %#v", rec)
						}
					}
					if rec["event"] != "v2.question.completed" {
						if _, ok := rec["reason"]; ok {
							t.Errorf("additional reason record = %#v", rec)
						}
						continue
					}
					completed++
					if rec["level"] != "INFO" || rec["reason"] != wantReason || rec["conn_id"] != connID || rec["question_batch_id"] != id || rec["frame_kind"] != kind {
						t.Errorf("terminal = %#v", rec)
					}
				}
				if received != 1 || completed != 1 {
					t.Errorf("receipts=%d terminals=%d, want one each", received, completed)
				}
				for _, secret := range []string{token, testAnswerToken, "ZZ2801QUESTIONTEXTZZ", "ZZ2801OPTIONLABELZZ", "ZZ2801ANSWERVALUEZZ", "question_index"} {
					if strings.Contains(log, secret) {
						t.Errorf("log disclosed %q", secret)
					}
				}
			})
		}
	}
}
