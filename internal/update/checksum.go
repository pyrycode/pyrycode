package update

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupportedPlatform is returned by AssetName for any os/arch combo
// not built by .goreleaser.yaml.
var ErrUnsupportedPlatform = errors.New("unsupported os/arch")

// ErrAssetNotInChecksums is returned by ParseChecksumsFile when the
// requested asset is not listed in the checksums.txt body.
var ErrAssetNotInChecksums = errors.New("asset not listed in checksums")

// ErrMalformedChecksums is returned by ParseChecksumsFile when the input
// is empty or contains no parseable "<hex>  <name>" lines at all.
var ErrMalformedChecksums = errors.New("malformed checksums file")

// ErrChecksumMismatch is returned by VerifySHA256 when the computed digest
// does not match the expected one.
var ErrChecksumMismatch = errors.New("sha256 checksum mismatch")

// osTitles and archNames mirror .goreleaser.yaml's archives.name_template
// verbatim. If the template ever changes, these maps change.
var (
	osTitles  = map[string]string{"linux": "Linux", "darwin": "Darwin"}
	archNames = map[string]string{"amd64": "x86_64", "arm64": "arm64"}
)

// AssetName returns the GoReleaser-produced tarball filename for the given
// version and host platform, e.g. AssetName("v0.9.1", "darwin", "arm64") →
// "pyry_0.9.1_Darwin_arm64.tar.gz".
//
// A leading "v" on version is stripped (GoReleaser's name template uses the
// bare semver). The os/arch values follow Go's runtime.GOOS / runtime.GOARCH
// vocabulary: only the four combinations actually built by .goreleaser.yaml
// are supported (linux/darwin × amd64/arm64); any other combination returns
// an error wrapping ErrUnsupportedPlatform.
func AssetName(version, goos, goarch string) (string, error) {
	osTitle, ok := osTitles[goos]
	if !ok {
		return "", fmt.Errorf("asset name for %s/%s: %w", goos, goarch, ErrUnsupportedPlatform)
	}
	archName, ok := archNames[goarch]
	if !ok {
		return "", fmt.Errorf("asset name for %s/%s: %w", goos, goarch, ErrUnsupportedPlatform)
	}
	v := strings.TrimPrefix(version, "v")
	return fmt.Sprintf("pyry_%s_%s_%s.tar.gz", v, osTitle, archName), nil
}

// ParseChecksumsFile finds the SHA-256 hex digest for assetName inside the
// contents of a GoReleaser-produced checksums.txt. Each non-empty line of
// the file is expected to be "<sha256-hex>  <filename>" (two spaces between
// the digest and the filename, matching `sha256sum` output). Lines that
// don't match are skipped silently — GoReleaser may add trailing blank
// lines or future header comments.
//
// On success returns the lowercase hex digest (64 characters). Returns an
// error wrapping ErrAssetNotInChecksums if no matching line is found, or
// ErrMalformedChecksums if the file is empty or contains no parseable
// lines at all.
func ParseChecksumsFile(text, assetName string) (string, error) {
	sawAny := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		sawAny = true
		if parts[1] == assetName {
			return strings.ToLower(parts[0]), nil
		}
	}
	if !sawAny {
		return "", fmt.Errorf("checksums file empty or unparseable: %w", ErrMalformedChecksums)
	}
	return "", fmt.Errorf("looking up %q: %w", assetName, ErrAssetNotInChecksums)
}

// VerifySHA256 returns nil iff sha256(data) lowercase-hex equals expectedHex.
// On mismatch the error includes both the expected and actual digests for
// diagnostic logging. expectedHex is matched case-insensitively (callers
// pass a value sourced from ParseChecksumsFile, which already lowercases,
// but this guards against future callers that don't).
func VerifySHA256(data []byte, expectedHex string) error {
	sum := sha256.Sum256(data)
	actualHex := hex.EncodeToString(sum[:])
	if !strings.EqualFold(actualHex, expectedHex) {
		return fmt.Errorf("expected %s, got %s: %w",
			strings.ToLower(expectedHex), actualHex, ErrChecksumMismatch)
	}
	return nil
}
