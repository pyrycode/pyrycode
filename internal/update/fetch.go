package update

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// Fetcher performs the network reads required by the `pyry update` flow:
// the GitHub Releases API JSON and the release-asset bytes (tarball,
// checksums.txt). It is a thin wrapper over net/http: no retries, no
// caller-imposed timeouts, no body parsing — those concerns live
// elsewhere in this package and in the wiring ticket.
//
// The zero value is usable and targets api.github.com with
// http.DefaultClient and a "pyry/dev" User-Agent. Callers in cmd/pyry
// override UserAgent to "pyry/" + main.Version and typically install an
// HTTPClient with an explicit Timeout (the fetcher does not impose one).
//
// *Fetcher is safe for concurrent use: all fields are read after
// construction, *http.Client.Do is documented as concurrency-safe, and
// each call constructs a fresh *http.Request.
type Fetcher struct {
	// BaseURL is the API root for the latest-release endpoint.
	// Empty value defaults to "https://api.github.com".
	// Tests set this to httptest.NewServer's URL.
	BaseURL string

	// HTTPClient is the underlying transport. Empty value defaults to
	// http.DefaultClient. Callers wanting a request budget construct a
	// &http.Client{Timeout: 60*time.Second} and assign it here.
	HTTPClient *http.Client

	// UserAgent is sent on every request. Empty value defaults to
	// "pyry/dev". Callers in cmd/pyry set this to "pyry/" + main.Version
	// so GitHub's API doesn't blanket-reject anonymous traffic.
	UserAgent string
}

// FetchLatestRelease GETs <BaseURL>/repos/<repo>/releases/latest and returns
// the response body verbatim. repo is in "owner/name" form (e.g.
// "pyrycode/pyrycode"). The body is the raw GitHub Releases API JSON,
// suitable for passing to ParseLatestRelease.
//
// Returns an error if the request cannot be constructed, the transport
// fails, the body cannot be read, or the response status is not 2xx.
// Non-2xx errors include the status code and the requested URL. Context
// cancellation propagates: an error wrapping context.Canceled or
// context.DeadlineExceeded is returned if ctx is done before the response
// body is fully received.
func (f *Fetcher) FetchLatestRelease(ctx context.Context, repo string) ([]byte, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", f.baseURL(), repo)
	return f.get(ctx, url)
}

// FetchAsset GETs the given URL and returns the response body verbatim.
// The URL is typically extracted from the release JSON returned by
// FetchLatestRelease (assets[].browser_download_url for the tarball, or
// the URL of the checksums.txt asset). The User-Agent header is sent on
// this request, same as FetchLatestRelease.
//
// Same error semantics as FetchLatestRelease.
func (f *Fetcher) FetchAsset(ctx context.Context, url string) ([]byte, error) {
	return f.get(ctx, url)
}

func (f *Fetcher) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building GET %s: %w", url, err)
	}
	req.Header.Set("User-Agent", f.userAgent())

	resp, err := f.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Drain a bounded prefix of the body so the connection can be
		// reused; ignore errors — we already have a status to report.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		return nil, fmt.Errorf("GET %s: unexpected status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body from %s: %w", url, err)
	}
	return body, nil
}

func (f *Fetcher) baseURL() string {
	if f.BaseURL == "" {
		return "https://api.github.com"
	}
	return f.BaseURL
}

func (f *Fetcher) httpClient() *http.Client {
	if f.HTTPClient == nil {
		return http.DefaultClient
	}
	return f.HTTPClient
}

func (f *Fetcher) userAgent() string {
	if f.UserAgent == "" {
		return "pyry/dev"
	}
	return f.UserAgent
}
