package apps

import (
	"context"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"golang.org/x/sys/unix"
)

func candidateStaging(ctx context.Context, source string) (string, func(), error) {
	src, err := privateDir(source)
	if err != nil {
		return "", nil, err
	}
	defer src.Close()
	app, err := privateDir(filepath.Dir(source))
	if err != nil {
		return "", nil, err
	}
	defer app.Close()
	parent, err := artifactDirAt(app, "staging", true)
	if err != nil {
		return "", nil, err
	}
	name := rand.Text()
	if err = unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil {
		parent.Close()
		return "", nil, err
	}
	cleanup := func() { removeArtifactTemp(parent, name); parent.Close() }
	dst, err := childDir(parent, name)
	if err == nil {
		err = copyCandidateSource(ctx, src, dst, true)
		dst.Close()
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return filepath.Join(filepath.Dir(source), "staging", name), cleanup, nil
}
func copyCandidateSource(ctx context.Context, src, dst *os.File, root bool) error {
	entries, err := src.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if root && (name == "node_modules" || name == "dist") {
			continue
		}
		var st unix.Stat_t
		if err = unix.Fstatat(int(src.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			child, e := childDir(src, name)
			if e != nil {
				return e
			}
			target, e := artifactDirAt(dst, name, true)
			if e == nil {
				e = copyCandidateSource(ctx, child, target, false)
				target.Close()
			}
			child.Close()
			if e != nil {
				return e
			}
		case unix.S_IFREG:
			f, e := regularAt(src, name, false)
			if e != nil {
				return e
			}
			info, e := f.Stat()
			if e != nil {
				f.Close()
				return e
			}
			out, e := regularAt(dst, name, true)
			if e == nil {
				_, e = io.Copy(out, artifactReader{ctx, f})
				if e == nil {
					e = out.Chmod(0600 | info.Mode().Perm()&0100)
				}
				ce := out.Close()
				if e == nil {
					e = ce
				}
			}
			f.Close()
			if e != nil {
				return e
			}
		default:
			return ErrInvalidStorage
		}
	}
	return ctx.Err()
}
func candidateInputs(stage, serverID, appID string) (Manifest, error) {
	raw, err := candidateRead(stage, "app.json")
	if err != nil {
		return Manifest{}, err
	}
	m, err := ValidateManifest(raw, serverID)
	if err != nil {
		return Manifest{}, err
	}
	if m.AppID() != appID {
		return Manifest{}, ErrInvalidIdentity
	}
	read := func(name string) (map[string]any, error) {
		b, e := candidateRead(stage, name)
		if e != nil {
			return nil, e
		}
		v, e := strictJSON(b)
		if e != nil {
			return nil, e
		}
		o, ok := v.(map[string]any)
		if !ok {
			return nil, ErrInvalidManifest
		}
		return o, nil
	}
	pkg, err := read("package.json")
	if err != nil {
		return Manifest{}, err
	}
	lock, err := read("package-lock.json")
	if err != nil {
		return Manifest{}, err
	}
	packages, ok := lock["packages"].(map[string]any)
	if !ok {
		return Manifest{}, ErrInvalidManifest
	}
	root, ok := packages[""].(map[string]any)
	if !ok || pkg["type"] != "module" {
		return Manifest{}, ErrInvalidManifest
	}
	for _, key := range []string{"name", "version"} {
		s, ok := pkg[key].(string)
		if !ok || s == "" || lock[key] != s || root[key] != s {
			return Manifest{}, ErrInvalidManifest
		}
	}
	for _, key := range []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"} {
		maps := []map[string]any{}
		for _, o := range []map[string]any{pkg, root} {
			v, exists := o[key]
			d, ok := v.(map[string]any)
			if exists && !ok {
				return Manifest{}, ErrInvalidManifest
			}
			if d == nil {
				d = map[string]any{}
			}
			for _, value := range d {
				if _, ok := value.(string); !ok {
					return Manifest{}, ErrInvalidManifest
				}
			}
			maps = append(maps, d)
		}
		if !reflect.DeepEqual(maps[0], maps[1]) {
			return Manifest{}, ErrInvalidManifest
		}
	}
	return m, nil
}
