package apps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// privateDir opens each component without following symlinks. The returned
// descriptor pins the directory used by subsequent relative operations.
func privateDir(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrInvalidStorage
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("apps: open filesystem root: %w", err)
	}
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/") {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if e != nil {
			return nil, fmt.Errorf("apps: open directory component: %w", e)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}
func regularAt(dir *os.File, name string, create bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if create {
		flags = unix.O_RDWR | unix.O_CREAT | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	}
	fd, err := unix.Openat(int(dir.Fd()), name, flags, 0600)
	if err != nil {
		return nil, fmt.Errorf("apps: open regular file: %w", err)
	}
	f := os.NewFile(uintptr(fd), name)
	s, err := f.Stat()
	if err == nil && !s.Mode().IsRegular() {
		err = ErrInvalidStorage
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("apps: inspect regular file: %w", err)
	}
	return f, nil
}
func selectBuild(ctx context.Context, root string, r Record) (_ *os.File, name string, err error) {
	builds := filepath.Join(root, r.AppID, "builds")
	parent, err := privateDir(builds)
	if err != nil {
		return nil, "", fmt.Errorf("apps: open builds directory: %w", err)
	}
	defer parent.Close()
	entries, err := parent.ReadDir(-1)
	if err != nil {
		return nil, "", fmt.Errorf("apps: read builds directory: %w", err)
	}
	var selected *os.File
	defer func() {
		if err != nil && selected != nil {
			_ = selected.Close()
		}
	}()
	selectedName := ""
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return nil, "", err
		}
		name := entry.Name()
		if len(name) != 64 || strings.Trim(name, "0123456789abcdef") != "" || !entry.IsDir() {
			return nil, "", ErrInvalidManifest
		}
		dir, e := directoryAt(parent, name)
		if e != nil {
			return nil, "", fmt.Errorf("apps: open build directory: %w", e)
		}
		file, e := regularAt(dir, "app.json", false)
		if e != nil {
			_ = dir.Close()
			return nil, "", fmt.Errorf("apps: open build manifest: %w", e)
		}
		bytes, e := io.ReadAll(io.LimitReader(file, 16385))
		_ = file.Close()
		if e != nil {
			_ = dir.Close()
			return nil, "", fmt.Errorf("apps: read build manifest: %w", e)
		}
		manifest, e := ValidateManifest(bytes, r.Manifest.ServerID())
		if e != nil {
			_ = dir.Close()
			return nil, "", fmt.Errorf("apps: validate build manifest: %w", e)
		}
		if manifest.AppID() != r.AppID {
			_ = dir.Close()
			return nil, "", ErrInvalidIdentity
		}
		if manifest.ReleaseVersion() != r.ActiveRelease {
			_ = dir.Close()
			continue
		}
		if selected != nil {
			_ = dir.Close()
			return nil, "", errors.New("apps: ambiguous committed release")
		}
		selected, selectedName = dir, name
	}
	if selected == nil {
		return nil, "", errors.New("apps: committed release missing")
	}
	return selected, selectedName, nil
}

func prepareRelease(ctx context.Context, root string, r Record, node string) (string, string, error) {
	build, name, err := selectBuild(ctx, root, r)
	if err != nil {
		return "", "", err
	}
	_ = build.Close()
	selected := filepath.Join(root, r.AppID, "builds", name)
	service, err := privateDir(filepath.Join(selected, "dist/service"))
	if err != nil {
		return "", "", fmt.Errorf("apps: open service directory: %w", err)
	}
	entry, err := regularAt(service, "main.js", false)
	_ = service.Close()
	if err != nil {
		return "", "", fmt.Errorf("apps: open service entry: %w", err)
	}
	_ = entry.Close()
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(versionCtx, node, "--version")
	command.Env = []string{}
	command.WaitDelay = time.Second
	version, err := command.Output()
	if err != nil {
		return "", "", fmt.Errorf("apps: node version: %w", err)
	}
	if !supportedNode(strings.TrimSpace(string(version))) {
		return "", "", errors.New("apps: unsupported node runtime")
	}
	app, err := privateDir(filepath.Join(root, r.AppID))
	if err != nil {
		return "", "", fmt.Errorf("apps: open app directory: %w", err)
	}
	defer app.Close()
	if err = unix.Mkdirat(int(app.Fd()), "data", 0700); err != nil && !errors.Is(err, unix.EEXIST) {
		return "", "", fmt.Errorf("apps: create data directory: %w", err)
	}
	data := filepath.Join(root, r.AppID, "data")
	dir, err := privateDir(data)
	if err != nil {
		return "", "", fmt.Errorf("apps: open data directory: %w", err)
	}
	defer dir.Close()
	if err = dir.Chmod(0700); err != nil {
		return "", "", fmt.Errorf("apps: set data directory mode: %w", err)
	}
	db, err := regularAt(dir, "app.sqlite", true)
	if err != nil {
		return "", "", fmt.Errorf("apps: open app database: %w", err)
	}
	defer db.Close()
	if err = db.Chmod(0600); err != nil {
		return "", "", fmt.Errorf("apps: set app database mode: %w", err)
	}
	return selected, data, nil
}
func supportedNode(version string) bool {
	if !strings.HasPrefix(version, "v") || !validRelease(version[1:]) {
		return false
	}
	var major, minor, patch int
	if _, err := fmt.Sscanf(version, "v%d.%d.%d", &major, &minor, &patch); err != nil {
		return false
	}
	return major == 24 && minor >= 15
}
