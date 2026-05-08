package update

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFetcher_FetchLatestRelease_OK(t *testing.T) {
	t.Parallel()

	const wantUA = "pyry/v0.9.1"
	const wantPath = "/repos/pyrycode/pyrycode/releases/latest"
	canned := []byte(`{"tag_name":"v0.9.1"}`)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		if got := r.Header.Get("User-Agent"); got != wantUA {
			t.Errorf("User-Agent = %q, want %q", got, wantUA)
		}
		_, _ = w.Write(canned)
	}))
	t.Cleanup(ts.Close)

	f := Fetcher{BaseURL: ts.URL, UserAgent: wantUA}
	got, err := f.FetchLatestRelease(context.Background(), "pyrycode/pyrycode")
	if err != nil {
		t.Fatalf("FetchLatestRelease: %v", err)
	}
	if !bytes.Equal(got, canned) {
		t.Errorf("body = %q, want %q", got, canned)
	}
}

func TestFetcher_FetchAsset_OK(t *testing.T) {
	t.Parallel()

	want := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "pyry/test" {
			t.Errorf("User-Agent = %q, want %q", got, "pyry/test")
		}
		_, _ = w.Write(want)
	}))
	t.Cleanup(ts.Close)

	f := Fetcher{UserAgent: "pyry/test"}
	got, err := f.FetchAsset(context.Background(), ts.URL+"/blob")
	if err != nil {
		t.Fatalf("FetchAsset: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("body = %v, want %v", got, want)
	}
}

func TestFetcher_DefaultUserAgent(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "pyry/dev" {
			t.Errorf("User-Agent = %q, want %q", got, "pyry/dev")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(ts.Close)

	f := Fetcher{}
	if _, err := f.FetchAsset(context.Background(), ts.URL); err != nil {
		t.Fatalf("FetchAsset: %v", err)
	}
}

func TestFetcher_NonOKStatus(t *testing.T) {
	t.Parallel()

	for _, code := range []int{404, 500, 503} {
		code := code
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			t.Parallel()

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				_, _ = w.Write([]byte("body content"))
			}))
			t.Cleanup(ts.Close)

			f := Fetcher{BaseURL: ts.URL}
			_, err := f.FetchAsset(context.Background(), ts.URL+"/x")
			if err == nil {
				t.Fatalf("FetchAsset: got nil error, want non-2xx error")
			}
			msg := err.Error()
			if !strings.Contains(msg, strconv.Itoa(code)) {
				t.Errorf("error %q does not contain status code %d", msg, code)
			}
			if !strings.Contains(msg, ts.URL) {
				t.Errorf("error %q does not contain URL %q", msg, ts.URL)
			}
		})
	}
}

func TestFetcher_ContextCancelled(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	f := Fetcher{BaseURL: ts.URL}
	_, err := f.FetchAsset(ctx, ts.URL+"/slow")
	if err == nil {
		t.Fatalf("FetchAsset: got nil error, want context.Canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false, err = %v", err)
	}
}

func TestFetcher_FetchLatestRelease_RepoSlugInPath(t *testing.T) {
	t.Parallel()

	const wantPath = "/repos/owner/different-name/releases/latest"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(ts.Close)

	f := Fetcher{BaseURL: ts.URL}
	if _, err := f.FetchLatestRelease(context.Background(), "owner/different-name"); err != nil {
		t.Fatalf("FetchLatestRelease: %v", err)
	}
}
