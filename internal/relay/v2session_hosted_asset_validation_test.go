package relay

import (
	"fmt"
	"strings"
	"testing"
)

const testAssetApp = "22222222-2222-4222-8222-222222222222"

func testAssetPayload(path string) string {
	return fmt.Sprintf(`{"app_id":%q,"release_version":"1.2.3","path":%q,"navigation":false}`, testAssetApp, path)
}
func TestHostedAssetValidation(t *testing.T) {
	good := testAssetPayload("/index.html")
	for _, tc := range []struct {
		payload string
		valid   bool
	}{
		{good, true}, {strings.TrimSuffix(good, "}") + `,"additive":{"x":1}}`, true},
		{strings.Replace(good, `"navigation":false`, `"navigation":null`, 1), false},
		{strings.Replace(good, `"navigation":false`, `"navigation":0`, 1), false},
		{strings.Replace(good, `,"navigation":false`, "", 1), false},
		{strings.TrimSuffix(good, "}") + `,"query":null}`, false},
		{strings.TrimSuffix(good, "}") + `,"query":""}`, false},
		{strings.Replace(good, "1.2.3", "01.2.3", 1), false},
		{strings.Replace(good, "1.2.3", "2147483648.2.3", 1), false},
		{strings.Replace(good, "22222222-2222-4222-8222-222222222222", "ABCDEF22-2222-4222-8222-222222222222", 1), false},
		{strings.TrimSuffix(good, "}") + `,"additive":{"x":1,"x":2}}`, false},
	} {
		raw := fmt.Sprintf(`{"id":10,"type":"app_asset_request","ts":"2026-10-10T18:00:01Z","payload":%s}`, tc.payload)
		_, recognized, correlated, valid := parseHostedRequest([]byte(raw))
		if !recognized || !correlated || valid != tc.valid {
			t.Fatalf("%s: %v %v %v", raw, recognized, correlated, valid)
		}
	}
}
