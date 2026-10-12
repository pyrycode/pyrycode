package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func testHostedHeaders(t *testing.T, raw []byte) {
	t.Helper()
	if len(raw) == 0 || raw[0] != '{' {
		t.Fatalf("headers must be an object: %s", raw)
	}
	var headers map[string]string
	if err := json.Unmarshal(raw, &headers); err != nil {
		t.Fatal(err)
	}
}

func TestHostedApps_ResourceSharedExamples(t *testing.T) {
	t.Parallel()
	asset := testHostedExamples(t, "Two-chunk asset body decodes")
	requestID := uint64(3)
	t.Run("asset-request", func(t *testing.T) {
		testHostedRoundTrip(t, asset[0], TypeAppAssetRequest, 3, nil, AppAssetRequestPayload{AppID: testHostedAppID, ReleaseVersion: "1.0.0", Path: "/assets/example.txt", Query: "v=1&v=2"})
	})
	t.Run("asset-response", func(t *testing.T) {
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte("abcdef")))
		testHostedRoundTrip(t, asset[1], TypeAppResponse, 4, &requestID, AppResponsePayload{AppID: testHostedAppID, ReleaseVersion: "1.0.0", Status: 200, Headers: map[string]string{"content-type": "text/plain"}, SizeBytes: 6, TotalChunks: 2, SHA256: digest})
	})
	for i, position := range []int{2, 4} {
		t.Run(fmt.Sprintf("asset-chunk-%d", i), func(t *testing.T) {
			data := []string{"YWJj", "ZGVm"}[i]
			testHostedRoundTrip(t, asset[position], TypeAppResponseChunk, uint64(5+i), &requestID, AppResponseChunkPayload{AppID: testHostedAppID, Index: uint64(i), Data: data})
			testHostedBase64(t, data, []byte([]string{"abc", "def"}[i]))
		})
	}
	t.Run("asset-credit", func(t *testing.T) {
		testHostedRoundTrip(t, asset[3], TypeAppResponseCredit, 4, nil, AppResponseCreditPayload{AppID: testHostedAppID, RequestID: requestID, NextIndex: 1})
	})
	write := testHostedExamples(t, "API write stores")
	t.Run("api-write", func(t *testing.T) {
		body := "eyJwZXJpb2QiOiIyNGgifQ=="
		testHostedRoundTrip(t, write[0], TypeAppAPIRequest, 5, nil, AppAPIRequestPayload{AppID: testHostedAppID, ReleaseVersion: "1.0.0", Path: "/api/preferences", Method: "POST", Headers: map[string]string{"content-type": "application/json"}, Body: body})
		testHostedBase64(t, body, []byte(`{"period":"24h"}`))
	})
	t.Run("empty-response", func(t *testing.T) {
		digest := fmt.Sprintf("%x", sha256.Sum256(nil))
		testHostedRoundTrip(t, write[1], TypeAppResponse, 7, testHostedUint(5), AppResponsePayload{AppID: testHostedAppID, ReleaseVersion: "1.0.0", Status: 204, Headers: map[string]string{}, SHA256: digest})
	})
	cancel := testHostedExamples(t, "Cancellation of a new in-flight read;")
	t.Run("cancel-target", func(t *testing.T) {
		testHostedRoundTrip(t, cancel[0], TypeAppAPIRequest, 6, nil, AppAPIRequestPayload{AppID: testHostedAppID, ReleaseVersion: "1.0.0", Path: "/api/computers", Query: "period=24h", Method: "GET", Headers: map[string]string{}})
	})
	t.Run("cancel-control", func(t *testing.T) {
		p := AppCancelPayload{AppID: testHostedAppID, RequestID: 6}
		testHostedRoundTrip(t, cancel[1], TypeAppCancel, 7, nil, p)
		testHostedRoundTrip(t, cancel[2], TypeAppCancelled, 8, testHostedUint(7), p)
	})
	t.Run("navigation-without-query", func(t *testing.T) {
		raw := testHostedExamples(t, "Bridge error is distinct from HTTP failure:")[0]
		testHostedRoundTrip(t, raw, TypeAppAssetRequest, 8, nil, AppAssetRequestPayload{AppID: testHostedAppID, ReleaseVersion: "1.0.0", Path: "/", Navigation: true})
	})
}

func testHostedBase64(t *testing.T, data string, want []byte) {
	t.Helper()
	got, err := base64.StdEncoding.DecodeString(data)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("base64 bytes: got %x, want %x, error %v", got, want, err)
	}
	if base64.StdEncoding.EncodeToString(got) != data {
		t.Fatal("data is not canonical padded base64")
	}
}

