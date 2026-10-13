package apps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrAPIRequest = errors.New("apps: invalid API request")
	ErrAPILimit   = errors.New("apps: API request byte limit exceeded")
)

// APIRequest carries validated-at-use escaped URL text and opaque decoded bytes.
// Headers must use the lowercase allowlist. Reserve/Release follow ResponseInput's
// ownership contract. Do not mutate the request during ForwardAPI.
type APIRequest struct {
	Method, Path, Query string
	Headers             map[string]string
	Body                []byte
	Reserve             func(int64) bool
	Release             func(int64)
	// Progress has the optional positive-byte ResponseInput reporting contract.
	Progress func()
}

// ForwardAPI sends once to the authenticated host's exact active app generation.
// It never retries or claims rollback after cancellation of an uncertain mutation.
// The caller owns the returned response until Complete or Close.
func (o *Owner) ForwardAPI(ctx context.Context, serverID, appID, release string, in APIRequest) (*PreparedResponse, error) {
	target, _, err := o.responseTarget(serverID, appID, release, nil)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if len(in.Body) > 32768 {
		return nil, ErrAPILimit
	}
	switch in.Method {
	case "GET", "HEAD":
		if len(in.Body) != 0 {
			return nil, ErrAPIRequest
		}
	case "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
	default:
		return nil, ErrAPIRequest
	}
	decoded, ok := apiURL(in.Path, in.Query)
	if !ok || !validAPIHeaders(in.Headers) || in.Reserve == nil || in.Release == nil {
		return nil, ErrAPIRequest
	}
	req, err := http.NewRequestWithContext(ctx, in.Method, "http://private.invalid", bytes.NewReader(in.Body))
	if err != nil {
		return nil, ErrAPIRequest
	}
	req.URL.Path, req.URL.RawPath, req.URL.RawQuery = decoded, in.Path, in.Query
	for key, value := range in.Headers {
		req.Header.Set(key, value)
	}
	response, err := target.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !target.valid() {
			return nil, ErrUnavailable
		}
		return nil, ErrResponseIO
	}
	return o.prepareAPI(ctx, serverID, appID, release, target, in, response)
}

func (o *Owner) prepareAPI(ctx context.Context, serverID, appID, release string, target *Target, in APIRequest, response *http.Response) (*PreparedResponse, error) {
	headers := map[string]string{}
	for _, key := range []string{"content-type", "cache-control", "etag", "last-modified"} {
		if values := response.Header.Values(key); len(values) > 0 {
			headers[key] = strings.Join(values, ", ")
		}
	}
	for _, value := range response.Header.Values("Content-Encoding") {
		if !strings.EqualFold(strings.TrimSpace(value), "identity") {
			response.Body.Close()
			return nil, ErrResponseMetadata
		}
	}
	if !validResponseMetadata(response.StatusCode, headers) {
		response.Body.Close()
		return nil, ErrResponseMetadata
	}
	body, size := response.Body, response.ContentLength
	if in.Method == "HEAD" || response.StatusCode == 204 || response.StatusCode == 205 || response.StatusCode == 304 {
		if err := body.Close(); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrResponseIO
		}
		body, size = io.NopCloser(bytes.NewReader(nil)), 0
	}
	return o.prepare(ctx, serverID, appID, release, ResponseInput{Body: body, Size: size, Limit: 1048576, Status: response.StatusCode, Headers: headers, Reserve: in.Reserve, Release: in.Release, Progress: in.Progress}, target)
}

func validAPIHeaders(headers map[string]string) bool {
	if len(headers) > 8 {
		return false
	}
	for key, value := range headers {
		switch key {
		case "accept", "content-type", "if-none-match":
		default:
			return false
		}
		if len(value) > 1024 || !utf8.ValidString(value) {
			return false
		}
		for _, r := range value {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	raw, err := json.Marshal(headers)
	return err == nil && len(raw) <= 4096
}
