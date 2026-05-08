# Spec: `internal/update` extract pyry binary from .tar.gz (#186)

## Files to read first

- `internal/update/checksum.go:1-54` — sibling pure-function shape (`AssetName`): package-level placement, sentinel-error idiom (`errors.New(...)` plus `fmt.Errorf("…: %w", sentinel)` wrapping), exported-symbol doc-comment voice. Mirror tone exactly.
- `internal/update/version.go:1-13` — package doc-comment lives at the top of `version.go`. Don't duplicate the `// Package update ...` block in `install.go`; one package comment per package.
- `internal/update/checksum_test.go:1-220` — table-driven test pattern with `t.Parallel()`, `tests := []struct{...}` shape, `wantErr error` + `errors.Is(err, tc.wantErr)` matching, error-message-shape sub-tests. Mirror this for `install_test.go`.
- `CODING-STYLE.md` — `gofmt` non-negotiable, doc-comment-on-every-exported-symbol, stdlib-only.
- Issue #186 body — acceptance criteria are the contract (three test cases enumerated verbatim).
- `docs/lessons.md` — "Atomic on-disk writes" section is *not* in scope here (atomic replace was carved off into a future wiring ticket; see "Out of scope" below). Skim only to confirm this slice does not touch `os.Rename` / `os.CreateTemp`.

