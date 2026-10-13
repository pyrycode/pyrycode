package relay

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

func testAPIPayload(body []byte) string {
	return fmt.Sprintf(`{"app_id":%q,"release_version":"1.2.3","path":"/api/%%41","query":"x=%%2f&x=&x=%%2F","method":"POST","headers":{"content-type":"application/octet-stream"},"body":%q}`, testAssetApp, base64.StdEncoding.EncodeToString(body))
}

func TestHostedAPIValidation(t *testing.T) {
	good := testAPIPayload(nil)
	for _, tc := range []struct {
		name, payload string
		valid         bool
	}{
		{"empty", good, true},
		{"unknown", strings.TrimSuffix(good, "}") + `,"future":{"x":1}}`, true},
		{"omitted_query", strings.Replace(good, `,"query":"x=%2f&x=&x=%2F"`, "", 1), true},
		{"empty_headers", strings.Replace(good, `{"content-type":"application/octet-stream"}`, `{}`, 1), true},
		{"null_body", strings.Replace(good, `"body":""`, `"body":null`, 1), false},
		{"missing_body", strings.Replace(good, `,"body":""`, "", 1), false},
		{"number_body", strings.Replace(good, `"body":""`, `"body":1`, 1), false},
		{"null_headers", strings.Replace(good, `{"content-type":"application/octet-stream"}`, `null`, 1), false},
		{"array_headers", strings.Replace(good, `{"content-type":"application/octet-stream"}`, `[]`, 1), false},
		{"number_header", strings.Replace(good, `"application/octet-stream"`, `1`, 1), false},
		{"null_method", strings.Replace(good, `"method":"POST"`, `"method":null`, 1), false},
		{"empty_query", strings.Replace(good, `x=%2f&x=&x=%2F`, "", 1), false},
		{"bad_release", strings.Replace(good, "1.2.3", "01.2.3", 1), false},
		{"bad_app", strings.Replace(good, testAssetApp, "ABCDEF22-2222-4222-8222-222222222222", 1), false},
		{"duplicate", strings.TrimSuffix(good, "}") + `,"future":{"x":1,"x":2}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, recognized, correlated, valid := parseHostedRequest([]byte(testAssetEnvelope(10, protocol.TypeAppAPIRequest, tc.payload)))
			if !recognized || !correlated || valid != tc.valid {
				t.Fatalf("recognized=%v correlated=%v valid=%v", recognized, correlated, valid)
			}
		})
	}
	for _, raw := range []string{
		testAssetEnvelope(10, protocol.TypeAppAPIRequest, good) + ` {}`,
		strings.Replace(testAssetEnvelope(10, protocol.TypeAppAPIRequest, good), `"id":10`, `"id":9007199254740992`, 1),
		strings.Replace(testAssetEnvelope(10, protocol.TypeAppAPIRequest, good), `"id":10`, `"id":10,"id":10`, 1),
		strings.Replace(testAssetEnvelope(10, protocol.TypeAppAPIRequest, good), `Z"`, `+00:00"`, 1),
		strings.Replace(testAssetEnvelope(10, protocol.TypeAppAPIRequest, good), `"payload":`, `"session_id":null,"payload":`, 1),
		strings.Replace(testAssetEnvelope(10, protocol.TypeAppAPIRequest, good), `"path":"/api/%41"`, "\"path\":\"/api/\xff\"", 1),
	} {
		if _, _, _, valid := parseHostedRequest([]byte(raw)); valid {
			t.Fatal("accepted malformed envelope")
		}
	}
}
