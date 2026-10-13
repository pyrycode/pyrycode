package apps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func assetFixture(t *testing.T) (*Registry, *Owner, string) {
	t.Helper()
	r, o := responseOwner(t)
	web := filepath.Join(r.root, testApp, "builds", strings.Repeat("a", 64), "dist/web")
	if err := os.MkdirAll(web, 0700); err != nil {
		t.Fatal(err)
	}
	assetWrite(t, web, "index.html", []byte("<html>active</html>"))
	return r, o, web
}
func assetWrite(t *testing.T, root, name string, body []byte) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0600); err != nil {
		t.Fatal(err)
	}
}
func assetRequest(path string, b *responseBudget) AssetRequest {
	return AssetRequest{Path: path, Reserve: b.reserve, Release: b.release}
}
func assetCheck(t *testing.T, p *PreparedResponse, b *responseBudget, status int, mime string, body []byte) {
	t.Helper()
	defer p.Close()
	m, err := p.Metadata()
	if err != nil || m.Status != status || m.Headers["content-type"] != mime || m.Size != int64(len(body)) || m.SHA256 != fmt.Sprintf("%x", sha256.Sum256(body)) {
		t.Fatalf("metadata %#v %v", m, err)
	}
	got, err := io.ReadAll(p)
	if err != nil || !bytes.Equal(got, body) || b.used() != int64(len(body)) {
		t.Fatalf("body size=%d err=%v held=%d", len(got), err, b.used())
	}
	if err = p.Complete(nil); err != nil {
		t.Fatal(err)
	}
	if b.used() != 0 {
		t.Fatal("retained capacity")
	}
}
func TestAssetStable(t *testing.T) {
	r, o, web := assetFixture(t)
	candidate := ownerBuild(t, r, testApp, "b", "2.0.0", "candidate")
	assetWrite(t, filepath.Join(candidate, "dist/web"), "index.html", []byte("candidate"))
	manifest := testMap(t)
	manifest["release_version"] = "2.0.0"
	if _, err := r.UpdateManifest(testApp, testJSON(t, manifest)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, mime string
		body       []byte
	}{
		{"index.html", "text/html; charset=utf-8", []byte("<html>active</html>")},
		{"assets/main.js", "text/javascript", []byte("import './module.mjs';")},
		{"assets/module.mjs", "text/javascript", []byte("export default 1;")},
		{"main.css", "text/css; charset=utf-8", []byte("body{}")},
		{"data.json", "application/json", []byte(`{"x":1}`)},
		{"image.png", "image/png", []byte{0, 255, 1}}, {"image.jpg", "image/jpeg", []byte{255}},
		{"image.svg", "image/svg+xml", []byte("<svg/>")}, {"image.gif", "image/gif", nil},
		{"image.webp", "image/webp", nil}, {"favicon.ico", "image/x-icon", nil},
		{"font.woff", "font/woff", nil}, {"font.woff2", "font/woff2", nil}, {"font.ttf", "font/ttf", nil}, {"font.otf", "font/otf", nil},
		{"module.wasm", "application/wasm", []byte{0, 97, 115, 109}}, {"opaque.bin", "application/octet-stream", []byte{0, 255}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assetWrite(t, web, tc.name, tc.body)
			b := &responseBudget{ceiling: 1000}
			in := assetRequest("/"+tc.name, b)
			in.Query = "v=1&v=2&empty=&x=%2F%25"
			p, err := o.PrepareAsset(context.Background(), testHost, testApp, "1.0.0", in)
			if err != nil {
				t.Fatal(err)
			}
			assetWrite(t, web, tc.name, []byte("changed"))
			v := ownerRecord(t, r, testApp)
			if _, err = r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: "renamed", Desired: v.Desired, State: v.State, ActiveRelease: v.ActiveRelease, PendingRelease: "2.0.0"}); err != nil {
				t.Fatal(err)
			}
			assetCheck(t, p, b, 200, tc.mime, tc.body)
		})
	}
}
func TestAssetNavigation(t *testing.T) {
	_, o, web := assetFixture(t)
	assetWrite(t, web, "present", []byte("public"))
	for _, tc := range []struct {
		path       string
		nav        bool
		status     int
		body, mime string
	}{
		{"/", false, 200, "<html>active</html>", "text/html; charset=utf-8"},
		{"/route/nested", true, 200, "<html>active</html>", "text/html; charset=utf-8"},
		{"/route/nested", false, 404, "", "text/plain; charset=utf-8"},
		{"/route/app.js", true, 404, "", "text/plain; charset=utf-8"},
		{"/route/app%2Ejs", true, 404, "", "text/plain; charset=utf-8"},
		{"/present", true, 200, "public", "application/octet-stream"},
	} {
		b := &responseBudget{ceiling: 1000}
		in := assetRequest(tc.path, b)
		in.Navigation = tc.nav
		p, err := o.PrepareAsset(context.Background(), testHost, testApp, "1.0.0", in)
		if err != nil {
			t.Fatal(tc.path, err)
		}
		assetCheck(t, p, b, tc.status, tc.mime, []byte(tc.body))
	}
	for _, root := range []string{"api", "_pyry", "service", "dist/service", "data", "registry.json", "source", "staging", "builds", "backups", "sql", "node_modules"} {
		for _, path := range []string{"/" + root, "/" + root + "/child", "/" + fmt.Sprintf("%%%02x", root[0]) + root[1:] + "/child"} {
			b := &responseBudget{ceiling: 100}
			in := assetRequest(path, b)
			in.Navigation = true
			p, err := o.PrepareAsset(context.Background(), testHost, testApp, "1.0.0", in)
			if p != nil || err != ErrAssetPath || b.used() != 0 {
				t.Fatal(path, p, err)
			}
		}
	}
}
func TestAssetSyntax(t *testing.T) {
	for _, path := range []string{"", "relative", "http://host/x", "//host/x", "/a//b", "/a\\b", "/a?x", "/a#x", "/a\x00", "/a\x7f", "/é", "/%", "/%xz", "/%2f", "/%5C", "/%25", "/.", "/..", "/a/./b", "/a/%2e%2e/b", "/%00", "/%0a", "/%5c", "/%C2%80", strings.Repeat("/", 2049)} {
		if _, err := validateAssetURL(path, ""); err != ErrAssetPath {
			t.Errorf("path %q: %v", path, err)
		}
	}
	for _, query := range []string{"?x", "x#fragment", "x\x00", "x\n", "é", "%", "%zz", strings.Repeat("x", 1025)} {
		if _, err := validateAssetURL("/ok", query); err != ErrAssetPath {
			t.Errorf("query %q: %v", query, err)
		}
	}
	for _, tc := range []struct{ path, query, decoded string }{
		{"/assets/a%20b.js", "x=%00&x=%2F&x=%25", "/assets/a b.js"},
		{"/", "", "/"}, {"/" + strings.Repeat("a", 2047), strings.Repeat("x", 1024), "/" + strings.Repeat("a", 2047)},
	} {
		if got, err := validateAssetURL(tc.path, tc.query); err != nil || got != tc.decoded {
			t.Fatal(got, err)
		}
	}
}
func TestAssetBuildSelection(t *testing.T) {
	for _, event := range []string{"missing", "ambiguous", "unsafe build", "unsafe manifest", "unsafe web"} {
		t.Run(event, func(t *testing.T) {
			r, o, web := assetFixture(t)
			build := filepath.Dir(filepath.Dir(web))
			var err error
			switch event {
			case "missing":
				err = os.Rename(build, filepath.Join(t.TempDir(), "saved"))
			case "ambiguous":
				ownerBuild(t, r, testApp, "b", "1.0.0", "duplicate")
			case "unsafe build":
				err = os.Rename(build, build+"-saved")
				if err == nil {
					err = os.Symlink(build+"-saved", build)
				}
			case "unsafe manifest":
				err = os.Remove(filepath.Join(build, "app.json"))
				if err == nil {
					err = os.Symlink(filepath.Join(build, "dist/service/main.js"), filepath.Join(build, "app.json"))
				}
			case "unsafe web":
				err = os.Rename(web, web+"-saved")
				if err == nil {
					err = os.Symlink(web+"-saved", web)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			b := &responseBudget{ceiling: 100}
			p, err := o.PrepareAsset(context.Background(), testHost, testApp, "1.0.0", assetRequest("/", b))
			if p != nil || err != ErrInvalidStorage || b.used() != 0 {
				t.Fatal(p, err)
			}
		})
	}
}
func TestAssetConfinement(t *testing.T) {
	_, o, web := assetFixture(t)
	outside := t.TempDir()
	assetWrite(t, outside, "secret", []byte("secret"))
	if err := os.Symlink(outside, filepath.Join(web, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(web, "final")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(web, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(web, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	assetWrite(t, web, "plain", nil)
	for _, name := range []string{"link/secret", "link/missing", "final", "fifo", "directory", "directory/", "plain/", "plain/child"} {
		b := &responseBudget{ceiling: 100}
		in := assetRequest("/"+name, b)
		in.Navigation = true
		if p, err := o.PrepareAsset(context.Background(), testHost, testApp, "1.0.0", in); p != nil || err != ErrInvalidStorage || b.used() != 0 {
			t.Fatal(name, p, err)
		}
	}
	root, err := privateDir(web)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	assetWrite(t, web, "nested/entry", []byte("pinned"))
	f, err := assetFile(root, "nested/entry")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(web, "nested"), filepath.Join(web, "saved")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(web, "nested")); err != nil {
		t.Fatal(err)
	}
	if _, err = assetFile(root, "nested/secret"); err == nil {
		t.Fatal("followed intermediate replacement")
	}
	if err = os.Remove(filepath.Join(web, "saved/entry")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(outside, "secret"), filepath.Join(web, "saved/entry")); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(got) != "pinned" {
		t.Fatal(string(got), err)
	}
	if _, err = assetFile(root, "saved/entry"); err == nil {
		t.Fatal("followed final replacement")
	}
}
func TestAssetLimits(t *testing.T) {
	_, o, web := assetFixture(t)
	for _, event := range []string{"exact", "oversized", "growing", "refused", "probe refused", "cancelled"} {
		t.Run(event, func(t *testing.T) {
			size := int64(8 << 20)
			if event == "oversized" {
				size++
			}
			if event == "growing" {
				size = 1
			}
			path := filepath.Join(web, "large.bin")
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			err = f.Truncate(size)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			b := &responseBudget{ceiling: (8 << 20) + 1}
			if event == "refused" {
				b.ceiling = 0
			}
			if event == "probe refused" {
				b.ceiling = 8 << 20
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			in := assetRequest("/large.bin", b)
			in.Reserve = func(n int64) bool {
				if event == "growing" {
					if err := os.Truncate(path, (8<<20)+1); err != nil {
						t.Error(err)
					}
				}
				if event == "cancelled" {
					cancel()
				}
				return b.reserve(n)
			}
			p, err := o.PrepareAsset(ctx, testHost, testApp, "1.0.0", in)
			want := map[string]error{"oversized": ErrResponseLimit, "growing": ErrResponseLimit, "refused": ErrResponseCapacity, "probe refused": ErrResponseCapacity, "cancelled": context.Canceled}[event]
			if !errors.Is(err, want) {
				t.Fatal(err, want)
			}
			if p != nil {
				assetCheck(t, p, b, 200, "application/octet-stream", make([]byte, 8<<20))
			}
			if b.used() != 0 {
				t.Fatal("leaked reservation")
			}
		})
	}
}
func TestAssetLookupRestart(t *testing.T) {
	r, o, web := assetFixture(t)
	target, record, err := o.responseTarget(testHost, testApp, "1.0.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(web, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if err = o.Stop(testApp); err != nil {
		t.Fatal(err)
	}
	healthStart(t, r, o)
	healthTarget(t, o)
	b := &responseBudget{ceiling: 100}
	p, err := o.prepareAsset(context.Background(), target, record, assetRequest("/", b))
	if p != nil || err != ErrUnavailable || b.used() != 0 {
		t.Fatal(p, err)
	}
	in := responseInput(file, 19, assetLimit, b)
	if q, err := o.prepare(context.Background(), testHost, testApp, "1.0.0", in, target); q != nil || err != ErrUnavailable {
		t.Fatal(q, err)
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) || b.used() != 0 {
		t.Fatal("stale source retained", err)
	}
	p, err = o.PrepareAsset(context.Background(), testHost, testApp, "1.0.0", assetRequest("/", b))
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
}
func TestAssetRetirement(t *testing.T) {
	for _, event := range []string{"remove", "release", "stop", "exit", "retire"} {
		t.Run(event, func(t *testing.T) {
			r, o, _ := assetFixture(t)
			g := healthTarget(t, o).generation
			b := &responseBudget{ceiling: 100}
			p, err := o.PrepareAsset(context.Background(), testHost, testApp, "1.0.0", assetRequest("/", b))
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			switch event {
			case "remove":
				_, err = r.Remove(testApp)
			case "release":
				v := ownerRecord(t, r, testApp)
				_, err = r.UpdateLifecycle(testHost, testApp, Lifecycle{Title: v.Title, Desired: v.Desired, State: v.State, ActiveRelease: "2.0.0"})
			case "stop":
				err = o.Stop(testApp)
			case "exit":
				err = os.WriteFile(filepath.Join(r.root, testApp, "data/exit"), nil, 0600)
			case "retire":
				g.cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			healthWait(t, func() bool { return b.used() == 0 })
			if _, err = p.Metadata(); err == nil {
				t.Fatal("stale metadata")
			}
			if n, err := p.Read(make([]byte, 1)); n != 0 || err == nil {
				t.Fatal("stale read")
			}
		})
	}
}

func TestAssetResolution(t *testing.T) {
	r, o, _ := assetFixture(t)
	for _, tc := range []struct {
		host, app, release string
		want               error
	}{
		{"foreign", testApp, "2.0.0", ErrNotFound},
		{testHost, "unknown", "1.0.0", ErrNotFound},
		{testHost, testApp, "2.0.0", ErrReleaseChanged},
	} {
		b := &responseBudget{ceiling: 100}
		if p, err := o.PrepareAsset(context.Background(), tc.host, tc.app, tc.release, assetRequest("/", b)); p != nil || err != tc.want || b.used() != 0 {
			t.Fatal(p, err)
		}
	}
	if err := o.Stop(testApp); err != nil {
		t.Fatal(err)
	}
	b := &responseBudget{ceiling: 100}
	if p, err := o.PrepareAsset(context.Background(), testHost, testApp, "1.0.0", assetRequest("/", b)); p != nil || err != ErrUnavailable {
		t.Fatal(p, err)
	}
	if _, err := r.Remove(testApp); err != nil {
		t.Fatal(err)
	}
	if p, err := o.PrepareAsset(context.Background(), testHost, testApp, "1.0.0", assetRequest("/", b)); p != nil || err != ErrNotFound {
		t.Fatal(p, err)
	}
}
