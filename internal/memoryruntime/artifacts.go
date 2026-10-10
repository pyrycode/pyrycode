package memoryruntime

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

const artifactLimit = 512 << 20

func validateWheel(root *os.Root, name string) error {
	f, err := regularFile(root, name, os.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	z, err := zip.NewReader(f, info.Size())
	if err != nil {
		return err
	}
	if len(z.File) > 100000 {
		return ErrUnsafe
	}
	var total uint64
	for _, entry := range z.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if !fs.ValidPath(name) || (!entry.Mode().IsRegular() && !entry.Mode().IsDir()) {
			return ErrUnsafe
		}
		if entry.UncompressedSize64 > 2<<30-total {
			return ErrUnsafe
		}
		total += entry.UncompressedSize64
	}
	return nil
}

func download(ctx context.Context, root *os.Root, cache string, a artifact, client *http.Client) (string, error) {
	if !fs.ValidPath(a.File) || strings.Contains(a.File, "/") || len(a.SHA256) != 64 {
		return "", ErrIntegrity
	}
	cache += "/" + a.SHA256
	if err := privateDir(root, cache); err != nil {
		return "", err
	}
	destination := cache + "/" + a.File
	if f, err := regularFile(root, destination, os.O_RDONLY); err == nil {
		h := sha256.New()
		n, hashErr := io.Copy(h, io.LimitReader(f, artifactLimit+1))
		f.Close()
		if hashErr == nil && n <= artifactLimit && hex.EncodeToString(h.Sum(nil)) == a.SHA256 {
			return destination, nil
		}
		if err := root.Remove(destination); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("artifact download failed")
	}
	temp := cache + "/download-" + randomName()
	f, err := regularFile(root, temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return "", err
	}
	defer root.Remove(temp) // Best effort: failed or renamed temporary downloads are not reusable.
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(response.Body, artifactLimit+1))
	if err == nil && (n > artifactLimit || hex.EncodeToString(h.Sum(nil)) != a.SHA256) {
		err = ErrIntegrity
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := root.Rename(temp, destination); err != nil {
		return "", err
	}
	return destination, nil
}
func extract(ctx context.Context, home *os.Root, generation, source string) error {
	f, err := regularFile(home, source, os.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	root, err := home.OpenRoot(generation)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	var links []*tar.Header
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(h.Name, "/")
		if !fs.ValidPath(name) || seen[name] || len(seen) > 100000 {
			return ErrUnsafe
		}
		seen[name] = true
		total += h.Size
		if h.Size < 0 || total > 2<<30 {
			return ErrUnsafe
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := privateDir(root, name); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if parent := path.Dir(name); parent != "." {
				if err := privateDir(root, parent); err != nil {
					return err
				}
			}
			perm := os.FileMode(0600)
			if h.Mode&0111 != 0 {
				perm = 0700
			}
			out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			closeErr := out.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			target := path.Join(path.Dir(name), h.Linkname)
			if path.IsAbs(h.Linkname) || !fs.ValidPath(target) {
				return ErrUnsafe
			}
			links = append(links, h)
		default:
			return ErrUnsafe
		}
	}
	// No file writes follow link creation. Root prevents link chains from escaping.
	for _, h := range links {
		if parent := path.Dir(h.Name); parent != "." {
			if err := privateDir(root, parent); err != nil {
				return err
			}
		}
		if err := root.Symlink(h.Linkname, h.Name); err != nil {
			return err
		}
	}
	for _, h := range links {
		if _, err := root.Stat(h.Name); err != nil {
			return ErrUnsafe
		}
	}
	return nil
}
