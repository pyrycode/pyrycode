package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path/filepath"
)

// ErrBinaryNotInArchive is returned by ExtractBinary when the requested
// binaryName is not present as a regular file in the tar archive.
var ErrBinaryNotInArchive = errors.New("binary not found in archive")

// ErrMalformedArchive is returned by ExtractBinary when the input is not a
// valid gzip stream or the gzipped payload is not a valid tar archive.
var ErrMalformedArchive = errors.New("malformed tar.gz archive")

// ExtractBinary reads a gzipped tar archive entirely from memory and returns
// the bytes of the regular-file entry whose tar header Name matches
// binaryName (e.g. "pyry"). Memory-only: no temp files, no streaming-to-disk.
//
// Matching is by exact tar header Name compared with filepath.Base — the
// GoReleaser archive lays files at the archive root (`pyry`, `LICENSE`,
// `docs/INSTALL.md`, ...) so callers pass the bare filename. Non-regular
// entries (directories, symlinks, hard links) are skipped; only the first
// matching regular file is returned.
//
// Returns an error wrapping ErrMalformedArchive if tgzData is not a valid
// gzip stream or the decompressed payload is not a parseable tar archive.
// Returns an error wrapping ErrBinaryNotInArchive if no regular-file entry
// matches binaryName.
func ExtractBinary(tgzData []byte, binaryName string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tgzData))
	if err != nil {
		return nil, fmt.Errorf("opening gzip stream: %w", ErrMalformedArchive)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("looking up %q: %w", binaryName, ErrBinaryNotInArchive)
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar header: %w", ErrMalformedArchive)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(hdr.Name) != binaryName {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("reading %q from tar: %w", binaryName, ErrMalformedArchive)
		}
		return data, nil
	}
}
