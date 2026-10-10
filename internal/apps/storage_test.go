package apps

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testStorageDocument(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"server_id": testHost, "revision": 2,
		"records": []any{map[string]any{
			"app_id": testApp, "manifest": testMap(t), "title": "App",
			"desired": "available", "state": "stopped", "revision": 1,
			"active_release": "1.0.0", "pending_release": "2.0.0",
			"last_error": map[string]any{"code": "app.io_failed", "message": "Failure"},
		}},
		"tombstones": []any{map[string]any{"app_id": testID(3), "revision": 2}},
	}
}

func testStorageOpen(t *testing.T, data []byte, host string, valid bool) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "registry.json")
	if e := os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	r, e := Open(root, host)
	if valid {
		if e != nil || r == nil {
			t.Fatalf("valid storage rejected: %v", e)
		}
	} else if !errors.Is(e, ErrInvalidStorage) || r != nil {
		t.Fatalf("corrupt storage accepted: registry=%v err=%v", r, e)
	}
	if !bytes.Equal(data, testBytes(t, path)) {
		t.Fatal("opening storage changed its bytes")
	}
}

func TestStorageExactKeys(t *testing.T) {
	t.Parallel()
	testStorageOpen(t, testJSON(t, testStorageDocument(t)), testHost, true)
	testStorageOpen(t, testJSON(t, snapshot{ServerID: testHost, Records: []Record{}, Tombstones: []tombstone{}}), testHost, true)
	for _, scope := range []struct {
		name                     string
		path, required, optional []string
	}{
		{"snapshot", nil, []string{"server_id", "revision", "records", "tombstones"}, nil},
		{"record", []string{"records"}, []string{"app_id", "manifest", "title", "desired", "state", "revision"}, []string{"active_release", "pending_release", "last_error"}},
		{"tombstone", []string{"tombstones"}, []string{"app_id", "revision"}, nil},
		{"last_error", []string{"records", "last_error"}, []string{"code", "message"}, nil},
	} {
		target := func(m map[string]any) map[string]any {
			for _, key := range scope.path {
				switch value := m[key].(type) {
				case []any:
					m = value[0].(map[string]any)
				case map[string]any:
					m = value
				}
			}
			return m
		}
		t.Run(scope.name+"/unknown", func(t *testing.T) {
			m := testStorageDocument(t)
			target(m)["unexpected"] = true
			testStorageOpen(t, testJSON(t, m), testHost, false)
		})
		for _, key := range append(scope.required, scope.optional...) {
			for _, kind := range []string{"missing", "alias_only", "alias_extra"} {
				t.Run(scope.name+"/"+key+"/"+kind, func(t *testing.T) {
					m := testStorageDocument(t)
					fields := target(m)
					value := fields[key]
					valid := false
					switch kind {
					case "missing":
						delete(fields, key)
						for _, optional := range scope.optional {
							valid = valid || key == optional
						}
					case "alias_only":
						delete(fields, key)
						fields[strings.ToUpper(key)] = value
					case "alias_extra":
						fields[strings.ToUpper(key)] = value
					}
					testStorageOpen(t, testJSON(t, m), testHost, valid)
				})
			}
		}
	}
}

func TestStorageAliasShadowing(t *testing.T) {
	t.Parallel()
	m := testStorageDocument(t)
	baseline := string(testJSON(t, m))
	for _, tc := range []struct{ name, data string }{
		{"missing_host", `{"revision":5,"Revision":0,"records":[],"tombstones":[]}`},
		{"missing_revision", `{"server_id":"` + testHost + `","SERVER_ID":"` + testHost + `","records":[],"tombstones":[]}`},
		{"record_identity", strings.Replace(baseline, `"app_id":"`+testApp+`"`, `"app_id":"bad","APP_ID":"`+testApp+`"`, 1)},
		{"record_revision", strings.Replace(baseline, `"revision":1`, `"revision":0,"REVISION":1`, 1)},
		{"tombstone_identity", strings.Replace(baseline, `"app_id":"`+testID(3)+`"`, `"app_id":"bad","APP_ID":"`+testID(3)+`"`, 1)},
		{"tombstone_revision", strings.Replace(baseline, `"revision":2}]`, `"revision":0,"REVISION":2}]`, 1)},
		{"error_code", strings.Replace(baseline, `"code":"app.io_failed"`, `"code":"bad","CODE":"app.io_failed"`, 1)},
		{"error_message", strings.Replace(baseline, `"message":"Failure"`, `"message":"","MESSAGE":"Failure"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.data == baseline {
				t.Fatal("corruption did not change fixture")
			}
			testStorageOpen(t, []byte(tc.data), testHost, false)
			if tc.name == "missing_host" {
				testStorageOpen(t, []byte(tc.data), "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", false)
			}
		})
	}
}
