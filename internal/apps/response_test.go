package apps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type responseBudget struct {
	mu            sync.Mutex
	held, ceiling int64
}

func (b *responseBudget) reserve(n int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.held+n > b.ceiling {
		return false
	}
	b.held += n
	return true
}
func (b *responseBudget) release(n int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.held -= n
	if b.held < 0 {
		panic("double release")
	}
}
func (b *responseBudget) used() int64 { b.mu.Lock(); defer b.mu.Unlock(); return b.held }

type responseSource struct {
	io.Reader
	reads, closes atomic.Int32
	closeErr      error
}

func (s *responseSource) Read(p []byte) (int, error) { s.reads.Add(1); return s.Reader.Read(p) }
func (s *responseSource) Close() error               { s.closes.Add(1); return s.closeErr }
func responseInput(body io.ReadCloser, size, limit int64, b *responseBudget) ResponseInput {
	return ResponseInput{Body: body, Size: size, Limit: limit, Status: 200, Headers: map[string]string{}, Reserve: b.reserve, Release: b.release}
}
func responseOwner(t *testing.T) (*Registry, *Owner) {
	t.Helper()
	r, o := healthFixture(t, healthReply{Body: healthyBody()})
	healthStart(t, r, o)
	healthTarget(t, o)
	return r, o
}
func responseClean(t *testing.T, s *responseSource, b *responseBudget) {
	t.Helper()
	if s.closes.Load() != 1 || b.used() != 0 {
		t.Fatalf("cleanup closes=%d held=%d", s.closes.Load(), b.used())
	}
}

