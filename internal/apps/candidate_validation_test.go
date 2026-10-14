package apps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCandidateAdmission(t *testing.T) {
	for _, kind := range []string{"foreign", "unknown", "removed", "invalid", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			r, tools, c, _ := testCandidate(t)
			host, id := testHost, testApp
			ctx := context.Background()
			switch kind {
			case "foreign":
				host = testID(9)
			case "unknown":
				id = testID(9)
			case "removed":
				if _, e := r.Remove(testApp); e != nil {
					t.Fatal(e)
				}
			case "invalid":
				id = "../escape"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := testBytes(t, filepath.Join(r.root, "registry.json"))
			_, e := r.PrepareCandidate(ctx, host, id, tools)
			testCandidateFailure(t, e, "admission")
			if _, e = os.Stat(c.Log); !os.IsNotExist(e) {
				t.Fatal("ran npm", e)
			}
			if !reflect.DeepEqual(before, testBytes(t, filepath.Join(r.root, "registry.json"))) {
				t.Fatal("admission changed registry")
			}
		})
	}
}
func TestCandidateSource(t *testing.T) {
	for _, kind := range []string{"file link", "directory link", "source link", "app link", "ancestor link", "fifo", "directory manifest"} {
		t.Run(kind, func(t *testing.T) {
			r, tools, c, _ := testCandidate(t)
			source := filepath.Join(r.root, testApp, "source")
			outside := testRoot(t)
			link := func(p, target string) {
				t.Helper()
				if e := os.Symlink(target, p); e != nil {
					t.Fatal(e)
				}
			}
			switch kind {
			case "file link":
				link(filepath.Join(source, "linked"), filepath.Join(c.Fixture, "package.json"))
			case "directory link":
				link(filepath.Join(source, "linked"), outside)
			case "source link", "app link":
				p := source
				if kind == "app link" {
					p = filepath.Dir(source)
				}
				if e := os.Rename(p, p+".original"); e != nil {
					t.Fatal(e)
				}
				link(p, p+".original")
			case "ancestor link":
				alias := filepath.Join(outside, "alias")
				link(alias, r.root)
				r.root = alias
			case "fifo":
				if e := unix.Mkfifo(filepath.Join(source, "pipe"), 0600); e != nil {
					t.Fatal(e)
				}
			case "directory manifest":
				if e := os.Remove(filepath.Join(source, "app.json")); e != nil {
					t.Fatal(e)
				}
				if e := os.Mkdir(filepath.Join(source, "app.json"), 0700); e != nil {
					t.Fatal(e)
				}
			}
			_, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
			phase := "staging"
			if kind == "directory manifest" {
				phase = "validation"
			}
			testCandidateFailure(t, e, phase)
			if _, e = os.Stat(c.Log); !os.IsNotExist(e) {
				t.Fatal("ran npm", e)
			}
		})
	}
	r, tools, _, _ := testCandidate(t)
	source := filepath.Join(r.root, testApp, "source")
	for _, n := range []string{"node_modules/old", "dist/old"} {
		testArtifactWrite(t, source, n, "source sentinel")
	}
	before := testTreeBytes(t, source)
	if _, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, testTreeBytes(t, source)) {
		t.Fatal("source outputs changed")
	}
}
func TestCandidateValidation(t *testing.T) {
	for _, kind := range []string{"node old", "node major", "node malformed", "npm malformed", "manifest app", "manifest host", "package type", "lock name", "lock version", "root name", "root version", "dependencies", "devDependencies", "optionalDependencies", "peerDependencies", "null map", "non-string map"} {
		t.Run(kind, func(t *testing.T) {
			r, tools, c, config := testCandidate(t)
			source := filepath.Join(r.root, testApp, "source")
			var pkg, lock map[string]any
			if e := json.Unmarshal(testBytes(t, filepath.Join(source, "package.json")), &pkg); e != nil {
				t.Fatal(e)
			}
			if e := json.Unmarshal(testBytes(t, filepath.Join(source, "package-lock.json")), &lock); e != nil {
				t.Fatal(e)
			}
			root := lock["packages"].(map[string]any)[""].(map[string]any)
			switch kind {
			case "node old":
				c.Node = "v24.14.9"
			case "node major":
				c.Node = "v25.0.0"
			case "node malformed":
				c.Node = "v24.15.0 SECRET"
			case "npm malformed":
				c.NPM = "11.0.0 SECRET"
			case "manifest app", "manifest host":
				m := testMap(t)
				key := "app_id"
				if kind == "manifest host" {
					key = "server_id"
				}
				m[key] = testID(9)
				testArtifactWrite(t, source, "app.json", string(testJSON(t, m)))
			case "package type":
				pkg["type"] = "commonjs"
			case "lock name":
				lock["name"] = "different"
			case "lock version":
				lock["version"] = "2.0.0"
			case "root name":
				root["name"] = "different"
			case "root version":
				root["version"] = "2.0.0"
			case "null map":
				pkg["optionalDependencies"] = nil
			case "non-string map":
				pkg["optionalDependencies"] = map[string]any{"x": 1}
				root["optionalDependencies"] = pkg["optionalDependencies"]
			default:
				pkg[kind] = map[string]any{"changed": "1.0.0"}
			}
			testArtifactWrite(t, source, "package.json", string(testJSON(t, pkg)))
			testArtifactWrite(t, source, "package-lock.json", string(testJSON(t, lock)))
			testCandidateConfig(t, config, c)
			_, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
			testCandidateFailure(t, e, "validation")
			if _, e = os.Stat(c.Log); !os.IsNotExist(e) {
				t.Fatal("installed before validation", e)
			}
		})
	}
	// Package versions are independent of app releases; absent/empty maps agree.
	r, tools, _, _ := testCandidate(t)
	source := filepath.Join(r.root, testApp, "source")
	var pkg, lock map[string]any
	json.Unmarshal(testBytes(t, filepath.Join(source, "package.json")), &pkg)
	json.Unmarshal(testBytes(t, filepath.Join(source, "package-lock.json")), &lock)
	root := lock["packages"].(map[string]any)[""].(map[string]any)
	for _, m := range []map[string]any{pkg, lock, root} {
		m["version"] = "7.8.9"
	}
	pkg["peerDependencies"] = map[string]any{}
	testArtifactWrite(t, source, "package.json", string(testJSON(t, pkg)))
	testArtifactWrite(t, source, "package-lock.json", string(testJSON(t, lock)))
	testCandidateVersion(t, r, "1.9.0")
	if _, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools); e != nil {
		t.Fatal(e)
	}
}
func TestCandidateArtifacts(t *testing.T) {
	r, tools, c, _ := testCandidate(t)
	testArtifactWrite(t, c.Fixture, "dist/web/.env", "SECRET")
	_, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
	testCandidateFailure(t, e, "artifacts")
	if got := string(testBytes(t, c.Log)); !strings.HasSuffix(got, "run build\n") {
		t.Fatal(got)
	}
	if len(ownerRecord(t, r, testApp).ReleaseHistory) != 0 {
		t.Fatal("recorded invalid output")
	}
}

func TestCandidateSourceExecutable(t *testing.T) {
	r, _, _, _ := testCandidate(t)
	source := filepath.Join(r.root, testApp, "source")
	testArtifactWrite(t, source, "scripts/custom", "#!/bin/sh\nexit 0\n")
	if e := os.Chmod(filepath.Join(source, "scripts/custom"), 0700); e != nil {
		t.Fatal(e)
	}
	stage, cleanup, e := candidateStaging(context.Background(), source)
	if e != nil {
		t.Fatal(e)
	}
	defer cleanup()
	info, e := os.Stat(filepath.Join(stage, "scripts/custom"))
	if e != nil || info.Mode().Perm() != 0700 {
		t.Fatal(info, e)
	}
}
func TestCandidateBuildMutation(t *testing.T) {
	r, tools, c, config := testCandidate(t)
	c.Mutate = `{"type":"commonjs"}`
	testCandidateConfig(t, config, c)
	_, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
	testCandidateFailure(t, e, "artifacts")
}