func TestHostedApps_ResourceBoundaries(t *testing.T) {
	t.Parallel()
	for _, id := range []uint64{0, 9007199254740991} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			asset := fmt.Sprintf(`{"app_id":%q,"release_version":"1.0.0","path":"/","navigation":false}`, testHostedAppID)
			testHostedRoundTrip(t, testHostedFrame(TypeAppAssetRequest, id, nil, asset), TypeAppAssetRequest, id, nil, AppAssetRequestPayload{AppID: testHostedAppID, ReleaseVersion: "1.0.0", Path: "/"})
			for _, body := range []string{"", "AP+A/v8="} {
				api := fmt.Sprintf(`{"app_id":%q,"release_version":"1.0.0","path":"/api/binary","method":"POST","headers":{},"body":%q}`, testHostedAppID, body)
				testHostedRoundTrip(t, testHostedFrame(TypeAppAPIRequest, id, nil, api), TypeAppAPIRequest, id, nil, AppAPIRequestPayload{AppID: testHostedAppID, ReleaseVersion: "1.0.0", Path: "/api/binary", Method: "POST", Headers: map[string]string{}, Body: body})
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(nil))
			response := fmt.Sprintf(`{"app_id":%q,"release_version":"1.0.0","status":200,"headers":{},"size_bytes":0,"total_chunks":0,"sha256":%q}`, testHostedAppID, digest)
			testHostedRoundTrip(t, testHostedFrame(TypeAppResponse, id, &id, response), TypeAppResponse, id, &id, AppResponsePayload{AppID: testHostedAppID, ReleaseVersion: "1.0.0", Status: 200, Headers: map[string]string{}, SHA256: digest})
			for _, index := range []uint64{0, 255} {
				chunk := fmt.Sprintf(`{"app_id":%q,"index":%d,"data":"AP+A/v8="}`, testHostedAppID, index)
				testHostedRoundTrip(t, testHostedFrame(TypeAppResponseChunk, id, &id, chunk), TypeAppResponseChunk, id, &id, AppResponseChunkPayload{AppID: testHostedAppID, Index: index, Data: "AP+A/v8="})
				credit := fmt.Sprintf(`{"app_id":%q,"request_id":%d,"next_index":%d}`, testHostedAppID, id, index)
				testHostedRoundTrip(t, testHostedFrame(TypeAppResponseCredit, id, nil, credit), TypeAppResponseCredit, id, nil, AppResponseCreditPayload{AppID: testHostedAppID, RequestID: id, NextIndex: index})
			}
		})
	}
	testHostedBase64(t, "AP+A/v8=", []byte{0, 255, 128, 254, 255})
}

func TestHostedApps_RequiredHeaderObjects(t *testing.T) {
	t.Parallel()
	for _, headers := range []map[string]string{nil, {}, {"content-type": "application/octet-stream"}} {
		api := AppAPIRequestPayload{Headers: headers}
		response := AppResponsePayload{Headers: headers}
		for _, tc := range []struct {
			typ string
			p   any
		}{{TypeAppAPIRequest, api}, {TypeAppResponse, response}} {
			raw, err := json.Marshal(tc.p)
			if err != nil {
				t.Fatal(err)
			}
			testHostedPayloadKeys(t, tc.typ, raw)
			var decoded struct {
				Headers map[string]string `json:"headers"`
			}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded.Headers) != len(headers) || decoded.Headers["content-type"] != headers["content-type"] {
				t.Fatalf("headers changed: %s", raw)
			}
		}
		if headers == nil && (api.Headers != nil || response.Headers != nil) {
			t.Fatal("marshal mutated the caller")
		}
	}
}

func TestHostedApps_ResourceEnvelopeSizes(t *testing.T) {
	t.Parallel()
	body := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{255}, 32768))
	path := "/api/" + strings.Repeat("a", 2048-len("/api/"))
	query := "k=" + strings.Repeat("&", 1022)
	// Each header value is within 1024 decoded bytes; JSON escaping is counted.
	headers := map[string]string{"accept": strings.Repeat("h", 1024), "content-type": strings.Repeat("h", 1024), "if-none-match": strings.Repeat("<", 170) + strings.Repeat("h", 854)}
	headerJSON, err := json.Marshal(headers)
	if err != nil {
		t.Fatal(err)
	}
	if len(headerJSON) > 4096 {
		t.Fatalf("header fixture exceeds contract: %d", len(headerJSON))
	}
	for _, tc := range []struct {
		typ   string
		p     any
		reply *uint64
	}{
		{TypeAppAssetRequest, AppAssetRequestPayload{AppID: testHostedAppID, ReleaseVersion: "2147483647.2147483647.2147483647", Path: "/assets/" + strings.Repeat("a", 2048-len("/assets/")), Query: query}, nil},
		{TypeAppAPIRequest, AppAPIRequestPayload{AppID: testHostedAppID, ReleaseVersion: "2147483647.2147483647.2147483647", Path: path, Query: query, Method: "OPTIONS", Headers: headers, Body: body}, nil},
		{TypeAppResponseChunk, AppResponseChunkPayload{AppID: testHostedAppID, Index: 255, Data: body}, testHostedUint(9007199254740991)},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			payload, err := json.Marshal(tc.p)
			if err != nil {
				t.Fatal(err)
			}
			raw := testHostedFrame(tc.typ, 9007199254740991, tc.reply, string(payload))
			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatal(err)
			}
			final, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			if len(final) > 65519 {
				t.Fatalf("serialized envelope: %d exceeds 65519", len(final))
			}
			t.Logf("serialized envelope: %d plaintext bytes", len(final))
		})
	}
}
