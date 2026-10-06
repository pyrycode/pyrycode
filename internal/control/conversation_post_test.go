package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

func conversationPostWire(t *testing.T, sock, wire string) string {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte(wire + "\n")); err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if err := json.NewDecoder(conn).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestConversationPost_Forwarding(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, id, text string }{
		{"unchanged", " caller-id/../opaque ", " \nmessage: café 世界\t "},
		{"whitespace", " \t ", "\n "},
		{"ASCII cap", "ascii-id", strings.Repeat("x", MaxChannelPostBytes)},
		{"UTF-8 cap", "unicode-id", strings.Repeat("é", MaxChannelPostBytes/2)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := make(chan ConversationPostPayload, 3)
			var logs bytes.Buffer
			_, sock := startConversationServer(t, func(s *Server) {
				s.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
				s.sessions = &recordingResolver{
					delegate: s.sessions,
					record: func(sessions.SessionID) {
						t.Error("conversation.post performed a session lookup")
					},
					resolveRecord: func(string) {
						t.Error("conversation.post resolved the caller id")
					},
				}
				s.SetConversationSubmitter(func(id, text string) error {
					calls <- ConversationPostPayload{ConversationID: id, Text: text}
					return nil
				})
			})
			want := ConversationPostPayload{ConversationID: tt.id, Text: tt.text}
			if err := ConversationPost(context.Background(), sock, tt.id, tt.text); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || !reflect.DeepEqual(<-calls, want) {
				t.Fatal("client did not forward exactly once unchanged")
			}
			// Escape é to distinguish decoded bytes from wire bytes in the UTF-8 cap case.
			wire, err := json.Marshal(Request{Verb: VerbConversationPost, ConversationPost: &want})
			if err != nil {
				t.Fatal(err)
			}
			escaped := strings.ReplaceAll(string(wire), "é", `\u00e9`)
			if got := conversationPostWire(t, sock, escaped); got != `{"ok":true}` {
				t.Fatalf("success response = %s", got)
			}
			if len(calls) != 1 || !reflect.DeepEqual(<-calls, want) {
				t.Fatal("wire did not forward exactly once unchanged")
			}
			if strings.Contains(logs.String(), tt.id) || strings.Contains(logs.String(), tt.text) {
				t.Error("control logs leaked accepted id or message text")
			}
		})
	}
}

func TestConversationPost_Refusals(t *testing.T) {
	t.Parallel()
	const id = "private-caller-id-sentinel"
	const text = "private-message-text-sentinel"
	valid := `,"conversationPost":{"conversationID":"` + id + `","text":"` + text + `"}`
	for _, tt := range []struct {
		name, fields, want string
		installed          bool
		refusal            error
		calls              int
	}{
		{"unwired valid", valid, "no conversation submitter configured", false, nil, 0},
		{"unwired absent", "", "no conversation submitter configured", false, nil, 0},
		{"unwired invalid", `,"conversationPost":{}`, "no conversation submitter configured", false, nil, 0},
		{"absent", "", "missing conversation id", true, nil, 0},
		{"null payload", `,"conversationPost":null`, "missing conversation id", true, nil, 0},
		{"missing id", `,"conversationPost":{"text":"` + text + `"}`, "missing conversation id", true, nil, 0},
		{"empty id", `,"conversationPost":{"conversationID":"","text":"` + text + `"}`, "missing conversation id", true, nil, 0},
		{"null id", `,"conversationPost":{"conversationID":null,"text":"` + text + `"}`, "missing conversation id", true, nil, 0},
		{"missing text", `,"conversationPost":{"conversationID":"` + id + `"}`, "empty message", true, nil, 0},
		{"empty text", `,"conversationPost":{"conversationID":"` + id + `","text":""}`, "empty message", true, nil, 0},
		{"null text", `,"conversationPost":{"conversationID":"` + id + `","text":null}`, "empty message", true, nil, 0},
		{"ASCII over cap", `,"conversationPost":{"conversationID":"` + id + `","text":"` + strings.Repeat("x", MaxChannelPostBytes+1) + `"}`, "message too large", true, nil, 0},
		{"escaped UTF-8 over cap", `,"conversationPost":{"conversationID":"` + id + `","text":"` + strings.Repeat(`\u00e9`, MaxChannelPostBytes/2) + `x"}`, "message too large", true, nil, 0},
		{"unknown id", valid, "unknown conversation", true, errors.New("unknown conversation"), 1},
		{"backlog full", valid, "backlog full", true, errors.New("backlog full"), 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := make(chan struct{}, 2)
			var logs bytes.Buffer
			_, sock := startConversationServer(t, func(s *Server) {
				s.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
				if tt.installed {
					s.SetConversationSubmitter(func(_, _ string) error {
						calls <- struct{}{}
						return tt.refusal
					})
				}
			})
			got := conversationPostWire(t, sock, `{"verb":"conversation.post"`+tt.fields+`}`)
			want := `{"error":"conversation.post: ` + tt.want + `"}`
			if got != want || len(calls) != tt.calls {
				t.Errorf("response = %s, calls = %d; want %s, calls %d", got, len(calls), want, tt.calls)
			}
			if strings.Contains(logs.String(), id) || strings.Contains(logs.String(), text) {
				t.Error("control logs leaked caller id or message text")
			}
		})
	}
}

