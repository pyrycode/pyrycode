package apps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type apiReply struct {
	Status   int
	Headers  map[string]string
	Body     []byte
	Stall    string
	Truncate bool
}
type apiCapture struct {
	URI, Method string
	Headers     http.Header
	Body        []byte
}

// apiHelper extends the existing generation-owned health service, not its lifecycle.
func apiHelper(w http.ResponseWriter, req *http.Request, data string) {
	body, _ := io.ReadAll(req.Body)
	capture, _ := json.Marshal(apiCapture{req.RequestURI, req.Method, req.Header, body})
	_ = os.WriteFile(filepath.Join(data, "request"), capture, 0600)
	f, err := os.OpenFile(filepath.Join(data, "requests"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err == nil {
		_, _ = f.WriteString("request\n")
		_ = f.Close()
	}
	if req.URL.Path == "/api/value" {
		if req.Method == "POST" {
			_ = os.WriteFile(filepath.Join(data, "value"), body, 0600)
			w.WriteHeader(204)
		} else {
			value, _ := os.ReadFile(filepath.Join(data, "value"))
			_, _ = w.Write(value)
		}
		return
	}
	raw, _ := os.ReadFile(filepath.Join(data, "api"))
	reply := apiReply{Status: 200}
	_ = json.Unmarshal(raw, &reply)
	for k, v := range reply.Headers {
		w.Header().Set(k, v)
	}
	if reply.Stall == "headers" {
		<-req.Context().Done()
		return
	}
	if reply.Stall == "disconnect" {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	if reply.Truncate {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_, _ = fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\nx")
			_ = conn.Close()
		}
		return
	}
	w.WriteHeader(reply.Status)
	if reply.Stall == "body" {
		_, _ = w.Write([]byte("prefix"))
		w.(http.Flusher).Flush()
		_ = os.WriteFile(filepath.Join(data, "stalled"), nil, 0600)
		<-req.Context().Done()
		return
	}
	_, _ = w.Write(reply.Body)
}
func apiWrite(t *testing.T, r *Registry, reply apiReply) {
	t.Helper()
	raw, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(r.root, testApp, "data/api"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func apiRequest(b *responseBudget) APIRequest {
	return APIRequest{Method: "GET", Path: "/api/test", Headers: map[string]string{}, Reserve: b.reserve, Release: b.release}
}
func apiOwner(t *testing.T) (*Registry, *Owner) {
	t.Helper()
	r, o := healthFixture(t, healthReply{API: true, Body: healthyBody()})
	healthStart(t, r, o)
	healthTarget(t, o)
	return r, o
}
func apiCount(r *Registry) int {
	raw, _ := os.ReadFile(filepath.Join(r.root, testApp, "data/requests"))
	return strings.Count(string(raw), "request\n")
}

func TestAPIForwarding(t *testing.T) {
	r, o := apiOwner(t)
	for _, method := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		for _, body := range [][]byte{nil, {0, 255, 1}, []byte("x=a%2Fb&x="), []byte(`{"binary":"value"}`)} {
			if (method == "GET" || method == "HEAD") && len(body) > 0 {
				continue
			}
			b := &responseBudget{ceiling: 100}
			in := apiRequest(b)
			in.Method = method
			in.Path = "/api/%e2%82%Ac/%41"
			in.Query = "x=%2f&x=&empty&x=%2F"
			in.Body = body
			in.Headers = map[string]string{"accept": "application/json", "content-type": "application/octet-stream", "if-none-match": "opaque"}
			apiWrite(t, r, apiReply{Status: 200, Body: []byte("ok")})
			p, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in)
			if err != nil {
				t.Fatal(err)
			}
			p.Close()
			raw, err := os.ReadFile(filepath.Join(r.root, testApp, "data/request"))
			if err != nil {
				t.Fatal(err)
			}
			var got apiCapture
			if err = json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.URI != in.Path+"?"+in.Query || got.Method != method || !bytes.Equal(got.Body, body) {
				t.Fatalf("forwarded %#v", got)
			}
			for k, v := range in.Headers {
				if got.Headers.Get(k) != v {
					t.Fatal(k, got.Headers)
				}
			}
			for _, key := range []string{"Cookie", "Authorization", "Accept-Encoding", "Origin", "Referer"} {
				if got.Headers.Get(key) != "" {
					t.Fatal("added forbidden header", key)
				}
			}
			if b.used() != 0 {
				t.Fatal("capacity retained")
			}
		}
	}
	b := &responseBudget{ceiling: 100}
	in := apiRequest(b)
	in.Path = "/api/value"
	in.Method = "POST"
	in.Body = []byte(`{"period":"24h"}`)
	p, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := p.Metadata()
	if m.Status != 204 || m.Size != 0 || m.SHA256 != fmt.Sprintf("%x", sha256.Sum256(nil)) {
		t.Fatal(m)
	}
	p.Close()
	in.Method = "GET"
	in.Body = nil
	p, err = o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(p)
	if err != nil || string(got) != `{"period":"24h"}` {
		t.Fatal(string(got), err)
	}
	if err = p.Complete(nil); err != nil {
		t.Fatal(err)
	}
	if b.used() != 0 {
		t.Fatal("capacity retained")
	}
}
func TestAPIRejectRequests(t *testing.T) {
	r, o := apiOwner(t)
	b := &responseBudget{ceiling: 2 << 20}
	base := apiRequest(b)
	for _, path := range []string{"http://127.0.0.1/api/x", "//api/x", "/api", "/other/x", "/api//x", "/api/../x", "/api/%2e./x", "/api/%2F", "/api/%5c", "/api/%25", "/api/%00", "/api/%7f", "/api/%C2%80", "/api/%ff", "/api/%", "/api/é", "/api/x?y", "/api/x#y", "/api/\\x", "/api/\"x", "/api/{x}", "/api/x\n", "/api/" + strings.Repeat("x", 2044)} {
		in := base
		in.Path = path
		_, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in)
		if !errors.Is(err, ErrAPIRequest) {
			t.Fatalf("path %q: %v", path, err)
		}
	}
	for _, query := range []string{"?x", "x#y", "x\n", "é", "%", strings.Repeat("x", 1025)} {
		in := base
		in.Query = query
		_, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in)
		if !errors.Is(err, ErrAPIRequest) {
			t.Fatalf("query %q: %v", query, err)
		}
	}
	for _, method := range []string{"get", "TRACE", "CONNECT", "", "POST\r\n"} {
		in := base
		in.Method = method
		_, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in)
		if !errors.Is(err, ErrAPIRequest) {
			t.Fatal(method, err)
		}
	}
	for _, headers := range []map[string]string{{"Accept": "x"}, {"authorization": "x"}, {"cookie": "x"}, {"host": "x"}, {"origin": "x"}, {"referer": "x"}, {"connection": "upgrade"}, {"accept": "x\r\n"}, {"accept": "\u0080"}, {"accept": string([]byte{255})}, {"accept": strings.Repeat("x", 1025)}, {"accept": strings.Repeat("\"", 1024), "content-type": strings.Repeat("\\", 1024)}} {
		in := base
		in.Headers = headers
		_, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in)
		if !errors.Is(err, ErrAPIRequest) {
			t.Fatal(headers, err)
		}
	}
	for _, method := range []string{"GET", "HEAD"} {
		in := base
		in.Method = method
		in.Body = []byte("x")
		_, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in)
		if !errors.Is(err, ErrAPIRequest) {
			t.Fatal(err)
		}
	}
	in := base
	in.Method = "POST"
	in.Body = make([]byte, 32769)
	if _, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in); !errors.Is(err, ErrAPILimit) {
		t.Fatal(err)
	}
	if apiCount(r) != 0 || b.used() != 0 {
		t.Fatal("invalid request reached backend")
	}
	// Exact byte limits, including JSON escaping in header serialization.
	in.Body = in.Body[:32768]
	in.Path = "/api/" + strings.Repeat("x", 2043)
	in.Query = strings.Repeat("x", 1024)
	in.Headers = map[string]string{"accept": strings.Repeat("\"", 1024), "content-type": strings.Repeat("x", 1024), "if-none-match": strings.Repeat("x", 974)}
	raw, _ := json.Marshal(in.Headers)
	if len(raw) != 4096 {
		t.Fatal("wrong edge", len(raw))
	}
	p, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	in.Headers["if-none-match"] += "x"
	if _, err = o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in); !errors.Is(err, ErrAPIRequest) {
		t.Fatal(err)
	}
	if apiCount(r) != 1 {
		t.Fatal("request replay/overflow forwarding")
	}
}
func TestAPIResolution(t *testing.T) {
	r, o := apiOwner(t)
	b := &responseBudget{ceiling: 100}
	in := apiRequest(b)
	in.Method = "POST"
	in.Body = []byte("mutation")
	for _, tc := range []struct {
		host, app, release string
		want               error
	}{{"foreign", testApp, "1.0.0", ErrNotFound}, {testHost, "unknown", "1.0.0", ErrNotFound}, {testHost, testApp, "2.0.0", ErrReleaseChanged}} {
		_, err := o.ForwardAPI(context.Background(), tc.host, tc.app, tc.release, in)
		if !errors.Is(err, tc.want) {
			t.Fatal(err)
		}
	}
	if err := o.Stop(testApp); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := r.Remove(testApp); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if apiCount(r) != 0 {
		t.Fatal("invalid identity reached backend")
	}
}
func TestAPIResponses(t *testing.T) {
	r, o, _ := apiResponseOwner(t)
	for _, tc := range []struct {
		name     string
		reply    apiReply
		method   string
		capacity int64
		want     error
		empty    bool
	}{
		{"binary", apiReply{Status: 200, Body: []byte{0, 255, 1}}, "GET", 2 << 20, nil, false},
		{"redirect", apiReply{Status: 302, Headers: map[string]string{"Location": "/api/elsewhere"}, Body: []byte("redirect")}, "GET", 100, nil, false},
		{"error", apiReply{Status: 503, Body: []byte("error")}, "GET", 100, nil, false},
		{"last status", apiReply{Status: 599}, "GET", 100, nil, false},
		{"head", apiReply{Status: 200, Body: []byte("ignored")}, "HEAD", 1, nil, true},
		{"204", apiReply{Status: 204}, "GET", 1, nil, true}, {"205", apiReply{Status: 205, Body: []byte("ignored")}, "GET", 1, nil, true}, {"304", apiReply{Status: 304}, "GET", 1, nil, true},
		{"upgrade", apiReply{Status: 101}, "GET", 100, ErrResponseMetadata, false}, {"unsupported", apiReply{Status: 600}, "GET", 100, ErrResponseMetadata, false},
		{"encoding", apiReply{Status: 200, Headers: map[string]string{"Content-Encoding": "gzip"}}, "GET", 100, ErrResponseMetadata, false},
		{"metadata", apiReply{Status: 200, Headers: map[string]string{"ETag": strings.Repeat("x", 1025)}}, "GET", 100, ErrResponseMetadata, false},
		{"control", apiReply{Status: 200, Headers: map[string]string{"ETag": "\u0080"}}, "GET", 100, ErrResponseMetadata, false},
		{"exact", apiReply{Status: 200, Body: bytes.Repeat([]byte("x"), 1048576)}, "GET", 1048577, nil, false},
		{"overflow", apiReply{Status: 200, Body: bytes.Repeat([]byte("x"), 1048577)}, "GET", 2 << 20, ErrResponseLimit, false},
		{"capacity", apiReply{Status: 200, Body: []byte("no room")}, "GET", 0, ErrResponseCapacity, false},
		{"truncated", apiReply{Truncate: true}, "GET", 100, ErrResponseIO, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := apiCount(r)
			apiWrite(t, r, tc.reply)
			b := &responseBudget{ceiling: tc.capacity}
			in := apiRequest(b)
			in.Method = tc.method
			started := time.Now()
			p, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", in)
			if p != nil {
				defer p.Close()
			}
			if !errors.Is(err, tc.want) {
				_, lookupErr := o.Lookup(testHost, testApp)
				v := ownerRecord(t, r, testApp)
				t.Fatalf("got %v want %v; elapsed=%s lookup=%v state=%s requests=%d budget=%d", err, tc.want, time.Since(started), lookupErr, v.State, apiCount(r)-before, b.used())
			}
			if p != nil {
				m, err := p.Metadata()
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(p)
				expected := tc.reply.Body
				if tc.empty {
					expected = nil
				}
				if err != nil || m.Status != tc.reply.Status || m.Size != int64(len(expected)) || m.SHA256 != fmt.Sprintf("%x", sha256.Sum256(expected)) || !bytes.Equal(body, expected) {
					t.Fatal(m, len(body), err)
				}
				if _, ok := m.Headers["location"]; ok {
					t.Fatal("location leaked")
				}
				p.Close()
				p.Close()
			}
			if b.used() != 0 || apiCount(r) != before+1 {
				t.Fatal("cleanup/replay", b.used(), apiCount(r)-before)
			}
		})
	}
	apiWrite(t, r, apiReply{Status: 200, Headers: map[string]string{"Content-Type": "application/octet-stream", "Cache-Control": "no-store", "ETag": "opaque", "Last-Modified": "yesterday", "Content-Encoding": "identity", "Set-Cookie": "secret", "Access-Control-Allow-Origin": "*", "X-Private": "secret"}})
	b := &responseBudget{ceiling: 100}
	p, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", apiRequest(b))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	m, _ := p.Metadata()
	if len(m.Headers) != 4 || m.Headers["content-type"] != "application/octet-stream" || m.Headers["etag"] != "opaque" {
		t.Fatal(m)
	}
}
func TestAPICancellation(t *testing.T) {
	for _, phase := range []string{"headers", "body"} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprint(phase, deadline), func(t *testing.T) {
				r, o := apiOwner(t)
				apiWrite(t, r, apiReply{Status: 200, Stall: phase})
				b := &responseBudget{ceiling: 2 << 20}
				in := apiRequest(b)
				in.Method = "POST"
				in.Body = []byte("mutation")
				ctx, cancel := context.WithCancel(context.Background())
				want := error(context.Canceled)
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
					want = context.DeadlineExceeded
				}
				defer cancel()
				done := make(chan error, 1)
				go func() {
					p, err := o.ForwardAPI(ctx, testHost, testApp, "1.0.0", in)
					if p != nil {
						p.Close()
					}
					done <- err
				}()
				if phase == "body" {
					waitFile(t, filepath.Join(r.root, testApp, "data/stalled"))
				} else {
					healthWait(t, func() bool { return apiCount(r) == 1 })
				}
				if !deadline {
					cancel()
				}
				select {
				case err := <-done:
					if !errors.Is(err, want) {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("cancellation hung")
				}
				if apiCount(r) != 1 || b.used() != 0 {
					t.Fatal("mutation replay or capacity leak")
				}
			})
		}
	}
}
func TestAPIRetirement(t *testing.T) {
	for _, event := range []string{"remove", "release", "stop", "exit"} {
		for _, phase := range []string{"headers", "body"} {
			t.Run(event+"/"+phase, func(t *testing.T) {
				r, o := apiOwner(t)
				b := &responseBudget{ceiling: 2 << 20}
				apiWrite(t, r, apiReply{Status: 200, Body: []byte("retained")})
				p, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", apiRequest(b))
				if err != nil {
					t.Fatal(err)
				}
				defer p.Close()
				buf := make([]byte, 1)
				if _, err = p.Read(buf); err != nil {
					t.Fatal(err)
				}
				apiWrite(t, r, apiReply{Status: 200, Stall: phase})
				staging := &responseBudget{ceiling: 2 << 20}
				done := make(chan error, 1)
				go func() {
					q, err := o.ForwardAPI(context.Background(), testHost, testApp, "1.0.0", apiRequest(staging))
					if q != nil {
						q.Close()
					}
					done <- err
				}()
				if phase == "body" {
					waitFile(t, filepath.Join(r.root, testApp, "data/stalled"))
				} else {
					healthWait(t, func() bool { return apiCount(r) == 2 })
				}
				switch event {
				case "remove":
					_, err = r.Remove(testApp)
				case "release":
					v := ownerRecord(t, r, testApp)
					_, err = r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: v.Title, Desired: v.Desired, State: v.State, ActiveRelease: "2.0.0"})
				case "stop":
					err = o.Stop(testApp)
				case "exit":
					err = os.WriteFile(filepath.Join(r.root, testApp, "data/exit"), nil, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("retired staging succeeded")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("retirement hung")
				}
				healthWait(t, func() bool { return b.used() == 0 })
				if _, err = p.Metadata(); err == nil {
					t.Fatal("retired metadata")
				}
				if n, err := p.Read(buf); n != 0 || err == nil {
					t.Fatal("retired bytes")
				}
				if staging.used() != 0 || apiCount(r) != 2 {
					t.Fatal("leak or replay")
				}
			})
		}
	}
}
func TestAPIEmptyReplacement(t *testing.T) {
	for _, method := range []string{"GET", "HEAD"} {
		t.Run(method, func(t *testing.T) {
			r, o := apiOwner(t)
			apiWrite(t, r, apiReply{Status: 204})
			target := healthTarget(t, o)
			req, _ := http.NewRequest(method, "http://unused/api/test", nil)
			resp, err := target.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if err = o.Stop(testApp); err != nil {
				t.Fatal(err)
			}
			healthStart(t, r, o)
			healthTarget(t, o)
			b := &responseBudget{ceiling: 1}
			in := apiRequest(b)
			in.Method = method
			p, err := o.prepareAPI(context.Background(), testHost, testApp, "1.0.0", target, in, resp)
			if p != nil || !errors.Is(err, ErrUnavailable) || b.used() != 0 || apiCount(r) != 1 {
				t.Fatal("rebound empty result", err, b.used())
			}
		})
	}
}