No prior knowledge doc on tar/gzip extraction exists; this is the fourth pure-function slice in `internal/update` (after #179 version compare, parent #183's checksum split into #180, and #181's restart-detect).

## Context

`pyry update` downloads a GoReleaser tarball (`pyry_<version>_<Os>_<Arch>.tar.gz`) and needs the `pyry` binary's bytes out of it before the wiring ticket can perform the on-disk replacement. The tarball also contains `LICENSE`, `README.md`, `docs/*.md`, and the systemd / launchd unit templates — all documentation that ships for tarball-direct downloaders. The runtime update flow only cares about the binary.

This ticket is the tar/gzip extraction half. It is split from the original #183 (which bundled extraction + atomic replace) so the tar-parsing concern can be tested with inline `bytes.Buffer` fixtures and is structurally decoupled from the filesystem-rename logic. Atomic replace will arrive as a separate ticket whose tests use `t.TempDir()`.

## Design

### Package placement

Lives in `internal/update/install.go` alongside `version.go`, `checksum.go`, and `restart.go`. No new sub-package — single small pure function, same shape as its siblings.

`install.go` is the file name from the AC; the eventual atomic-replace function will likely also land in `install.go` from the wiring ticket, but this ticket adds *only* `ExtractBinary` and its sentinel errors.

### Exported surface

```go
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
// Matching is by exact tar header Name, compared with filepath.Base — the
// GoReleaser archive lays files at the archive root (`pyry`, `LICENSE`,
// `docs/INSTALL.md`, ...) so callers pass the bare filename. Non-regular
// entries (directories, symlinks, hard links) are skipped; only the first
// matching regular file is returned.
//
// Returns an error wrapping ErrMalformedArchive if tgzData is not a valid
// gzip stream or the decompressed payload is not a parseable tar archive.
// Returns an error wrapping ErrBinaryNotInArchive if no regular-file entry
// matches binaryName.
func ExtractBinary(tgzData []byte, binaryName string) ([]byte, error)
```

### Body

```go
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
```

That's the whole file (plus imports + sentinel-error declarations + doc comments). No helpers, no goroutines, no streaming abstractions.

### Why `filepath.Base(hdr.Name)` matching

GoReleaser's archive lays `pyry`, `LICENSE`, `README.md` at the archive root — `hdr.Name` for the binary is exactly `"pyry"`. But the docs and unit templates may carry a directory prefix (`docs/INSTALL.md`, `systemd/pyry.service`). `filepath.Base` makes the matcher robust against a future GoReleaser config change that wraps everything in a top-level `pyry_v0.9.1/` directory (a common archives layout) without requiring this function to change. The cost is that a hypothetical archive with a `subdir/pyry` entry would also match — accept it; GoReleaser does not produce such archives, and a stricter "exact `hdr.Name`" check would force this slice to be revisited the moment GoReleaser's layout changes.

### Why skip non-regular entries

`tar.TypeReg` is the only file type GoReleaser emits for archive contents. Skipping `TypeDir`, `TypeSymlink`, `TypeLink`, etc. defends against a malicious/quirky archive that names its top-level directory `pyry/` (which would otherwise be returned as a zero-length "binary"). The check is cheap, the failure mode is silent and weird without it.

### Why no streaming / size cap

The release tarball is ~10–20 MB. `bytes.NewReader(tgzData)` plus `io.ReadAll(tr)` for the matched entry keeps the whole binary in memory once decompressed. The wiring ticket already holds the full `tgzData` in memory (it came from an HTTP GET with the body buffered for checksum verification), so there's no streaming win to be had at this layer. If pyry ever ships a 1 GB binary, revisit; for now, simplicity wins.

A defensive size cap (e.g. `io.LimitReader(tr, 256<<20)`) is **not** added — this is an evidence-based-fix call. We have not observed a malicious archive in the wild, and the operator's update flow is already bounded by the HTTP fetch's content-length and `tgzData`'s in-memory size. The wiring ticket will checksum-verify before extraction, which forecloses the "attacker-controlled tarball" path entirely.

### Why no on-disk fallback

The AC says "memory-only: no temp files." The function is a pure (input-bytes → output-bytes) transform. Callers who need the binary on disk use the future atomic-replace function on the returned bytes; this slice does not couple to the filesystem.

## Concurrency model

None. Pure function. Safe to call concurrently with itself or any other function in the package — operates only on its arguments and returns a fresh slice.

## Error handling

Two sentinels, both wrapped via `fmt.Errorf(..., %w, sentinel)` so callers use `errors.Is`:

- `ErrMalformedArchive` — gzip-open failure or tar-read failure (any `tar.Next` / `io.ReadAll` error). The wrapper message names the failing step ("opening gzip stream", "reading tar header", "reading %q from tar") for diagnostic logging, but `errors.Is(err, ErrMalformedArchive)` is the canonical match.
- `ErrBinaryNotInArchive` — iterated to `io.EOF` without finding a regular-file entry matching `binaryName`. The wrapper message names the lookup target.

No error path returns the sentinel bare; always wrap. This matches `checksum.go`'s convention.

## Testing strategy

`internal/update/install_test.go`, table-driven with `t.Parallel()`, mirroring `checksum_test.go` shape.

A single helper `buildTarGz(t *testing.T, files map[string][]byte) []byte` constructs an inline tarball: `bytes.Buffer` → `gzip.Writer` → `tar.Writer` → write each name+bytes pair as a `tar.Header{Name, Size, Mode: 0o755, Typeflag: tar.TypeReg}` then `tw.Write(data)`. Close in reverse order. Map iteration order doesn't matter because the test reads from the resulting tarball, not the map.

Three core cases from the AC, plus two minor extensions for sentinel coverage:

```go
func TestExtractBinary(t *testing.T) {
    t.Parallel()

    pyryBytes := []byte("\x7fELF...fake binary contents...")
    fixture := buildTarGz(t, map[string][]byte{
        "pyry":            pyryBytes,
        "LICENSE":         []byte("MIT"),
        "docs/INSTALL.md": []byte("# Install"),
    })

    tests := []struct {
        name       string
        data       []byte
        binaryName string
        want       []byte
        wantErr    error
    }{
        {
            name:       "binary_present_returned_unchanged",
            data:       fixture,
            binaryName: "pyry",
            want:       pyryBytes,
        },
        {
            name:       "binary_missing",
            data:       fixture,
            binaryName: "claude", // present in no GoReleaser archive
            wantErr:    ErrBinaryNotInArchive,
        },
        {
            name:       "garbage_not_gzip",
            data:       []byte("this is plainly not a gzip stream"),
            binaryName: "pyry",
            wantErr:    ErrMalformedArchive,
        },
        {
            name:       "valid_gzip_garbage_tar",
            data:       gzipOf(t, []byte("not a tar archive at all")),
            binaryName: "pyry",
            wantErr:    ErrMalformedArchive,
        },
        {
            name:       "empty_input",
            data:       []byte{},
            binaryName: "pyry",
            wantErr:    ErrMalformedArchive,
        },
    }

    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            t.Parallel()
            got, err := ExtractBinary(tc.data, tc.binaryName)
            if tc.wantErr != nil {
                if !errors.Is(err, tc.wantErr) {
                    t.Fatalf("err = %v, want errors.Is(_, %v)", err, tc.wantErr)
                }
                return
            }
            if err != nil {
                t.Fatalf("unexpected err: %v", err)
            }
            if !bytes.Equal(got, tc.want) {
                t.Errorf("ExtractBinary returned %d bytes, want %d", len(got), len(tc.want))
            }
        })
    }
}
```

`gzipOf(t, []byte) []byte` is a one-liner test helper that wraps `bytes.Buffer` + `gzip.NewWriter` for the "valid gzip / invalid tar" case — it forces the code path past `gzip.NewReader` so `tar.Next`'s error path is exercised, distinguishing it from the "invalid gzip" case which fails earlier.

The `binary_present_returned_unchanged` case asserts byte-equality (`bytes.Equal`) — the function's contract is exact-byte fidelity, not "looks like an ELF". The fake `\x7fELF` prefix is just a recognisable byte pattern for diagnostic dumps.

`docs/INSTALL.md` in the fixture exercises the directory-prefixed entry path so `filepath.Base` matching is demonstrated (a future regression that switches to bare-`hdr.Name` equality would still pass these tests, but `directory_prefixed_entry_skipped` could be added if defending the helper-function decision is worth a sub-test — defer for now; the AC enumerates three cases and over-specifying invites churn).

No mocking, no filesystem, no goroutines. The whole test file is ~80 lines including the two helpers.

## Open questions

None. The acceptance criteria are exhaustive; the matching rule (`filepath.Base` exact) is specified; sentinel-error vocabulary mirrors `checksum.go`. Atomic replace is explicitly a separate ticket and is not addressed here.
