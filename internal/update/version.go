// Package update implements pyrycode's self-update logic: release manifest
// parsing, version comparison, fetch, and replace. This file lands the
// pure-function half (parsing + comparison); the HTTP fetcher and the binary
// replacer arrive in sister tickets.
package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Comparison is the result of comparing two semver versions. The values mirror
// cmp.Compare / strings.Compare conventions: negative when the first argument
// is less, zero when equal, positive when greater.
type Comparison int

const (
	// Older means current is older than latest.
	Older Comparison = -1
	// Same means current and latest are equal.
	Same Comparison = 0
	// Newer means current is newer than latest.
	Newer Comparison = 1
)

// ErrMalformedRelease is returned by ParseLatestRelease when the JSON is
// invalid, not an object, or missing/empty "tag_name".
var ErrMalformedRelease = errors.New("malformed release manifest")

// ErrInvalidVersion is returned by CompareVersions when either argument is
// not a parseable semver string.
var ErrInvalidVersion = errors.New("invalid semver version")

// ParseLatestRelease extracts the tag name from a GitHub Releases API JSON
// payload (e.g. the response body of GET /repos/{owner}/{repo}/releases/latest).
//
// On success it returns the value of the top-level "tag_name" field (e.g.
// "v0.9.1" — the leading "v" is preserved verbatim; CompareVersions strips it).
//
// Returns an error wrapping ErrMalformedRelease if jsonBytes is not valid
// JSON, if the document is not a JSON object, if "tag_name" is absent, or if
// "tag_name" is not a string. The empty string is also rejected as an absent
// tag.
func ParseLatestRelease(jsonBytes []byte) (string, error) {
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		return "", fmt.Errorf("decoding release JSON: %w", ErrMalformedRelease)
	}
	if payload.TagName == "" {
		return "", fmt.Errorf("missing tag_name field: %w", ErrMalformedRelease)
	}
	return payload.TagName, nil
}

// CompareVersions compares two semver version strings and reports whether
// current is older than, equal to, or newer than latest.
//
// Both arguments may carry a leading "v" (e.g. "v0.9.1") or omit it
// ("0.9.1") — the prefix is stripped before parsing. Pre-release and
// build-metadata suffixes are stripped at the first '-' or '+' before
// numeric parsing (e.g. "v0.10.0-rc1" is compared as "0.10.0"). This is a
// deliberate simplification: pyry's tags are plain major.minor.patch and
// the comparator does not need to implement SemVer 2.0.0 precedence rules
// for pre-releases.
//
// Returns an error wrapping ErrInvalidVersion if either argument cannot be
// parsed into three non-negative integers separated by dots after the prefix
// and suffix stripping above. The sentinel main.Version value "dev" is one
// such case — callers detecting a "dev" build should special-case it before
// invoking CompareVersions. On error the returned Comparison is Same; callers
// must check the error first.
func CompareVersions(current, latest string) (Comparison, error) {
	cMaj, cMin, cPat, err := parseSemver(current)
	if err != nil {
		return Same, err
	}
	lMaj, lMin, lPat, err := parseSemver(latest)
	if err != nil {
		return Same, err
	}
	switch {
	case cMaj != lMaj:
		return cmpInt(cMaj, lMaj), nil
	case cMin != lMin:
		return cmpInt(cMin, lMin), nil
	default:
		return cmpInt(cPat, lPat), nil
	}
}

func parseSemver(s string) (maj, min, pat int, err error) {
	original := s
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("parsing %q: %w", original, ErrInvalidVersion)
	}
	out := [3]int{}
	for i, p := range parts {
		n, perr := strconv.Atoi(p)
		if perr != nil || n < 0 {
			return 0, 0, 0, fmt.Errorf("parsing %q: %w", original, ErrInvalidVersion)
		}
		out[i] = n
	}
	return out[0], out[1], out[2], nil
}

func cmpInt(a, b int) Comparison {
	switch {
	case a < b:
		return Older
	case a > b:
		return Newer
	default:
		return Same
	}
}

// ErrUpToDate is returned by Eligible when the release is the running version.
var ErrUpToDate = errors.New("already at the latest release")

// ErrNotEligible is returned by Eligible when the release must not be installed
// automatically. The wrapping error names the reason.
var ErrNotEligible = errors.New("release not eligible")

// Release is the subset of a GitHub Releases API payload that decides whether a
// release may be installed without an operator.
type Release struct {
	Tag        string
	Draft      bool
	Prerelease bool
}

// ParseRelease decodes tag_name, draft and prerelease from a GitHub Releases API
// JSON payload. It has the same ErrMalformedRelease contract as
// ParseLatestRelease: invalid JSON, a non-object document, or an absent or empty
// tag_name are all malformed.
func ParseRelease(jsonBytes []byte) (Release, error) {
	var payload struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(jsonBytes, &payload); err != nil {
		return Release{}, fmt.Errorf("decoding release JSON: %w", ErrMalformedRelease)
	}
	if payload.TagName == "" {
		return Release{}, fmt.Errorf("missing tag_name field: %w", ErrMalformedRelease)
	}
	return Release{Tag: payload.TagName, Draft: payload.Draft, Prerelease: payload.Prerelease}, nil
}

// Eligible reports whether rel may be installed over a running build of version
// current without an operator. It returns nil when it may, an error wrapping
// ErrUpToDate when rel is the running version, and an error wrapping
// ErrNotEligible otherwise: a running build that is not a release (such as
// "dev"), a draft or pre-release, a tag that is not exactly
// v<major>.<minor>.<patch>, or a release older than the running one.
//
// The tag check is stricter than CompareVersions on purpose. CompareVersions
// strips a "-rc1" suffix before comparing, and strconv.Atoi accepts a sign, so
// either would let a tag through that no hand-made release carries. The tag is
// also joined into download URLs, so only digits and dots pass.
func Eligible(current string, rel Release) error {
	if _, _, _, err := parseSemver(current); err != nil {
		return fmt.Errorf("running build %q is not a release: %w", current, ErrNotEligible)
	}
	switch {
	case rel.Draft:
		return fmt.Errorf("draft release: %w", ErrNotEligible)
	case rel.Prerelease:
		return fmt.Errorf("pre-release: %w", ErrNotEligible)
	case !isPlainReleaseTag(rel.Tag):
		return fmt.Errorf("tag is not a plain release version: %w", ErrNotEligible)
	}
	cmp, err := CompareVersions(current, rel.Tag)
	if err != nil {
		return fmt.Errorf("comparing versions: %w", ErrNotEligible)
	}
	switch cmp {
	case Same:
		return ErrUpToDate
	case Newer:
		return fmt.Errorf("downgrade: %w", ErrNotEligible)
	}
	return nil
}

// isPlainReleaseTag reports whether tag is v?<digits>.<digits>.<digits> with
// nothing else.
func isPlainReleaseTag(tag string) bool {
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}
