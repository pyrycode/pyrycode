package apps

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

const testHost = "11111111-1111-4111-8111-111111111111"
const testApp = "22222222-2222-4222-8222-222222222222"
const testManifest = `{"contract_version":1,"server_id":"` + testHost + `","app_id":"` + testApp + `","title":"App","release_version":"1.0.0","runtime":{"node":">=24.15.0 <25.0.0","sqlite":"node:sqlite"},"frontend":{"root":"dist/web","entry":"index.html"},"service":{"entry":"dist/service/main.js","migrate":"dist/service/migrate.js","health_path":"/_pyry/health","migrations":"sql/migrations"},"build":{"package":"package.json","lockfile":"package-lock.json","install":["npm","ci"],"typecheck":["npm","run","typecheck"],"test":["npm","test"],"compile":["npm","run","build"]}}`

func testJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func testMap(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if e := json.Unmarshal([]byte(testManifest), &m); e != nil {
		t.Fatal(e)
	}
	return m
}
func testBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func testInput(id string) []byte { return []byte(strings.ReplaceAll(testManifest, testApp, id)) }
func testID(i int) string        { return fmt.Sprintf("%08x-2222-4222-8222-222222222222", i) }

func TestManifestFields(t *testing.T) {
	t.Parallel()
	good, e := ValidateManifest([]byte(testManifest), testHost)
	if e != nil {
		t.Fatal(e)
	}
	if good.AppID() != testApp || good.ServerID() != testHost || good.Title() != "App" || good.ReleaseVersion() != "1.0.0" {
		t.Fatal(good)
	}
	// Every required field is independently tested at every nesting level.
	var walk func(map[string]any, []string)
	walk = func(m map[string]any, path []string) {
		for key, value := range m {
			field := append(append([]string{}, path...), key)
			for _, kind := range []string{"missing", "null", "wrong type", "wrong value"} {
				t.Run(strings.Join(field, ".")+"/"+kind, func(t *testing.T) {
					root := testMap(t)
					target := root
					for _, p := range path {
						target = target[p].(map[string]any)
					}
					switch kind {
					case "missing":
						delete(target, key)
					case "null":
						target[key] = nil
					case "wrong type":
						target[key] = true
					case "wrong value":
						switch value.(type) {
						case map[string]any:
							target[key] = map[string]any{}
						case []any:
							target[key] = []any{"npm"}
						case float64:
							target[key] = 2
						default:
							target[key] = "invalid"
							if key == "title" {
								target[key] = ""
							}
						}
					}
					if _, e := ValidateManifest(testJSON(t, root), testHost); !errors.Is(e, ErrInvalidManifest) {
						t.Fatalf("accepted %s: %v", field, e)
					}
				})
			}
			t.Run(strings.Join(field, ".")+"/unknown", func(t *testing.T) {
				root := testMap(t)
				target := root
				for _, p := range path {
					target = target[p].(map[string]any)
				}
				target["unexpected"] = 1
				if _, e := ValidateManifest(testJSON(t, root), testHost); e == nil {
					t.Fatal("unknown accepted")
				}
			})
			if nested, ok := value.(map[string]any); ok {
				walk(nested, field)
			}
		}
	}
	walk(testMap(t), nil)
}

func TestManifestBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		field, value string
		valid        bool
	}{
		{"title", strings.Repeat("é", 64), true}, {"title", strings.Repeat("é", 65), false}, {"title", "", false}, {"title", " App", false}, {"title", "App\u00a0", false}, {"title", "\t", false}, {"title", "a\x7f", false}, {"title", "a\u0085", false},
		{"description", strings.Repeat("é", 256), true}, {"description", strings.Repeat("é", 257), false}, {"description", "", false}, {"description", " spaces ", true}, {"description", "a\n", false}, {"description", "a\u009f", false},
		{"release_version", "0.0.0", true}, {"release_version", "2147483647.2147483647.2147483647", true}, {"release_version", "2147483648.0.0", false}, {"release_version", "01.0.0", false}, {"release_version", "1.0", false}, {"release_version", "1.0.0-beta", false}, {"release_version", "v1.0.0", false}, {"release_version", "-1.0.0", false},
		{"app_id", "AAAAAAAA-aaaa-4aaa-aaaa-aaaaaaaaaaaa", false}, {"app_id", "22222222-2222-1222-8222-222222222222", false}, {"app_id", "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", true}, {"app_id", "aaaaaaaa-aaaa-4aaa-caaa-aaaaaaaaaaaa", false}, {"server_id", "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", false},
	} {
		t.Run(tc.field+"/"+tc.value, func(t *testing.T) {
			m := testMap(t)
			m[tc.field] = tc.value
			_, e := ValidateManifest(testJSON(t, m), testHost)
			if (e == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, e)
			}
		})
	}
	for _, data := range [][]byte{
		[]byte(`[]`), []byte(`null`), []byte(testManifest + ` {}`), []byte(strings.Replace(testManifest, `"title":"App"`, `"title":"App","title":"App"`, 1)),
		[]byte(strings.Replace(testManifest, `"sqlite":"node:sqlite"`, `"sqlite":"node:sqlite","sqlite":"node:sqlite"`, 1)),
		[]byte(strings.Replace(testManifest, `"title":"App"`, `"title":"App","\u0074itle":"App"`, 1)),
		append([]byte(testManifest), 0xff), []byte(strings.Replace(testManifest, `"contract_version":1`, `"contract_version":1.0`, 1)),
		[]byte(strings.Replace(testManifest, `"title":"App"`, `"description":null,"title":"App"`, 1)), []byte(strings.Repeat("[", 100) + strings.Repeat("]", 100)),
		[]byte(strings.Replace(testManifest, `"title":"App"`, `"description":false,"title":"App"`, 1)),
	} {
		if _, e := ValidateManifest(data, testHost); e == nil {
			t.Fatalf("accepted %q", data)
		}
	}
	for _, extra := range []int{16384 - len(testManifest), 16385 - len(testManifest)} {
		data := append([]byte(testManifest), bytes.Repeat([]byte(" "), extra)...)
		_, e := ValidateManifest(data, testHost)
		if (e == nil) != (len(data) == 16384) {
			t.Fatal(len(data), e)
		}
	}
	if _, e := ValidateManifest([]byte(testManifest), "bad-host"); e == nil {
		t.Fatal("invalid host accepted")
	}
}
