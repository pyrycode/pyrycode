package apps

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

const assetLimit = 8 << 20

var ErrAssetPath = errors.New("apps: invalid asset path or query")

// AssetRequest carries native-stamped navigation and escaped origin-relative URL text.
// Query is optional opaque text, never used for file selection. Reserve/Release have
// the ResponseInput contract. The caller supplies authenticated host/app identity.
type AssetRequest struct {
	Path, Query string
	Navigation  bool
	Reserve     func(int64) bool
	Release     func(int64)
}

// PrepareAsset stages a confined active-release asset or ordinary HTTP 404.
// Consumers use checked Metadata/Read and Complete/Close, as with Prepare.
func (o *Owner) PrepareAsset(ctx context.Context, serverID, appID, release string, in AssetRequest) (*PreparedResponse, error) {
	target, record, err := o.responseTarget(serverID, appID, release, nil)
	if err != nil {
		return nil, err
	}
	return o.prepareAsset(ctx, target, record, in)
}
func (o *Owner) prepareAsset(ctx context.Context, target *Target, record Record, in AssetRequest) (*PreparedResponse, error) {
	decoded, err := validateAssetURL(in.Path, in.Query)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if !target.valid() {
		return nil, ErrUnavailable
	}
	build, _, err := selectBuild(ctx, o.registry.root, record)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrInvalidStorage
	}
	defer build.Close()
	dist, err := directoryAt(build, "dist")
	if err != nil {
		return nil, ErrInvalidStorage
	}
	defer dist.Close()
	web, err := directoryAt(dist, "web")
	if err != nil {
		return nil, ErrInvalidStorage
	}
	defer web.Close()
	name := strings.TrimPrefix(decoded, "/")
	if name == "" {
		name = "index.html"
	}
	f, err := assetFile(web, name)
	if errors.Is(err, os.ErrNotExist) && in.Navigation && path.Ext(decoded) == "" {
		name = "index.html"
		f, err = assetFile(web, name)
	}
	response := ResponseInput{Limit: assetLimit, Status: 200, Headers: map[string]string{"content-type": assetMIME(name)}, Reserve: in.Reserve, Release: in.Release}
	switch {
	case errors.Is(err, os.ErrNotExist):
		response.Body = io.NopCloser(strings.NewReader(""))
		response.Size, response.Status = 0, 404
		response.Headers["content-type"] = "text/plain; charset=utf-8"
	case err != nil:
		return nil, ErrInvalidStorage
	default:
		stat, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, ErrResponseIO
		}
		response.Body, response.Size = f, stat.Size()
	}
	return o.prepare(ctx, record.Manifest.ServerID(), record.AppID, record.ActiveRelease, response, target)
}

// directoryAt opens one component, pinning it without a check/open gap.
func directoryAt(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func assetFile(root *os.File, name string) (*os.File, error) {
	current := root
	defer func() {
		if current != root {
			_ = current.Close()
		}
	}()
	parts := strings.Split(strings.TrimSuffix(name, "/"), "/")
	for _, part := range parts[:len(parts)-1] {
		next, err := directoryAt(current, part)
		if err != nil {
			return nil, err
		}
		if current != root {
			_ = current.Close()
		}
		current = next
	}
	f, err := regularAt(current, parts[len(parts)-1], false)
	if err == nil && strings.HasSuffix(name, "/") {
		_ = f.Close()
		return nil, ErrInvalidStorage
	}
	return f, err
}
func assetMIME(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".svg":
		return "image/svg+xml"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".avif":
		return "image/avif"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".otf":
		return "font/otf"
	case ".wasm":
		return "application/wasm"
	default:
		return "application/octet-stream"
	}
}
