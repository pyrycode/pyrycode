package memorycapture

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/pyrycode/pyrycode/internal/canonicalpath"
	"golang.org/x/sys/unix"
)

// Note is complete content with an unambiguous canonical root and relative slash path.
type Note struct{ Root, Path, Content string }

// Notes confines note I/O to explicit roots. Close waits for active operations.
// Symlinks below roots are never followed. Reference roots never grant writes.
type Notes struct {
	mu      sync.RWMutex
	roots   []noteRoot
	aliases map[string]int
	hook    func(string)
}
type noteRoot struct {
	name string
	file *os.File
}

// These fixed bounds cannot be widened by a caller: 32 roots/64 KiB request,
// 4 KiB paths and queries, 1 MiB notes, 10000 entries/32 MiB scanned bytes,
// 128 results/4 MiB returned content. Exceeding one returns no partial content.
const (
	maxNotePath    = 4096
	maxNoteBytes   = 1 << 20
	maxNoteEntries = 10000
	maxNoteScan    = 32 << 20
	maxNoteResults = 128
	maxNoteContent = 4 << 20
)

var ErrNoteLimit = errors.New("capture note limit exceeded")
var ErrNoteAccess = errors.New("capture note access rejected")

// NewNotes grants reads to vault and additional roots only, resolving root aliases.
// It grants writes only to vault. It neither creates roots nor discovers instructions.
func NewNotes(ctx context.Context, vault string, additional []string) (*Notes, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(additional) >= 32 {
		return nil, ErrNoteLimit
	}
	inputs := append([]string{vault}, additional...)
	total := 0
	for _, s := range inputs {
		total += len(s)
		if len(s) > maxNotePath || total > 64<<10 {
			return nil, ErrNoteLimit
		}
		if s == "" || !utf8.ValidString(s) {
			return nil, ErrNoteAccess
		}
	}
	n := &Notes{aliases: make(map[string]int)}
	for _, s := range inputs {
		if err := ctx.Err(); err != nil {
			n.Close()
			return nil, err
		}
		canonical, err := canonicalpath.Resolve(s)
		if err != nil || len(canonical) > maxNotePath {
			n.Close()
			return nil, ErrNoteAccess
		}
		i, ok := n.aliases[canonical]
		if !ok {
			f, e := noteDirectory(ctx, canonical)
			if e != nil {
				n.Close()
				return nil, e
			}
			i = len(n.roots)
			n.roots = append(n.roots, noteRoot{canonical, f})
		}
		n.aliases[s] = i
		n.aliases[canonical] = i
	}
	return n, nil
}
func (n *Notes) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	var err error
	for _, r := range n.roots {
		if r.file.Close() != nil {
			err = ErrNoteAccess
		}
	}
	n.roots = nil
	return err
}
func notePath(p string) error {
	if len(p) > maxNotePath {
		return ErrNoteLimit
	}
	if !validPath(p) || !utf8.ValidString(p) || !strings.EqualFold(path.Ext(p), ".md") {
		return ErrNoteAccess
	}
	return nil
}
func noteProtected(p string) bool {
	p = strings.ToLower(p)
	base := path.Base(p)
	return strings.HasPrefix(base, "agents") || strings.HasPrefix(base, "claude") || strings.Contains("/"+p, "/.claude/rules/")
}
func (n *Notes) checkpoint(stage string) {
	if n.hook != nil {
		n.hook(stage)
	}
}

func noteFold(s string) string {
	return strings.Map(func(r rune) rune {
		least := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < least {
				least = next
			}
		}
		return least
	}, s)
}

// Read accepts a registered root spelling and a root-relative Markdown path.
func (n *Notes) Read(ctx context.Context, root, p string) (Note, error) {
	if err := ctx.Err(); err != nil {
		return Note{}, err
	}
	if len(root) > maxNotePath {
		return Note{}, ErrNoteLimit
	}
	if err := notePath(p); err != nil {
		return Note{}, err
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	i, ok := n.aliases[root]
	if !ok || len(n.roots) == 0 {
		return Note{}, ErrNoteAccess
	}
	data, err := n.read(ctx, n.roots[i], p)
	if err != nil {
		return Note{}, err
	}
	return Note{n.roots[i].name, p, string(data)}, nil
}
func (n *Notes) read(ctx context.Context, r noteRoot, p string) ([]byte, error) {
	dir, err := noteParent(ctx, r, p)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	f, err := noteFile(dir, path.Base(p), unix.O_RDONLY, false)
	if err != nil {
		return nil, ErrNoteAccess
	}
	defer f.Close()
	n.checkpoint("access")
	if err := noteFence(ctx, r, p, dir, f); err != nil {
		return nil, err
	}
	data, err := n.readBytes(ctx, f)
	if err != nil {
		return nil, err
	}
	n.checkpoint("read-complete")
	if err = noteFence(ctx, r, p, dir, f); err != nil {
		return nil, err
	}
	return data, nil
}

// Search matches literal case-insensitive content and returns complete notes.
// Symlink entries are skipped; unsafe replacements and limits reject the entire search.
func (n *Notes) Search(ctx context.Context, query string) ([]Note, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(query) > maxNotePath {
		return nil, ErrNoteLimit
	}
	if query == "" || !utf8.ValidString(query) {
		return nil, ErrNoteAccess
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	if len(n.roots) == 0 {
		return nil, ErrNoteAccess
	}
	query = noteFold(query)
	seen := make(map[string]bool)
	var results []Note
	entries, scanned, returned := 0, 0, 0
	var walk func(noteRoot, string) error
	walk = func(r noteRoot, p string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir, err := noteParent(ctx, r, path.Join(p, "placeholder.md"))
		if err != nil {
			return err
		}
		defer dir.Close()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			n.checkpoint("scan")
			if err := ctx.Err(); err != nil {
				return err
			}
			batch, e := dir.ReadDir(64)
			if e != nil && e != io.EOF {
				return ErrNoteAccess
			}
			for _, entry := range batch {
				entries++
				if entries > maxNoteEntries {
					return ErrNoteLimit
				}
				name := path.Join(p, entry.Name())
				if len(name) > maxNotePath {
					return ErrNoteLimit
				}
				if entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				if entry.IsDir() {
					if err := walk(r, name); err != nil {
						return err
					}
					continue
				}
				if !entry.Type().IsRegular() || !strings.EqualFold(path.Ext(name), ".md") {
					continue
				}
				canonical := filepath.Join(r.name, filepath.FromSlash(name))
				if seen[canonical] {
					continue
				}
				seen[canonical] = true
				data, err := n.read(ctx, r, name)
				if err != nil {
					return err
				}
				scanned += len(data)
				if scanned > maxNoteScan {
					return ErrNoteLimit
				}
				if strings.Contains(noteFold(string(data)), query) {
					returned += len(data)
					if len(results) >= maxNoteResults || returned > maxNoteContent {
						return ErrNoteLimit
					}
					results = append(results, Note{r.name, name, string(data)})
				}
			}
			if err := noteFence(ctx, r, path.Join(p, "placeholder.md"), dir, nil); err != nil {
				return err
			}
			if e == io.EOF {
				return nil
			}
		}
	}
	for _, r := range n.roots {
		if err := walk(r, "."); err != nil {
			return nil, err
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Root == results[j].Root {
			return results[i].Path < results[j].Path
		}
		return results[i].Root < results[j].Root
	})
	return results, nil
}
