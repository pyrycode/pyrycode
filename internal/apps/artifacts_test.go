package apps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func testArtifactTemp(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
func testArtifactWrite(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func testArtifactTree(t *testing.T) string {
	t.Helper()
	root := testArtifactTemp(t)
	for name, content := range map[string]string{"app.json": testManifest, "package.json": "{}", "package-lock.json": "{}", "dist/web/index.html": "<html>", "dist/service/main.js": "main", "dist/service/migrate.js": "migrate", "sql/migrations/0010_next.sql": "next", "sql/migrations/0002_first.sql": "first", "node_modules/pkg/index.js": "dependency", "src/edit.ts": "excluded", "dist/cache/file": "excluded"} {
		testArtifactWrite(t, root, name, content)
	}
	return root
}
func testArtifactLink(t *testing.T, root, name, target string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
}
func TestRetainedCanonicalInventory(t *testing.T) {
	t.Parallel()
	// These expected bytes deliberately do not use a JSON encoder or the production serializer.
	names := []string{"z", "é\"\\\n<&>\u2028\u2029"}
	entries := []artifactEntry{{path: names[1], kind: "file", size: 3, digest: "abc"}, {path: names[0], kind: "symlink", target: "é\"\\\t<&>"}}
	want := `[{"path":"z","kind":"symlink","target":"é\"\\\u0009<&>"},{"path":"é\"\\\u000a<&>` + "\u2028\u2029" + `","kind":"file","size_bytes":3,"sha256":"abc"}]`
	got := canonicalInventory(entries)
	if string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if digestBytes(got) != fmt.Sprintf("%x", sha256.Sum256([]byte(want))) {
		t.Fatal("digest")
	}
	var controls strings.Builder
	for c := 0; c < 32; c++ {
		fmt.Fprintf(&controls, `\u00%02x`, c)
	}
	input := make([]byte, 32)
	for c := range input {
		input[c] = byte(c)
	}
	if got := inventoryString(string(input)); got != `"`+controls.String()+`"` {
		t.Fatalf("controls: %s", got)
	}
}
func TestRetainedValidation(t *testing.T) {
	t.Parallel()
	root := testArtifactTree(t)
	result, err := ValidateArtifacts(context.Background(), root, testHost, testApp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Migrations, ",") != "sql/migrations/0002_first.sql,sql/migrations/0010_next.sql" {
		t.Fatal(result.Migrations)
	}
	if bytes.Contains(result.Inventory, []byte("excluded")) || bytes.Contains(result.Inventory, []byte("src/")) {
		t.Fatal("source retained")
	}
	for _, tc := range []struct {
		name string
		edit func(*testing.T, string)
	}{
		{"app identity", func(t *testing.T, r string) {
			testArtifactWrite(t, r, "app.json", strings.ReplaceAll(testManifest, testApp, testID(3)))
		}},
		{"host identity", func(t *testing.T, r string) {
			testArtifactWrite(t, r, "app.json", strings.ReplaceAll(testManifest, testHost, testID(3)))
		}},
		{"missing entry", func(t *testing.T, r string) {
			if e := os.Remove(filepath.Join(r, "dist/service/migrate.js")); e != nil {
				t.Fatal(e)
			}
		}},
		{"duplicate migration", func(t *testing.T, r string) { testArtifactWrite(t, r, "sql/migrations/0002_other.sql", "x") }},
		{"migration name", func(t *testing.T, r string) { testArtifactWrite(t, r, "sql/migrations/0003_UP.sql", "x") }},
		{"nested migration", func(t *testing.T, r string) { testArtifactWrite(t, r, "sql/migrations/sub/0003_ok.sql", "x") }},
		{"entry link", func(t *testing.T, r string) {
			if e := os.Remove(filepath.Join(r, "dist/web/index.html")); e != nil {
				t.Fatal(e)
			}
			testArtifactLink(t, r, "dist/web/index.html", "../../package.json")
		}},
		{"component link", func(t *testing.T, r string) {
			if e := os.Rename(filepath.Join(r, "dist/web"), filepath.Join(r, "web")); e != nil {
				t.Fatal(e)
			}
			testArtifactLink(t, r, "dist/web", "../web")
		}},
		{"fifo", func(t *testing.T, r string) {
			if e := unix.Mkfifo(filepath.Join(r, "dist/service/pipe"), 0600); e != nil {
				t.Fatal(e)
			}
		}},
		{"invalid UTF8", func(t *testing.T, r string) { testArtifactWrite(t, r, "node_modules/\xff", "x") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testArtifactTree(t)
			tc.edit(t, r)
			if _, e := ValidateArtifacts(context.Background(), r, testHost, testApp); e == nil {
				t.Fatal("accepted")
			}
		})
	}
	for _, name := range []string{".env", ".env.production", "secrets.json", "credentials.json", "private.pem", "app.db", "app.sqlite-wal", "dump.sql", "service/main.js", "dist/service/main.js", "node_modules/pkg.js", "package.json", "package-lock.json", "sql/a.txt"} {
		t.Run(name, func(t *testing.T) {
			r := testArtifactTree(t)
			testArtifactWrite(t, r, "dist/web/"+name, "unsafe")
			if _, e := ValidateArtifacts(context.Background(), r, testHost, testApp); e == nil {
				t.Fatal("unsafe public artifact accepted")
			}
		})
	}
	alias := filepath.Join(testArtifactTemp(t), "staging")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateArtifacts(context.Background(), alias, testHost, testApp); err == nil {
		t.Fatal("root link accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ValidateArtifacts(ctx, root, testHost, testApp); err == nil {
		t.Fatal("cancel accepted")
	}
}
func TestRetainedLinks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, target string
		valid        bool
	}{
		{"file", "pkg/index.js", true}, {"directory", "pkg", true}, {"chain", "first", true}, {"linked component", "first/index.js", true},
		{"dangling", "missing", false}, {"absolute", "/tmp", false}, {"escape", "../package.json", false}, {"escape then return", "../node_modules/pkg", false}, {"cycle", "link", false}, {"chained escape", "bad/index.js", false}, {"invalid target", "pkg/\xff", false}, {"nondirectory", "pkg/index.js/child", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testArtifactTree(t)
			testArtifactLink(t, r, "node_modules/first", "pkg")
			testArtifactLink(t, r, "node_modules/bad", "../dist")
			if tc.name != "chained escape" {
				if e := os.Remove(filepath.Join(r, "node_modules/bad")); e != nil {
					t.Fatal(e)
				}
			}
			testArtifactLink(t, r, "node_modules/link", tc.target)
			_, e := ValidateArtifacts(context.Background(), r, testHost, testApp)
			if (e == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, e)
			}
		})
	}
	r := testArtifactTree(t)
	testArtifactLink(t, r, "node_modules/a", "b")
	testArtifactLink(t, r, "node_modules/b", "a")
	if _, e := ValidateArtifacts(context.Background(), r, testHost, testApp); e == nil {
		t.Fatal("two-link cycle")
	}
}
func testWritableBuild(t *testing.T, root string) {
	t.Helper()
	if e := filepath.Walk(root, func(p string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if i.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if i.IsDir() {
			return os.Chmod(p, 0700)
		}
		return os.Chmod(p, 0600)
	}); e != nil {
		t.Fatal(e)
	}
}
func TestRetainedInstall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stage := testArtifactTree(t)
	root := testArtifactTemp(t)
	testArtifactLink(t, stage, "node_modules/alias", "pkg")
	if e := os.Chmod(filepath.Join(stage, "node_modules/pkg/index.js"), 0700); e != nil {
		t.Fatal(e)
	}
	first, e := InstallBuild(ctx, root, stage, testHost, testApp)
	if e != nil {
		t.Fatal(e)
	}
	build := filepath.Join(root, testApp, "builds", first.BuildID)
	t.Cleanup(func() { testWritableBuild(t, build) })
	copied, e := os.Stat(filepath.Join(build, "node_modules/pkg/index.js"))
	if e != nil || copied.Mode().Perm() != 0500 {
		t.Fatal("executable dependency", copied, e)
	}
	original, e := os.Stat(filepath.Join(stage, "node_modules/pkg/index.js"))
	if e != nil || original.Mode().Perm() != 0700 {
		t.Fatal("source mode changed", original, e)
	}
	target, e := os.Readlink(filepath.Join(build, "node_modules/alias"))
	if e != nil || target != "pkg" {
		t.Fatal("copied link", target, e)
	}
	if _, e := os.Stat(filepath.Join(build, "src")); !os.IsNotExist(e) {
		t.Fatal("source retained", e)
	}
	if e := filepath.Walk(build, func(p string, i os.FileInfo, e error) error {
		if e == nil && i.Mode()&os.ModeSymlink == 0 && i.Mode().Perm()&0222 != 0 {
			return fmt.Errorf("writable: %s", p)
		}
		return e
	}); e != nil {
		t.Fatal(e)
	}
	again, e := InstallBuild(ctx, root, stage, testHost, testApp)
	if e != nil || again.BuildID != first.BuildID {
		t.Fatal(again, e)
	}
	testArtifactWrite(t, stage, "node_modules/pkg/index.js", "changed")
	if _, e := VerifyBuild(ctx, root, testHost, testApp, first.BuildID); e != nil {
		t.Fatal("staging edit affected build", e)
	}
	if _, e := VerifyBuild(ctx, root, testID(3), testApp, first.BuildID); e == nil {
		t.Fatal("foreign host")
	}
	if _, e := VerifyBuild(ctx, root, testHost, "../escape", first.BuildID); e == nil {
		t.Fatal("app traversal")
	}
	if _, e := VerifyBuild(ctx, root, testHost, testApp, "../escape"); e == nil {
		t.Fatal("digest traversal")
	}
}
func TestRetainedVerification(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"missing", "empty", "changed", "added", "removed", "inventory", "link", "changed valid link", "missing inventory", "digest", "writable", "extra directory"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			stage := testArtifactTree(t)
			root := testArtifactTemp(t)
			testArtifactLink(t, stage, "node_modules/alias", "pkg")
			result, e := ValidateArtifacts(ctx, stage, testHost, testApp)
			if e != nil {
				t.Fatal(e)
			}
			build := filepath.Join(root, testApp, "builds", result.BuildID)
			if kind == "empty" {
				if e := os.MkdirAll(build, 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.Chmod(build, 0500); e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { testWritableBuild(t, build) })
			} else if kind != "missing" {
				if _, e := InstallBuild(ctx, root, stage, testHost, testApp); e != nil {
					t.Fatal(e)
				}
				testWritableBuild(t, build)
				switch kind {
				case "changed":
					testArtifactWrite(t, build, "dist/web/index.html", "changed")
				case "added":
					testArtifactWrite(t, build, "node_modules/added", "added")
				case "removed":
					if e := os.Remove(filepath.Join(build, "package.json")); e != nil {
						t.Fatal(e)
					}
				case "inventory":
					testArtifactWrite(t, build, "inventory.json", string(result.Inventory)+"\n")
				case "digest":
					wrong := strings.Repeat("a", 64)
					if e := os.Rename(build, filepath.Join(filepath.Dir(build), wrong)); e != nil {
						t.Fatal(e)
					}
					build = filepath.Join(filepath.Dir(build), wrong)
					result.BuildID = wrong
				case "missing inventory":
					if e := os.Remove(filepath.Join(build, "inventory.json")); e != nil {
						t.Fatal(e)
					}
				case "changed valid link":
					if e := os.Remove(filepath.Join(build, "node_modules/alias")); e != nil {
						t.Fatal(e)
					}
					testArtifactLink(t, build, "node_modules/alias", "./pkg")
				case "link":
					if e := os.Remove(filepath.Join(build, "node_modules/alias")); e != nil {
						t.Fatal(e)
					}
					testArtifactLink(t, build, "node_modules/alias", "../dist")
				case "extra directory":
					if e := os.Mkdir(filepath.Join(build, "extra"), 0500); e != nil {
						t.Fatal(e)
					}
				}
				if kind != "writable" {
					if e := filepath.Walk(build, func(p string, i os.FileInfo, e error) error {
						if e != nil {
							return e
						}
						if i.Mode()&os.ModeSymlink != 0 {
							return nil
						}
						if i.IsDir() {
							return os.Chmod(p, 0500)
						}
						return os.Chmod(p, 0400)
					}); e != nil {
						t.Fatal(e)
					}
				}
				t.Cleanup(func() { testWritableBuild(t, build) })
			}
			before := testArtifactSnapshot(t, build)
			if _, e := VerifyBuild(ctx, root, testHost, testApp, result.BuildID); e == nil {
				t.Fatal("verified invalid build")
			}
			if kind != "missing" && kind != "digest" {
				if _, e := InstallBuild(ctx, root, stage, testHost, testApp); e == nil {
					t.Fatal("repaired invalid destination")
				}
			} else if kind == "missing" {
				if _, e := os.Stat(build); !os.IsNotExist(e) {
					t.Fatal("verification created build")
				}
			}
			if after := testArtifactSnapshot(t, build); after != before {
				t.Fatal("rejected destination changed")
			}
		})
	}
}
func TestRetainedConcurrentInstall(t *testing.T) {
	t.Parallel()
	root, stage := testArtifactTemp(t), testArtifactTree(t)
	var wg sync.WaitGroup
	results := make(chan RetainedBuild, 4)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := InstallBuild(context.Background(), root, stage, testHost, testApp)
			results <- r
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for r := range results {
		if id != "" && id != r.BuildID {
			t.Fatal("different digests")
		}
		id = r.BuildID
	}
	build := filepath.Join(root, testApp, "builds", id)
	t.Cleanup(func() { testWritableBuild(t, build) })
	entries, e := os.ReadDir(filepath.Dir(build))
	if e != nil || len(entries) != 1 {
		t.Fatal(entries, e)
	}
}

