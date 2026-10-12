package apps

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// RetainedBuild describes checked artifact bytes, not preparation success or readiness.
// Inventory and Migrations are detached from package state.
type RetainedBuild struct {
	Manifest   Manifest
	BuildID    string
	Inventory  []byte
	Migrations []string
}
type artifactEntry struct {
	path, kind, target, digest string
	size                       int64
}
type artifactScan struct {
	entries    []artifactEntry
	nodes      map[string]artifactEntry
	resolved   map[string][]string
	manifest   []byte
	migrations []string
	stored     []byte
	strict     bool
}

type artifactReader struct {
	ctx    context.Context
	source io.Reader
}

func (r artifactReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}

var migrationName = regexp.MustCompile(`^[0-9]{4}_[a-z0-9_]+\.sql$`)

// ValidateArtifacts checks only retained artifacts in a completed private staging tree.
// It runs no commands and modifies neither staging nor registration state.
func ValidateArtifacts(ctx context.Context, staging, serverID, appID string) (build RetainedBuild, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("apps: validate retained artifacts: %w", err)
		}
	}()
	dir, err := privateDir(staging)
	if err != nil {
		return RetainedBuild{}, err
	}
	defer dir.Close()
	return scanArtifacts(ctx, dir, nil, serverID, appID, false)
}
func scanArtifacts(ctx context.Context, dir, dst *os.File, serverID, appID string, strict bool) (RetainedBuild, error) {
	s := artifactScan{nodes: map[string]artifactEntry{}, strict: strict}
	if !validID(appID) || !validID(serverID) {
		return RetainedBuild{}, ErrInvalidIdentity
	}
	if err := s.walk(ctx, dir, dst, ""); err != nil {
		return RetainedBuild{}, fmt.Errorf("apps: retained artifacts: %w", err)
	}
	for _, p := range []string{"app.json", "package.json", "package-lock.json", "dist/web/index.html", "dist/service/main.js", "dist/service/migrate.js"} {
		if s.nodes[p].kind != "file" {
			return RetainedBuild{}, ErrInvalidStorage
		}
	}
	for _, p := range []string{"dist/web", "dist/service", "sql/migrations", "node_modules"} {
		if s.nodes[p].kind != "directory" {
			return RetainedBuild{}, ErrInvalidStorage
		}
	}
	manifest, err := ValidateManifest(s.manifest, serverID)
	if err != nil {
		return RetainedBuild{}, err
	}
	if manifest.AppID() != appID {
		return RetainedBuild{}, ErrInvalidIdentity
	}
	for _, entry := range s.entries {
		if entry.kind == "symlink" {
			if err := s.resolve(ctx, entry.path); err != nil {
				return RetainedBuild{}, err
			}
		}
	}
	privateDigests := map[string]bool{}
	for _, entry := range s.entries {
		if entry.kind == "file" && (strings.HasPrefix(entry.path, "dist/service/") || strings.HasPrefix(entry.path, "sql/migrations/")) {
			privateDigests[entry.digest] = true
		}
	}
	for _, entry := range s.entries {
		if strings.HasPrefix(entry.path, "dist/web/") && privateDigests[entry.digest] {
			return RetainedBuild{}, ErrInvalidStorage
		}
	}
	sort.Strings(s.migrations)
	for i := 1; i < len(s.migrations); i++ {
		if path.Base(s.migrations[i])[:4] == path.Base(s.migrations[i-1])[:4] {
			return RetainedBuild{}, ErrInvalidStorage
		}
	}
	inventory := canonicalInventory(s.entries)
	if strict && string(s.stored) != string(inventory) {
		return RetainedBuild{}, ErrInvalidStorage
	}
	result := RetainedBuild{Manifest: manifest, BuildID: digestBytes(inventory), Inventory: inventory, Migrations: s.migrations}
	if err := ctx.Err(); err != nil {
		return RetainedBuild{}, err
	}
	return result, nil
}
func retainedPath(p string) bool {
	switch p {
	case "app.json", "package.json", "package-lock.json", "dist", "dist/web", "dist/service", "sql", "sql/migrations", "node_modules":
		return true
	}
	for _, root := range []string{"dist/web/", "dist/service/", "sql/migrations/", "node_modules/"} {
		if strings.HasPrefix(p, root) {
			return true
		}
	}
	return false
}
func publicArtifact(p string) bool {
	for _, part := range strings.Split(strings.ToLower(strings.TrimPrefix(p, "dist/web/")), "/") {
		switch part {
		case ".git", ".ssh", ".aws", ".cache", ".npm", "node_modules", "service", "sql", "package.json", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb", "app.json", ".npmrc", "secrets", "secrets.json", "credentials", "credentials.json", "id_rsa", "id_ed25519":
			return false
		}
		if strings.HasPrefix(part, ".env") {
			return false
		}
		for _, ext := range []string{".sql", ".db", ".sqlite", ".sqlite3", ".db-wal", ".db-shm", ".sqlite-wal", ".sqlite-shm", ".sqlite3-wal", ".sqlite3-shm", ".pem", ".key", ".p12", ".pfx"} {
			if strings.HasSuffix(part, ext) {
				return false
			}
		}
	}
	return true
}
func artifactDirAt(parent *os.File, name string, create bool) (*os.File, error) {
	if create {
		if e := unix.Mkdirat(int(parent.Fd()), name, 0700); e != nil && e != unix.EEXIST {
			return nil, fmt.Errorf("apps: create artifact directory %q: %w", name, e)
		}
	}
	fd, e := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, fmt.Errorf("apps: open artifact directory %q: %w", name, e)
	}
	return os.NewFile(uintptr(fd), name), nil
}
func (s *artifactScan) walk(ctx context.Context, dir, dst *os.File, prefix string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	if s.strict && info.Mode().Perm()&0222 != 0 {
		return ErrInvalidStorage
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		p := name
		if prefix != "" {
			p = prefix + "/" + name
		}
		if !utf8.ValidString(p) {
			return ErrInvalidStorage
		}
		if p == "inventory.json" && s.strict {
			f, e := regularAt(dir, name, false)
			if e != nil {
				return e
			}
			s.stored, e = io.ReadAll(artifactReader{ctx, f})
			info, se := f.Stat()
			_ = f.Close()
			if e != nil {
				return e
			}
			if se != nil {
				return se
			}
			if info.Mode().Perm()&0222 != 0 {
				return ErrInvalidStorage
			}
			continue
		}
		if !retainedPath(p) {
			if s.strict {
				return ErrInvalidStorage
			}
			continue
		}
		if strings.HasPrefix(p, "dist/web/") && !publicArtifact(p) {
			return ErrInvalidStorage
		}
		var st unix.Stat_t
		if e := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); e != nil {
			return e
		}
		item := artifactEntry{path: p}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if strings.HasPrefix(p, "sql/migrations/") {
				return ErrInvalidStorage
			}
			switch p {
			case "app.json", "package.json", "package-lock.json", "dist/web/index.html", "dist/service/main.js", "dist/service/migrate.js":
				return ErrInvalidStorage
			}
			item.kind = "directory"
			child, e := artifactDirAt(dir, name, false)
			if e != nil {
				return e
			}
			var target *os.File
			if dst != nil {
				target, e = artifactDirAt(dst, name, true)
			}
			if e == nil {
				e = s.walk(ctx, child, target, p)
			}
			_ = child.Close()
			if target != nil {
				if e == nil {
					e = target.Chmod(0500)
				}
				if e == nil {
					e = target.Sync()
				}
				_ = target.Close()
			}
			if e != nil {
				return e
			}
		case unix.S_IFREG:
			switch p {
			case "dist", "dist/web", "dist/service", "sql", "sql/migrations", "node_modules":
				return ErrInvalidStorage
			}
			if strings.HasPrefix(p, "sql/migrations/") {
				if !migrationName.MatchString(name) {
					return ErrInvalidStorage
				}
				s.migrations = append(s.migrations, p)
			}
			f, e := regularAt(dir, name, false)
			if e != nil {
				return e
			}
			info, e := f.Stat()
			if e != nil {
				_ = f.Close()
				return e
			}
			if s.strict && info.Mode().Perm()&0222 != 0 {
				_ = f.Close()
				return ErrInvalidStorage
			}
			var out *os.File
			if dst != nil {
				out, e = regularAt(dst, name, true)
			}
			h := sha256.New()
			var writer io.Writer = h
			if out != nil {
				writer = io.MultiWriter(h, out)
			}
			if e == nil && p == "app.json" {
				s.manifest, e = io.ReadAll(io.LimitReader(artifactReader{ctx, f}, 16385))
				if e == nil {
					item.size = int64(len(s.manifest))
					_, e = writer.Write(s.manifest)
				}
			} else if e == nil {
				if strings.HasPrefix(p, "dist/web/") {
					prefix, pe := io.ReadAll(io.LimitReader(artifactReader{ctx, f}, 512))
					e = pe
					text := string(prefix)
					if e == nil && (strings.HasPrefix(text, "SQLite format 3\x00") || (strings.Contains(text, "-----BEGIN ") && strings.Contains(text, "PRIVATE KEY-----"))) {
						e = ErrInvalidStorage
					}
					if e == nil {
						n, we := writer.Write(prefix)
						item.size = int64(n)
						e = we
					}
				}
				if e == nil {
					n, ce := io.Copy(writer, artifactReader{ctx, f})
					item.size += n
					e = ce
				}
			}
			_ = f.Close()
			if out != nil {
				if e == nil {
					mode := os.FileMode(0400)
					if info.Mode().Perm()&0100 != 0 {
						mode = 0500
					}
					e = out.Chmod(mode)
				}
				if e == nil {
					e = out.Sync()
				}
				ce := out.Close()
				if e == nil {
					e = ce
				}
			}
			if e != nil {
				return e
			}
			item.kind = "file"
			item.digest = fmt.Sprintf("%x", h.Sum(nil))
		case unix.S_IFLNK:
			if !strings.HasPrefix(p, "node_modules/") {
				return ErrInvalidStorage
			}
			buf := make([]byte, 65536)
			n, e := unix.Readlinkat(int(dir.Fd()), name, buf)
			if e != nil {
				return e
			}
			if n == len(buf) {
				return ErrInvalidStorage
			}
			item.kind = "symlink"
			item.target = string(buf[:n])
			if !utf8.ValidString(item.target) || item.target == "" || path.IsAbs(item.target) {
				return ErrInvalidStorage
			}
			if dst != nil {
				if e := unix.Symlinkat(item.target, int(dst.Fd()), name); e != nil {
					return e
				}
			}
		default:
			return ErrInvalidStorage
		}
		s.nodes[p] = item
		if item.kind != "directory" {
			s.entries = append(s.entries, item)
		}
	}
	return nil
}