func responsePrepared(t *testing.T, o *Owner, ctx context.Context, in ResponseInput) *PreparedResponse {
	t.Helper()
	p, err := o.Prepare(ctx, testHost, testApp, "1.0.0", in)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResponseStable(t *testing.T) {
	r, o := responseOwner(t)
	for _, body := range [][]byte{nil, {0, 255, 1, 2}, []byte(strings.Repeat("backend error", 6000))} {
		s := &responseSource{Reader: bytes.NewReader(body)}
		b := &responseBudget{ceiling: int64(len(body)) + 1}
		in := responseInput(s, int64(len(body)), int64(len(body)), b)
		in.Status = 503
		in.Headers["etag"] = "original"
		p := responsePrepared(t, o, context.Background(), in)
		in.Headers["etag"] = "changed"
		s.Reader = strings.NewReader("changed source")
		v := ownerRecord(t, r, testApp)
		if _, err := r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: "changed title", Desired: v.Desired, State: v.State, ActiveRelease: v.ActiveRelease, PendingRelease: "2.0.0"}); err != nil {
			t.Fatal(err)
		}
		m, err := p.Metadata()
		if err != nil || m.Status != 503 || m.Headers["etag"] != "original" || m.Size != int64(len(body)) || m.SHA256 != fmt.Sprintf("%x", sha256.Sum256(body)) {
			t.Fatalf("metadata %#v %v", m, err)
		}
		m.Headers["etag"] = "mutated copy"
		again, _ := p.Metadata()
		if again.Headers["etag"] != "original" {
			t.Fatal("aliased headers")
		}
		got, err := io.ReadAll(p)
		if err != nil || !bytes.Equal(got, body) || b.used() != int64(len(body)) {
			t.Fatalf("body %x %v held %d", got, err, b.used())
		}
		if err = p.Complete(nil); err != nil {
			t.Fatal(err)
		}
		responseClean(t, s, b)
	}
}
func TestResponseBounds(t *testing.T) {
	_, o := responseOwner(t)
	for _, tc := range []struct {
		name, body            string
		size, limit, capacity int64
		want                  error
		noRead                bool
	}{
		{"exact", "abcd", 4, 4, 5, nil, false}, {"empty", "", 0, 0, 1, nil, false},
		{"declared oversized", "abcd", 4, 3, 10, ErrResponseLimit, true},
		{"unknown exact", "abcd", -1, 4, 5, nil, false}, {"unknown oversized", "abcde", -1, 4, 5, ErrResponseLimit, false},
		{"growing", "abcde", 2, 4, 5, ErrResponseLimit, false},
		{"known refused", "abcd", 4, 4, 3, ErrResponseCapacity, true},
		{"unknown refused", "abcd", -1, 4, 0, ErrResponseCapacity, true},
		{"increment refused", strings.Repeat("x", 65536), -1, 65536, 32768, ErrResponseCapacity, false},
		{"invalid size", "", -2, 4, 10, ErrResponseMetadata, true},
		{"invalid limit", "", 0, -1, 10, ErrResponseMetadata, true},
		{"probe refused", "abcd", 4, 4, 4, ErrResponseCapacity, false},
		{"short declared", "a", 4, 4, 5, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &responseSource{Reader: strings.NewReader(tc.body)}
			b := &responseBudget{ceiling: tc.capacity}
			p, err := o.Prepare(context.Background(), testHost, testApp, "1.0.0", responseInput(s, tc.size, tc.limit, b))
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if tc.name == "increment refused" && s.reads.Load() != 1 {
				t.Fatal("read beyond reserved capacity")
			}
			if tc.noRead && s.reads.Load() != 0 {
				t.Fatal("read before rejection")
			}
			if p != nil {
				m, _ := p.Metadata()
				if m.Size != int64(len(tc.body)) || b.used() != m.Size {
					t.Fatal(m, b.used())
				}
				p.Close()
			}
			responseClean(t, s, b)
		})
	}
}
func TestResponseMetadata(t *testing.T) {
	_, o := responseOwner(t)
	for _, tc := range []struct {
		name    string
		status  int
		headers map[string]string
		valid   bool
	}{
		{"empty", 200, nil, true}, {"last status", 599, map[string]string{}, true},
		{"low", 199, nil, false}, {"high", 600, nil, false}, {"case", 200, map[string]string{"ETag": "x"}, false},
		{"forbidden", 200, map[string]string{"location": "/private"}, false},
		{"utf8", 200, map[string]string{"etag": string([]byte{255})}, false}, {"control", 200, map[string]string{"etag": "a\nb"}, false},
		{"C1", 200, map[string]string{"etag": "\u0080"}, false},
		{"value edge", 200, map[string]string{"etag": strings.Repeat("é", 512)}, true}, {"value overflow", 200, map[string]string{"etag": strings.Repeat("é", 513)}, false},
		{"json edge", 200, map[string]string{"etag": strings.Repeat("a", 1024), "content-type": strings.Repeat("b", 1024), "cache-control": strings.Repeat("c", 1024), "last-modified": strings.Repeat("d", 957)}, true},
		{"json overflow", 200, map[string]string{"etag": strings.Repeat("a", 1024), "content-type": strings.Repeat("b", 1024), "cache-control": strings.Repeat("c", 1024), "last-modified": strings.Repeat("d", 958)}, false},
		{"json escaping", 200, map[string]string{"etag": strings.Repeat("\"", 1024), "cache-control": strings.Repeat("\\", 1024)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &responseSource{Reader: strings.NewReader("")}
			b := &responseBudget{ceiling: 1}
			in := responseInput(s, 0, 0, b)
			in.Status = tc.status
			in.Headers = tc.headers
			p, err := o.Prepare(context.Background(), testHost, testApp, "1.0.0", in)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				p.Close()
			} else if !errors.Is(err, ErrResponseMetadata) || s.reads.Load() != 0 {
				t.Fatal(err, s.reads.Load())
			}
			responseClean(t, s, b)
		})
	}
}
func TestResponseResolution(t *testing.T) {
	r, o := responseOwner(t)
	for _, tc := range []struct {
		host, app, release string
		want               error
	}{
		{testHost, "unknown", "1.0.0", ErrNotFound}, {"foreign", testApp, "2.0.0", ErrNotFound}, {testHost, testApp, "2.0.0", ErrReleaseChanged},
	} {
		s := &responseSource{Reader: strings.NewReader("secret")}
		b := &responseBudget{ceiling: 100}
		_, err := o.Prepare(context.Background(), tc.host, tc.app, tc.release, responseInput(s, 6, 6, b))
		if !errors.Is(err, tc.want) || s.reads.Load() != 0 {
			t.Fatal(err)
		}
		responseClean(t, s, b)
	}
	if err := o.Stop(testApp); err != nil {
		t.Fatal(err)
	}
	s := &responseSource{Reader: strings.NewReader("secret")}
	b := &responseBudget{ceiling: 100}
	_, err := o.Prepare(context.Background(), testHost, testApp, "1.0.0", responseInput(s, 6, 6, b))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	responseClean(t, s, b)
	if _, err = r.Remove(testApp); err != nil {
		t.Fatal(err)
	}
	s = &responseSource{Reader: strings.NewReader("")}
	_, err = o.Prepare(context.Background(), testHost, testApp, "1.0.0", responseInput(s, 0, 0, b))
	if !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	responseClean(t, s, b)
}

type responseBlocked struct {
	entered, closed chan struct{}
	once            sync.Once
	closes          atomic.Int32
}