func TestConversationPost_MalformedTypes(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{
		`"private-payload-sentinel"`,
		`{"conversationID":123,"text":"private-text-sentinel"}`,
		`{"conversationID":"private-id-sentinel","text":false}`,
	} {
		t.Run(payload, func(t *testing.T) {
			calls := make(chan struct{}, 2)
			_, sock := startConversationServer(t, func(s *Server) {
				s.SetConversationSubmitter(func(_, _ string) error { calls <- struct{}{}; return nil })
			})
			wire := conversationPostWire(t, sock, `{"verb":"conversation.post","conversationPost":`+payload+`}`)
			var resp Response
			if err := json.Unmarshal([]byte(wire), &resp); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(resp.Error, "decode request:") || !reflect.DeepEqual(resp, Response{Error: resp.Error}) || len(calls) != 0 || strings.Contains(wire, "private-") {
				t.Errorf("malformed response = %s, calls = %d", wire, len(calls))
			}
		})
	}
}

func TestConversationPost_ClientErrors(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, wire, want string }{
		{"absent OK", `{}`, "control: conversation.post response missing ok flag"},
		{"false OK", `{"ok":false}`, "control: conversation.post response missing ok flag"},
		{"null OK", `{"ok":null}`, "control: conversation.post response missing ok flag"},
		{"refusal", `{"error":"static refusal"}`, "static refusal"},
		{"refusal with OK", `{"ok":true,"error":"static refusal"}`, "static refusal"},
		{"malformed response", `invalid JSON`, ""},
		{"closed connection", ``, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sock := startMisbehavingServer(t, func(conn net.Conn) {
				var req Request
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if _, err := conn.Write([]byte(tt.wire + "\n")); err != nil {
					t.Error(err)
				}
			})
			err := ConversationPost(context.Background(), sock, "id", "text")
			if err == nil || (tt.want != "" && err.Error() != tt.want) {
				t.Errorf("ConversationPost = %v, want %q error", err, tt.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ConversationPost(ctx, filepath.Join(shortTempDir(t), "missing.sock"), "id", "text"); err == nil {
		t.Error("dial failure returned nil")
	}
}

func TestConversationPost_SubmitterOutsideLockAndDeadline(t *testing.T) {
	t.Parallel()
	_, sock := startConversationServer(t, func(s *Server) {
		s.handshakeTimeout = 50 * time.Millisecond
		s.SetConversationSubmitter(func(_, _ string) error {
			s.SetConversationSubmitter(nil)
			time.Sleep(100 * time.Millisecond)
			return nil
		})
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ConversationPost(ctx, sock, "id", "text"); err != nil {
		t.Fatal(err)
	}
	if err := ConversationPost(ctx, sock, "id", "text"); err == nil || err.Error() != "conversation.post: no conversation submitter configured" {
		t.Errorf("after clearing submitter: %v", err)
	}
}

func TestConversationPost_ConcurrentInstallation(t *testing.T) {
	t.Parallel()
	submit := func(_, _ string) error { return nil }
	srv, sock := startConversationServer(t, func(s *Server) { s.SetConversationSubmitter(submit) })
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			srv.SetConversationSubmitter(nil)
			srv.SetConversationSubmitter(submit)
		}
	}()
	defer wg.Wait()
	for range 20 {
		resp := channelRoundTrip(t, sock, Request{Verb: VerbConversationPost, ConversationPost: &ConversationPostPayload{ConversationID: "id", Text: "text"}})
		if !reflect.DeepEqual(resp, Response{OK: true}) && !reflect.DeepEqual(resp, Response{Error: "conversation.post: no conversation submitter configured"}) {
			t.Errorf("unexpected response: %#v", resp)
		}
	}
}

func TestConversationPost_WireCompatibility(t *testing.T) {
	t.Parallel()
	for _, wire := range []string{
		`{"verb":"conversation.post","conversationPost":{"conversationID":"opaque-id","text":"distinct text"}}`,
		`{"verb":"conversation.post","conversationPost":{"conversationID":"","text":""}}`,
		`{"verb":"channel.post","channelPost":{"name":"existing-name","text":"existing text"}}`,
	} {
		var req Request
		if err := json.Unmarshal([]byte(wire), &req); err != nil {
			t.Fatal(err)
		}
		if req.Verb == VerbConversationPost {
			want := &ConversationPostPayload{}
			if strings.Contains(wire, "opaque-id") {
				want = &ConversationPostPayload{ConversationID: "opaque-id", Text: "distinct text"}
			}
			if !reflect.DeepEqual(req.ConversationPost, want) {
				t.Errorf("decoded payload = %#v, want %#v", req.ConversationPost, want)
			}
		}
		got, err := json.Marshal(req)
		if err != nil || string(got) != wire {
			t.Errorf("remarshal = %s, %v; want %s", got, err, wire)
		}
	}
	for _, tt := range []struct {
		resp Response
		wire string
	}{
		{Response{OK: true}, `{"ok":true}`},
		{Response{Error: "channel.post: unchanged refusal"}, `{"error":"channel.post: unchanged refusal"}`},
	} {
		got, err := json.Marshal(tt.resp)
		if err != nil || string(got) != tt.wire {
			t.Errorf("response = %s, %v; want %s", got, err, tt.wire)
		}
	}
}
