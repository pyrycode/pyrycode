package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func testConversationString(s string) *string { return &s }

func startConversationServer(t *testing.T, install func(*Server)) (*Server, string) {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "p.sock")
	srv := NewServer(sock, &fakeResolver{sess: &fakeSession{}}, nil, nil, nil, nil)
	install(srv)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Serve did not stop")
		}
	})
	return srv, sock
}

func TestConversationNew_Forwarding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw, wire string
		want            ConversationPayload
	}{
		{"default", `{"cwd":"relative/link/../project"}`, `{"cwd":"relative/link/../project"}`, ConversationPayload{Cwd: "relative/link/../project"}},
		{"null settings", `{"cwd":"/p","model":null,"effort":null}`, `{"cwd":"/p"}`, ConversationPayload{Cwd: "/p"}},
		{"chat", `{"cwd":"/p","type":"chat","model":"","effort":""}`, `{"cwd":"/p","type":"chat","model":"","effort":""}`, ConversationPayload{Cwd: "/p", Type: testConversationString("chat"), Model: testConversationString(""), Effort: testConversationString("")}},
		{"channel", `{"cwd":"/private/link/./p","name":" chosen name ","type":"channel","model":"future-model","effort":"future-effort"}`, `{"cwd":"/private/link/./p","name":" chosen name ","type":"channel","model":"future-model","effort":"future-effort"}`, ConversationPayload{Cwd: "/private/link/./p", Name: " chosen name ", Type: testConversationString("channel"), Model: testConversationString("future-model"), Effort: testConversationString("future-effort")}},
		{"only model", `{"cwd":"/p","model":""}`, `{"cwd":"/p","model":""}`, ConversationPayload{Cwd: "/p", Model: testConversationString("")}},
		{"only effort", `{"cwd":"/p","effort":""}`, `{"cwd":"/p","effort":""}`, ConversationPayload{Cwd: "/p", Effort: testConversationString("")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req Request
			if err := json.Unmarshal([]byte(`{"verb":"conversation.new","conversation":`+tt.raw+`}`), &req); err != nil {
				t.Fatal(err)
			}
			if req.Verb != VerbConversationNew || !reflect.DeepEqual(req.Conversation, &tt.want) {
				t.Fatalf("decoded request = %#v, payload = %#v, want %#v", req, req.Conversation, tt.want)
			}
			wire, err := json.Marshal(req)
			if err != nil || string(wire) != `{"verb":"conversation.new","conversation":`+tt.wire+`}` {
				t.Fatalf("wire = %s, error = %v", wire, err)
			}
			calls := make(chan ConversationPayload, 2)
			_, sock := startConversationServer(t, func(s *Server) {
				s.SetConversationCreator(func(cwd, name, kind string, model, effort *string) (string, error) {
					calls <- ConversationPayload{Cwd: cwd, Name: name, Type: &kind, Model: model, Effort: effort}
					return "created-id", nil
				})
			})
			id, err := ConversationNew(context.Background(), sock, *req.Conversation)
			if id != "created-id" || err != nil {
				t.Fatalf("ConversationNew = %q, %v", id, err)
			}
			if len(calls) != 1 {
				t.Fatalf("creator calls = %d, want 1", len(calls))
			}
			want := tt.want
			if want.Type == nil {
				want.Type = testConversationString("chat")
			}
			if got := <-calls; !reflect.DeepEqual(got, want) {
				t.Errorf("creator got %#v, want %#v", got, want)
			}
		})
	}
}

func TestConversationNew_Refusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw, want string
		creator         bool
		id              string
		err             error
		calls           int
	}{
		{"unwired absent", ``, "conversation.new: no conversation creator configured", false, "", nil, 0},
		{"unwired invalid", `,"conversation":{"type":"secret-type"}`, "conversation.new: no conversation creator configured", false, "", nil, 0},
		{"unwired valid", `,"conversation":{"cwd":"/private/p"}`, "conversation.new: no conversation creator configured", false, "", nil, 0},
		{"absent", ``, "conversation.new: missing cwd", true, "", nil, 0},
		{"null", `,"conversation":null`, "conversation.new: missing cwd", true, "", nil, 0},
		{"missing cwd", `,"conversation":{"name":"secret-name"}`, "conversation.new: missing cwd", true, "", nil, 0},
		{"empty cwd", `,"conversation":{"cwd":""}`, "conversation.new: missing cwd", true, "", nil, 0},
		{"invalid type", `,"conversation":{"cwd":"/private/p","type":"secret-type"}`, "conversation.new: invalid type", true, "", nil, 0},
		{"empty type", `,"conversation":{"cwd":"/private/p","type":""}`, "conversation.new: invalid type", true, "", nil, 0},
		{"creator refused", `,"conversation":{"cwd":"/private/p","name":"secret-name","model":"secret-model"}`, "conversation.new: settings rejected", true, "discard-this-id", errors.New("settings rejected"), 1},
		{"empty id", `,"conversation":{"cwd":"/private/p"}`, "conversation.new: empty conversation id", true, "", nil, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := make(chan struct{}, 2)
			_, sock := startConversationServer(t, func(s *Server) {
				if tt.creator {
					s.SetConversationCreator(func(_, _, _ string, _, _ *string) (string, error) {
						calls <- struct{}{}
						return tt.id, tt.err
					})
				}
			})
			conn, err := net.Dial("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			if _, err := conn.Write([]byte(`{"verb":"conversation.new"` + tt.raw + "}\n")); err != nil {
				t.Fatal(err)
			}
			var resp Response
			if err := json.NewDecoder(conn).Decode(&resp); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(resp, Response{Error: tt.want}) || len(calls) != tt.calls {
				t.Errorf("response = %#v, calls = %d; want error %q, calls %d", resp, len(calls), tt.want, tt.calls)
			}
		})
	}
}