func (s *responseBlocked) Read([]byte) (int, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.closed
	return 0, errors.New("private endpoint and content")
}
func (s *responseBlocked) Close() error {
	if s.closes.Add(1) == 1 {
		close(s.closed)
	}
	return nil
}
func TestResponseCancellation(t *testing.T) {
	_, o := responseOwner(t)
	ctx, cancel := context.WithCancel(context.Background())
	s := &responseBlocked{entered: make(chan struct{}), closed: make(chan struct{})}
	b := &responseBudget{ceiling: 100}
	done := make(chan error, 1)
	go func() {
		p, err := o.Prepare(ctx, testHost, testApp, "1.0.0", responseInput(s, -1, 99, b))
		if p != nil {
			p.Close()
		}
		done <- err
	}()
	<-s.entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if b.used() != 0 || s.closes.Load() != 1 {
		t.Fatal("blocked source leaked")
	}
	ctx, cancel = context.WithCancel(context.Background())
	ordinary := &responseSource{Reader: strings.NewReader("retained")}
	p := responsePrepared(t, o, ctx, responseInput(ordinary, 8, 8, b))
	cancel()
	healthWait(t, func() bool { return b.used() == 0 })
	if _, err := p.Metadata(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	p.Close()
	responseClean(t, ordinary, b)
}
func TestResponseRetirement(t *testing.T) {
	for _, event := range []string{"remove", "release", "stop", "exit", "retire"} {
		t.Run(event, func(t *testing.T) {
			r, o := responseOwner(t)
			g := healthTarget(t, o).generation
			b := &responseBudget{ceiling: 100}
			s := &responseSource{Reader: strings.NewReader("retained")}
			p := responsePrepared(t, o, context.Background(), responseInput(s, 8, 8, b))
			var err error
			buf := make([]byte, 1)
			if _, err = p.Read(buf); err != nil {
				t.Fatal(err)
			}
			blocked := &responseBlocked{entered: make(chan struct{}), closed: make(chan struct{})}
			stagingBudget := &responseBudget{ceiling: 100}
			done := make(chan error, 1)
			go func() {
				q, e := o.Prepare(context.Background(), testHost, testApp, "1.0.0", responseInput(blocked, -1, 99, stagingBudget))
				if q != nil {
					q.Close()
				}
				done <- e
			}()
			<-blocked.entered
			switch event {
			case "remove":
				_, err = r.Remove(testApp)
			case "release":
				v := ownerRecord(t, r, testApp)
				_, err = r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: v.Title, Desired: v.Desired, State: v.State, ActiveRelease: "2.0.0"})
			case "stop":
				err = o.Stop(testApp)
				if err == nil {
					healthStart(t, r, o)
					healthTarget(t, o)
				}
			case "exit":
				err = os.WriteFile(filepath.Join(r.root, testApp, "data/exit"), nil, 0600)
			case "retire":
				g.cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = <-done; err == nil {
				t.Fatal("retired staging succeeded")
			}
			healthWait(t, func() bool { return b.used() == 0 })
			if _, err := p.Metadata(); err == nil {
				t.Fatal("retired metadata usable")
			}
			if n, err := p.Read(buf); n != 0 || err == nil {
				t.Fatal("retired bytes usable")
			}
			p.Close()
			responseClean(t, s, b)
			if stagingBudget.used() != 0 || blocked.closes.Load() != 1 {
				t.Fatal("staging leaked")
			}
		})
	}
}
func TestResponseDisposalRace(t *testing.T) {
	_, o := responseOwner(t)
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		s := &responseSource{Reader: strings.NewReader(strings.Repeat("x", 100))}
		b := &responseBudget{ceiling: 101}
		p := responsePrepared(t, o, ctx, responseInput(s, 100, 100, b))
		var wg sync.WaitGroup
		for _, f := range []func(){func() { io.ReadAll(p) }, func() { p.Metadata() }, func() { p.Complete(errors.New("secret delivery path")) }, func() { p.Close() }, cancel} {
			wg.Add(1)
			go func(f func()) { defer wg.Done(); f() }(f)
		}
		wg.Wait()
		p.Close()
		responseClean(t, s, b)
	}
}
func TestResponseIO(t *testing.T) {
	_, o := responseOwner(t)
	for _, failure := range []int{0, 1, 2, 3} {
		s := &responseSource{Reader: io.MultiReader(strings.NewReader("abc"), responseErrorReader{})}
		if failure == 1 {
			s.Reader = strings.NewReader("abc")
			s.closeErr = errors.New("secret native path")
		}
		if failure == 3 {
			s.Reader = responseReadFunc(func([]byte) (int, error) { return 0, nil })
		}
		if failure == 2 {
			s.Reader = responseReadFunc(func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF })
		}
		b := &responseBudget{ceiling: 100}
		p, err := o.Prepare(context.Background(), testHost, testApp, "1.0.0", responseInput(s, 3, 99, b))
		if p != nil || !errors.Is(err, ErrResponseIO) || strings.Contains(err.Error(), "secret") {
			t.Fatal(p, err)
		}
		responseClean(t, s, b)
	}
	s := &responseSource{Reader: strings.NewReader("abc")}
	b := &responseBudget{ceiling: 100}
	p := responsePrepared(t, o, context.Background(), responseInput(s, 3, 99, b))
	if err := p.Complete(errors.New("secret endpoint")); !errors.Is(err, ErrResponseIO) || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	responseClean(t, s, b)
}

type responseErrorReader struct{}

func (responseErrorReader) Read([]byte) (int, error) { return 0, errors.New("secret native path") }