func TestRetainedRealInventory(t *testing.T) {
	t.Parallel()
	stage := testArtifactTree(t)
	testArtifactWrite(t, stage, "dist/web/é\"\\\n<&>", "bytes")
	testArtifactLink(t, stage, "node_modules/alias", "pkg")
	// Author the sorted inventory separately: no production serializer or JSON encoder.
	known := []struct{ escapedPath, content string }{
		{`app.json`, testManifest}, {`dist/service/main.js`, "main"}, {`dist/service/migrate.js`, "migrate"}, {`dist/web/index.html`, "<html>"}, {`dist/web/é\"\\\u000a<&>`, "bytes"},
		{`node_modules/pkg/index.js`, "dependency"}, {`package-lock.json`, "{}"}, {`package.json`, "{}"}, {`sql/migrations/0002_first.sql`, "first"}, {`sql/migrations/0010_next.sql`, "next"},
	}
	var want strings.Builder
	want.WriteByte('[')
	for i, item := range known {
		if i > 0 {
			want.WriteByte(',')
		}
		if item.escapedPath == "node_modules/pkg/index.js" {
			want.WriteString(`{"path":"node_modules/alias","kind":"symlink","target":"pkg"},`)
		}
		fmt.Fprintf(&want, `{"path":"%s","kind":"file","size_bytes":%d,"sha256":"%x"}`, item.escapedPath, len(item.content), sha256.Sum256([]byte(item.content)))
	}
	want.WriteByte(']')
	result, e := ValidateArtifacts(context.Background(), stage, testHost, testApp)
	if e != nil {
		t.Fatal(e)
	}
	if string(result.Inventory) != want.String() {
		t.Fatalf("inventory got %s want %s", result.Inventory, want.String())
	}
	if result.BuildID != fmt.Sprintf("%x", sha256.Sum256([]byte(want.String()))) {
		t.Fatal("build digest")
	}
}
func TestRetainedLongLinks(t *testing.T) {
	t.Parallel()
	r := testArtifactTree(t)
	for i := 0; i < 50; i++ {
		target := fmt.Sprintf("link%d", i+1)
		if i == 49 {
			target = "pkg"
		}
		testArtifactLink(t, r, fmt.Sprintf("node_modules/link%d", i), target)
	}
	testArtifactLink(t, r, "node_modules/repeated", "link0/../link0/index.js")
	if _, e := ValidateArtifacts(context.Background(), r, testHost, testApp); e != nil {
		t.Fatal(e)
	}
}