// resolve interprets every component before processing '..', including components
// reached through other links. Completed link resolutions are reused within this
// scan; cached paths are immutable. No filesystem access follows an artifact link.
func (s *artifactScan) resolve(ctx context.Context, name string) error {
	if s.resolved == nil {
		s.resolved = map[string][]string{}
	}
	_, err := s.resolveParts(ctx, []string{"node_modules"}, strings.Split(strings.TrimPrefix(name, "node_modules/"), "/"), map[string]bool{})
	return err
}
func (s *artifactScan) resolveParts(ctx context.Context, current, pending []string, active map[string]bool) ([]string, error) {
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		part := pending[0]
		pending = pending[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if len(current) <= 1 {
				return nil, ErrInvalidStorage
			}
			current = current[:len(current)-1]
			continue
		}
		next := append(append([]string{}, current...), part)
		p := strings.Join(next, "/")
		node, ok := s.nodes[p]
		if !ok {
			return nil, ErrInvalidStorage
		}
		if node.kind == "symlink" {
			resolved, cached := s.resolved[p]
			if !cached {
				if active[p] {
					return nil, ErrInvalidStorage
				}
				active[p] = true
				var err error
				resolved, err = s.resolveParts(ctx, current, strings.Split(node.target, "/"), active)
				delete(active, p)
				if err != nil {
					return nil, err
				}
				s.resolved[p] = resolved
			}
			current = resolved
		} else {
			current = next
		}
		if len(pending) > 0 && s.nodes[strings.Join(current, "/")].kind != "directory" {
			return nil, ErrInvalidStorage
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return current, nil
}
func digestBytes(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func inventoryString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range []byte(s) {
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 32:
			fmt.Fprintf(&b, `\u00%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
func canonicalInventory(entries []artifactEntry) []byte {
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	var b strings.Builder
	b.WriteByte('[')
	for i, e := range entries {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"path":%s,"kind":%s`, inventoryString(e.path), inventoryString(e.kind))
		if e.kind == "file" {
			fmt.Fprintf(&b, `,"size_bytes":%d,"sha256":%s}`, e.size, inventoryString(e.digest))
		} else {
			fmt.Fprintf(&b, `,"target":%s}`, inventoryString(e.target))
		}
	}
	b.WriteByte(']')
	return []byte(b.String())
}