func TestConversationNew_ClientErrors(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, wire, want string }{
		{"absent result", `{}`, "control: empty conversation.new response"},
		{"empty id", `{"conversationNew":{"conversationID":""}}`, "control: empty conversation.new response"},
		{"refusal", `{"error":"static refusal"}`, "static refusal"},
		{"refusal with id", `{"error":"static refusal","conversationNew":{"conversationID":"discard"}}`, "static refusal"},
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
			id, err := ConversationNew(context.Background(), sock, ConversationPayload{Cwd: "/p"})
			if id != "" || err == nil || (tt.want != "" && err.Error() != tt.want) {
				t.Errorf("ConversationNew = %q, %v; want empty id and %q error", id, err, tt.want)
			}
		})
	}
	t.Run("dial failure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		id, err := ConversationNew(ctx, filepath.Join(shortTempDir(t), "missing.sock"), ConversationPayload{Cwd: "/p"})
		if id != "" || err == nil {
			t.Fatalf("ConversationNew = %q, %v", id, err)
		}
	})
}

func TestConversationNew_CreatorOutsideLockAndDeadline(t *testing.T) {
	t.Parallel()
	_, sock := startConversationServer(t, func(s *Server) {
		s.handshakeTimeout = 50 * time.Millisecond
		s.SetConversationCreator(func(_, _, _ string, _, _ *string) (string, error) {
			s.SetConversationCreator(nil)
			time.Sleep(100 * time.Millisecond)
			return "id-after-handshake", nil
		})
	})
	id, err := ConversationNew(context.Background(), sock, ConversationPayload{Cwd: "/p"})
	if err != nil || id != "id-after-handshake" {
		t.Fatalf("ConversationNew = %q, %v", id, err)
	}
	resp := channelRoundTrip(t, sock, Request{Verb: VerbConversationNew, Conversation: &ConversationPayload{Cwd: "/p"}})
	if resp.Error != "conversation.new: no conversation creator configured" {
		t.Errorf("after clearing creator: %#v", resp)
	}
}

func TestConversationNew_ConcurrentInstallation(t *testing.T) {
	t.Parallel()
	create := func(_, _, _ string, _, _ *string) (string, error) { return "id", nil }
	srv, sock := startConversationServer(t, func(s *Server) { s.SetConversationCreator(create) })
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			srv.SetConversationCreator(nil)
			srv.SetConversationCreator(create)
		}
	}()
	for range 20 {
		resp := channelRoundTrip(t, sock, Request{Verb: VerbConversationNew, Conversation: &ConversationPayload{Cwd: "/p"}})
		if resp.Error != "conversation.new: no conversation creator configured" && (resp.ConversationNew == nil || resp.ConversationNew.ConversationID != "id") {
			t.Errorf("unexpected response: %#v", resp)
		}
	}
	wg.Wait()
}

func TestConversationNew_WireCompatibility(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		value any
		want  string
	}{
		{Request{Verb: VerbChannelNew, Channel: &ChannelPayload{Cwd: "/p"}}, `{"verb":"channel.new","channel":{"cwd":"/p"}}`},
		{Request{Verb: VerbChannelNew, Channel: &ChannelPayload{Cwd: "/p", Name: "name"}}, `{"verb":"channel.new","channel":{"cwd":"/p","name":"name"}}`},
		{Response{ChannelNew: &ChannelNewResult{ConversationID: "old-id"}}, `{"channelNew":{"conversationID":"old-id"}}`},
		{Response{ConversationNew: &ConversationNewResult{ConversationID: "new-id"}}, `{"conversationNew":{"conversationID":"new-id"}}`},
	} {
		got, err := json.Marshal(tt.value)
		if err != nil || string(got) != tt.want {
			t.Errorf("Marshal = %s, %v; want %s", got, err, tt.want)
		}
	}
}