func TestRetainedPublicContents(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"SQLite format 3\x00database", "-----BEGIN PRIVATE KEY-----\nsecret", "main", "migrate", "first"} {
		t.Run(fmt.Sprintf("%q", content), func(t *testing.T) {
			r := testArtifactTree(t)
			testArtifactWrite(t, r, "dist/web/assets/hidden.txt", content)
			if _, e := ValidateArtifacts(context.Background(), r, testHost, testApp); e == nil {
				t.Fatal("private output accepted as public")
			}
		})
	}
}

func testArtifactSnapshot(t *testing.T, root string) string {
	t.Helper()
	var snapshot strings.Builder
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		fmt.Fprintf(&snapshot, "%q:%v:", rel, info.Mode())
		if info.Mode().IsRegular() {
			fmt.Fprintf(&snapshot, "%x", sha256.Sum256(testBytes(t, p)))
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(p)
			if e != nil {
				return e
			}
			fmt.Fprintf(&snapshot, "%q", target)
		}
		snapshot.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.String()
}
func TestRetainedRejectedInstallCleanup(t *testing.T) {
	t.Parallel()
	stage, root := testArtifactTree(t), testArtifactTemp(t)
	testArtifactWrite(t, stage, "sql/migrations/0002_duplicate.sql", "duplicate")
	testArtifactWrite(t, root, testApp+"/data/app.sqlite", "persistent")
	testArtifactWrite(t, root, "registry.json", "registration/history")
	beforeSource := testArtifactSnapshot(t, stage)
	if _, e := InstallBuild(context.Background(), root, stage, testHost, testApp); e == nil {
		t.Fatal("accepted invalid staging")
	}
	if after := testArtifactSnapshot(t, stage); after != beforeSource {
		t.Fatal("source changed")
	}
	if string(testBytes(t, filepath.Join(root, testApp, "data/app.sqlite"))) != "persistent" || string(testBytes(t, filepath.Join(root, "registry.json"))) != "registration/history" {
		t.Fatal("persistent state changed")
	}
	entries, e := os.ReadDir(filepath.Join(root, testApp, "builds"))
	if e != nil || len(entries) != 0 {
		t.Fatal("partial build or temporary tree left", entries, e)
	}
}

func TestRetainedPinnedAccess(t *testing.T) {
	t.Parallel()
	stage := testArtifactTree(t)
	dir, e := privateDir(stage)
	if e != nil {
		t.Fatal(e)
	}
	defer dir.Close()
	outside := testArtifactTree(t)
	testArtifactWrite(t, outside, "dist/web/index.html", "outside")
	moved := filepath.Join(testArtifactTemp(t), "original")
	if e := os.Rename(stage, moved); e != nil {
		t.Fatal(e)
	}
	testArtifactLink(t, filepath.Dir(stage), filepath.Base(stage), outside)
	result, e := scanArtifacts(context.Background(), dir, nil, testHost, testApp, false)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(result.Inventory, []byte(fmt.Sprintf("%x", sha256.Sum256([]byte("<html>"))))) || bytes.Contains(result.Inventory, []byte(fmt.Sprintf("%x", sha256.Sum256([]byte("outside"))))) {
		t.Fatal("reopened replaced root path")
	}
	if _, e := ValidateArtifacts(context.Background(), stage, testHost, testApp); e == nil {
		t.Fatal("replacement root link accepted")
	}
}
