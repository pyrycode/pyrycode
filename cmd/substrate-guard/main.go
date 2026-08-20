// Command substrate-guard fails the build if any claude-TUI substrate literal
// appears in pyrycode .go source outside an explicit allowlist.
//
// The substrate — claude's on-screen literals, escape sequences, and glyphs —
// is owned by github.com/pyrycode/tui-driver. A consumer that hardcodes those
// literals drifts on every claude self-update and silently re-couples to the
// internals tui-driver exists to hide. The compiler seal (unexported buffer /
// pty, no public Write, no Mirror tap) stops a consumer reaching the substrate
// through the Session API; this guard is the second, different-fabric check for
// the hole the compiler cannot see: a hardcoded screen string or escape
// sequence in a string literal or comment.
//
// Run via `make substrate-guard`, wired into `make check`. Scans every .go
// file under the repo root, skipping the allowlist; exits non-zero and prints
// file:line for every hit. Directories named .git, .claude, vendor,
// node_modules and dist are not descended into: .claude/ holds agent working
// copies of other branches, which are not this tree's source.
//
// Allowlist: the sanctioned fake-claude helper internal/e2e/internal/
// fakeclaude/main.go, which legitimately emits these literals to simulate
// claude's TUI for the relay/send_message e2e flows (#603), and this guard's
// own source, which must name the patterns it bans.
package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// allowlist holds path suffixes exempt from the scan.
var allowlist = []string{
	"internal/e2e/internal/fakeclaude/main.go",
	"cmd/substrate-guard/main.go",
}

// pattern is one banned substrate token. substr is matched against raw file
// bytes, so BOTH a compiled literal (an embedded glyph or ESC byte) and an
// escaped source form (the text \xe2\x9d\xaf or \x1b[) are caught. The glyphs
// are spelled as explicit hex bytes / ASCII escape text here so this guard's
// own source carries no raw glyph — only the named patterns it bans.
type pattern struct {
	name   string
	substr []byte
}

// patterns lists the banned substrate tokens. Specific forms precede the
// general CSI catch-all so the per-line first-match reports the most precise
// name. Each glyph is checked three ways: as raw UTF-8 bytes (an embedded
// rune), as a \x.. source escape, and as a \u.. source escape. U+273B is the
// thinking spinner; U+276F is the input prompt marker.
var patterns = []pattern{
	{"pasted-text chip", []byte("Pasted text")},
	{"esc-to-interrupt hint", []byte("esc to interrupt")},
	{"trust modal (spaced)", []byte("Quick safety check")},
	{"trust modal (stripped)", []byte("Quicksafetycheck")},
	{"network-failure text", []byte("failed to connect")},
	{"network-failure anchor", []byte("FailedToOpenSocket")},
	{"spinner glyph U+273B (rune)", []byte{0xe2, 0x9c, 0xbb}},
	{"idle glyph U+276F (rune)", []byte{0xe2, 0x9d, 0xaf}},
	{"spinner glyph (\\x form)", []byte(`\xe2\x9c\xbb`)},
	{"idle glyph (\\x form)", []byte(`\xe2\x9d\xaf`)},
	{"spinner glyph (\\u form)", []byte("\\u273b")},
	{"idle glyph (\\u form)", []byte("\\u276f")},
	{"bracketed-paste open", []byte(`\x1b[200~`)},
	{"bracketed-paste close", []byte(`\x1b[201~`)},
	{"CSI escape (\\x form)", []byte(`\x1b[`)},
	{"CSI escape (raw byte)", []byte{0x1b, '['}},
}

func isAllowlisted(rel string) bool {
	for _, a := range allowlist {
		if strings.HasSuffix(rel, a) {
			return true
		}
	}
	return false
}

// hit is one banned substrate literal, at one line of one scanned file.
type hit struct {
	file string  // slash-separated, relative to the scan root
	line int     // 1-based
	pat  pattern // the first pattern matching that line
}

// scan walks root and reports every banned substrate literal in .go files
// outside the allowlist, at most one hit per line, in walk order. Directories
// named .git, vendor, node_modules, dist and .claude are not descended into —
// .claude/ holds agent working copies of other branches, whose source is not
// this tree's. Per-entry walk errors and unreadable files are skipped. The
// returned error is non-nil only when the walk itself fails.
func scan(root string) ([]hit, error) {
	var hits []hit
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "dist", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			// Unreachable for a path WalkDir produced from root; report the
			// walked path rather than dropping the file, so no hit is lost.
			rel = path
		}
		rel = filepath.ToSlash(rel)
		if isAllowlisted(rel) {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for i, line := range bytes.Split(data, []byte("\n")) {
			for _, p := range patterns {
				if bytes.Contains(line, p.substr) {
					hits = append(hits, hit{file: rel, line: i + 1, pat: p})
					break // one finding per line is enough
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return hits, nil
}

func main() {
	hits, err := scan(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "substrate-guard: walk error:", err)
		os.Exit(2)
	}
	if len(hits) > 0 {
		fmt.Fprintf(os.Stderr, "substrate-guard: %d claude-TUI substrate literal(s) found outside the allowlist.\n", len(hits))
		fmt.Fprintln(os.Stderr, "These belong in github.com/pyrycode/tui-driver, not pyrycode.")
		fmt.Fprintln(os.Stderr, "Allowlist:", strings.Join(allowlist, ", "))
		for _, h := range hits {
			fmt.Fprintf(os.Stderr, "  %s:%d: substrate literal %s (%s)\n",
				h.file, h.line, strconv.Quote(string(h.pat.substr)), h.pat.name)
		}
		os.Exit(1)
	}
	fmt.Println("substrate-guard: clean")
}
