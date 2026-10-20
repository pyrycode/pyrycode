package claudetape

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type mcpFileEdge struct {
	parent *os.File
	name   string
	file   *os.File
}
type mcpFileTree struct {
	home     string
	root     *os.File
	rootInfo os.FileInfo
	edges    []mcpFileEdge
}

func openMCPFileTree(home string) (*mcpFileTree, error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return nil, mcpError()
	}
	before, e := os.Lstat(home)
	if e != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm() != 0700 {
		return nil, mcpError()
	}
	fd, e := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, mcpError()
	}
	root := os.NewFile(uintptr(fd), "owned MCP HOME")
	actual, e := root.Stat()
	if e != nil || !os.SameFile(before, actual) {
		root.Close()
		return nil, mcpError()
	}
	return &mcpFileTree{home: home, root: root, rootInfo: actual}, nil
}
func (t *mcpFileTree) Close() {
	for i := len(t.edges) - 1; i >= 0; i-- {
		t.edges[i].file.Close()
	}
	t.root.Close()
}
func (t *mcpFileTree) open(parent *os.File, name string, directory bool) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return nil, mcpError()
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, e := unix.Openat(int(parent.Fd()), name, flags, 0)
	if e != nil {
		return nil, mcpError()
	}
	f := os.NewFile(uintptr(fd), "owned MCP child")
	info, e := f.Stat()
	if e != nil || directory && (!info.IsDir() || info.Mode().Perm() != 0700) || !directory && !info.Mode().IsRegular() {
		f.Close()
		return nil, mcpError()
	}
	t.edges = append(t.edges, mcpFileEdge{parent, name, f})
	return f, nil
}
func (t *mcpFileTree) dir(names ...string) (*os.File, error) {
	parent := t.root
	for _, name := range names {
		var e error
		parent, e = t.open(parent, name, true)
		if e != nil {
			return nil, e
		}
	}
	return parent, nil
}
func (t *mcpFileTree) read(parent *os.File, name string, size int) ([]byte, error) {
	f, e := t.open(parent, name, false)
	if e != nil {
		return nil, e
	}
	return mcpReadStableFile(f, size, true)
}
func mcpReadStableFile(f *os.File, limit int, exact bool) ([]byte, error) {
	before, e := f.Stat()
	if e != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() < 0 || before.Size() > int64(limit) || exact && before.Size() != int64(limit) {
		return nil, mcpError()
	}
	raw, e := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if e != nil || len(raw) > limit || exact && len(raw) != limit {
		return nil, mcpError()
	}
	after, e := f.Stat()
	if e != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return nil, mcpError()
	}
	return raw, nil
}
func (t *mcpFileTree) check() error {
	current, e := os.Lstat(t.home)
	if e != nil || !os.SameFile(current, t.rootInfo) || !current.IsDir() || current.Mode().Perm() != 0700 {
		return mcpError()
	}
	for _, edge := range t.edges {
		var linked, opened unix.Stat_t
		if unix.Fstatat(int(edge.parent.Fd()), edge.name, &linked, unix.AT_SYMLINK_NOFOLLOW) != nil || unix.Fstat(int(edge.file.Fd()), &opened) != nil || linked.Dev != opened.Dev || linked.Ino != opened.Ino || linked.Mode != opened.Mode || linked.Mode&unix.S_IFMT == unix.S_IFLNK || linked.Mode&unix.S_IFMT == unix.S_IFREG && linked.Size != opened.Size {
			return mcpError()
		}
	}
	return nil
}

// Native MCP configs can live in the OS temp directory. Open the current exact
// argv leaf without following a symlink, bound the read and recheck its linked
// identity. Its whole raw document is validated before any JSON normalization.
func readMCPCurrentConfig(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, mcpError()
	}
	before, e := os.Lstat(path)
	if e != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 {
		return nil, mcpError()
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, mcpError()
	}
	f := os.NewFile(uintptr(fd), "current MCP config")
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(before, opened) {
		return nil, mcpError()
	}
	raw, e := mcpReadStableFile(f, MaxBytes, false)
	if e != nil {
		return nil, e
	}
	after, e := os.Lstat(path)
	if e != nil || !os.SameFile(opened, after) || !after.Mode().IsRegular() || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return nil, mcpError()
	}
	return raw, nil
}
