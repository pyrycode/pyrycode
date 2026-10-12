package memorycapture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testNotes(t *testing.T) (*Notes, string, string) {
	t.Helper()
	base := t.TempDir()
	vault, ref := filepath.Join(base, "vault"), filepath.Join(base, "ref")
	for _, p := range []string{vault, ref, filepath.Join(vault, "nested")} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(vault, alias); err != nil {
		t.Fatal(err)
	}
	n, err := NewNotes(context.Background(), vault, []string{ref, alias, filepath.Dir(vault), filepath.Join(vault, "nested")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := n.Close(); err != nil {
			t.Error(err)
		}
	})
	return n, vault, ref
}
func testNoteFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
func testNoteBytes(t *testing.T, p, want string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil || string(b) != want {
		t.Fatalf("preservation: %v", err)
	}
}
func TestNotesContract(t *testing.T) {
	n, vault, ref := testNotes(t)
	ctx := context.Background()
	for i, content := range []string{"first", "UPDATED", "UPDATED"} {
		before, _ := os.Stat(filepath.Join(vault, "nested/a.md"))
		changed, err := n.Write(ctx, "nested/a.md", []byte(content))
		if err != nil {
			t.Fatal(err)
		}
		if content == "UPDATED" && before != nil {
			b, _ := os.ReadFile(filepath.Join(vault, "nested/a.md"))
			if string(b) != content {
				t.Fatal("write")
			}
		}
		want := "nested/a.md"
		if i == 2 {
			want = ""
		}
		if changed != want {
			t.Fatal(changed)
		}
	}
	info, _ := os.Stat(filepath.Join(vault, "nested/a.md"))
	if p, err := n.Write(ctx, "nested/a.md", []byte("UPDATED")); err != nil || p != "" {
		t.Fatalf("no change: %q %v", p, err)
	}
	after, _ := os.Stat(filepath.Join(vault, "nested/a.md"))
	if !os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) {
		t.Fatal("identical write changed file")
	}
	testNoteFile(t, filepath.Join(ref, "b.MD"), "updated literal [x]")
	testNoteFile(t, filepath.Join(vault, "AGENTS.md"), "UPDATED instruction")
	alias := filepath.Join(filepath.Dir(vault), "alias")
	got, err := n.Read(ctx, alias, "nested/a.md")
	if err != nil || got != (Note{Root: vault, Path: "nested/a.md", Content: "UPDATED"}) {
		t.Fatalf("read: %#v %v", got, err)
	}
	got, err = n.Read(ctx, ref, "b.MD")
	if err != nil || got.Content != "updated literal [x]" {
		t.Fatal(err)
	}
	results, err := n.Search(ctx, "UpDaTeD")
	if err != nil || len(results) != 3 {
		t.Fatalf("search count %d: %v", len(results), err)
	}
	for i := 1; i < len(results); i++ {
		if results[i-1].Root+"/"+results[i-1].Path >= results[i].Root+"/"+results[i].Path {
			t.Fatal("unsorted results")
		}
	}
	again, err := n.Search(ctx, "UpDaTeD")
	if err != nil || !reflect.DeepEqual(results, again) {
		t.Fatal("order")
	}
	literal, err := n.Search(ctx, "[x]")
	if err != nil || len(literal) != 1 || literal[0].Root != ref {
		t.Fatal("literal")
	}
	testNoteFile(t, filepath.Join(vault, "greek.md"), "Σ and ς")
	if found, err := n.Search(ctx, "σ"); err != nil || len(found) != 1 {
		t.Fatal("Unicode case fold", err)
	}
	if p, err := n.Write(ctx, filepath.Join(ref, "b.MD"), []byte("bad")); err == nil || p != "" {
		t.Fatal("reference write")
	}
	testNoteBytes(t, filepath.Join(ref, "b.MD"), "updated literal [x]")
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Read(ctx, vault, "nested/a.md"); err == nil {
		t.Fatal("closed read")
	}
}
func TestNotesRejects(t *testing.T) {
	n, vault, ref := testNotes(t)
	ctx := context.Background()
	outside := filepath.Join(ref, "outside.md")
	testNoteFile(t, outside, "secret sentinel")
	for _, p := range []string{"AGENTS.md", "nested/aGeNtS-extra.md", "nested/CLAUDE.local.MD", "nested/.claude/rules/deeper/rule.md"} {
		testNoteFile(t, filepath.Join(vault, p), "instructions")
		if changed, err := n.Write(ctx, p, []byte("bad")); err == nil || changed != "" {
			t.Fatalf("protected %s", p)
		}
		testNoteBytes(t, filepath.Join(vault, p), "instructions")
		if _, err := n.Read(ctx, vault, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(ref, filepath.Join(vault, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(vault, "AGENTS.md"), filepath.Join(vault, "alias.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(vault, "hard.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(vault, "AGENTS.md"), filepath.Join(vault, "hard-instruction.md")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../ref/outside.md", outside, "escape/outside.md", "alias.md", "hard.md", "hard-instruction.md", "x.txt", "./a.md", "a\\b.md"} {
		if changed, err := n.Write(ctx, p, []byte("secret query")); err == nil || changed != "" || strings.Contains(err.Error(), "secret") {
			t.Fatalf("reject %s: %q %v", p, changed, err)
		}
	}
	for _, p := range []string{"../ref/outside.md", outside, "escape/outside.md", "alias.md"} {
		if got, err := n.Read(ctx, vault, p); err == nil || got != (Note{}) {
			t.Fatal("unsafe read")
		}
	}
	if _, err := n.Read(ctx, ref+"-not-granted", "outside.md"); err == nil {
		t.Fatal("root")
	}
	testNoteBytes(t, outside, "secret sentinel")
	testNoteBytes(t, filepath.Join(vault, "AGENTS.md"), "instructions")
	// Searching a vault alone never follows its escape directory into references.
	isolated, err := NewNotes(ctx, vault, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	if found, err := isolated.Search(ctx, "secret sentinel"); err != nil || len(found) != 1 || found[0].Path != "hard.md" {
		t.Fatalf("escape search: %#v %v", found, err)
	}
}
func TestNotesSubstitution(t *testing.T) {
	for _, op := range []string{"read", "search", "write", "create"} {
		for _, attack := range []string{"root", "parent", "leaf", "regular-root", "regular-parent", "regular-leaf"} {
			t.Run(op+"/"+attack, func(t *testing.T) {
				n, vault, ref := testNotes(t)
				target := filepath.Join(vault, "nested/a.md")
				testNoteFile(t, target, "original")
				if op == "create" {
					target = filepath.Join(vault, "nested/new.md")
				}
				outside := filepath.Join(ref, "a.md")
				testNoteFile(t, outside, "outside")
				fired := false
				preserved := target
				n.hook = func(stage string) {
					if fired || stage != "access" {
						return
					}
					fired = true
					replacement := ""
					switch strings.TrimPrefix(attack, "regular-") {
					case "root":
						if err := os.Rename(vault, vault+"-held"); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(ref, vault); err != nil {
							t.Fatal(err)
						}
						preserved = filepath.Join(vault+"-held", "nested/a.md")
						replacement = vault
					case "parent":
						if err := os.Rename(filepath.Dir(target), filepath.Dir(target)+"-held"); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(ref, filepath.Dir(target)); err != nil {
							t.Fatal(err)
						}
						preserved = filepath.Join(filepath.Dir(target)+"-held", "a.md")
						replacement = filepath.Dir(target)
					case "leaf":
						if err := os.Rename(target, target+"-held"); op != "create" && err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(outside, target); err != nil {
							t.Fatal(err)
						}
						preserved = target + "-held"
						replacement = target
					}
					if strings.HasPrefix(attack, "regular-") {
						if err := os.Remove(replacement); err != nil {
							t.Fatal(err)
						}
						if strings.HasSuffix(attack, "leaf") {
							testNoteFile(t, replacement, "original")
						} else {
							if err := os.Mkdir(replacement, 0755); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				var err error
				switch op {
				case "read":
					_, err = n.Read(context.Background(), vault, "nested/a.md")
				case "search":
					_, err = n.Search(context.Background(), "original")
				default:
					var changed string
					p := "nested/a.md"
					if op == "create" {
						p = "nested/new.md"
					}
					changed, err = n.Write(context.Background(), p, []byte("bad"))
					if changed != "" {
						t.Fatal("false success")
					}
				}
				if err == nil || !fired {
					t.Fatalf("substitution: %v %v", fired, err)
				}
				testNoteBytes(t, outside, "outside")
				if op != "create" {
					testNoteBytes(t, preserved, "original")
				}
			})
		}
	}
}
func TestNotesCancellation(t *testing.T) {
	n, vault, _ := testNotes(t)
	testNoteFile(t, filepath.Join(vault, "a.md"), "old")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n.hook = func(string) { t.Fatal("pre-cancelled work") }
	if _, err := NewNotes(ctx, vault, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := n.Read(ctx, vault, "a.md"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := n.Search(ctx, "old"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if p, err := n.Write(ctx, "a.md", []byte("new")); !errors.Is(err, context.Canceled) || p != "" {
		t.Fatal(err)
	}
	for _, op := range []string{"read", "search", "write"} {
		ctx, cancel = context.WithCancel(context.Background())
		n.hook = func(stage string) {
			if stage == "access" {
				cancel()
			}
		}
		var err error
		switch op {
		case "read":
			_, err = n.Read(ctx, vault, "a.md")
		case "search":
			_, err = n.Search(ctx, "old")
		case "write":
			_, err = n.Write(ctx, "a.md", []byte("new"))
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatal(op, err)
		}
	}
	testNoteBytes(t, filepath.Join(vault, "a.md"), "old")
}
func TestNotesLimits(t *testing.T) {
	n, vault, _ := testNotes(t)
	ctx := context.Background()
	for _, size := range []int{maxNoteBytes + 1} {
		if p, err := n.Write(ctx, "large.md", make([]byte, size)); !errors.Is(err, ErrNoteLimit) || p != "" {
			t.Fatal(err)
		}
	}
	for _, input := range []string{strings.Repeat("x", maxNotePath+1)} {
		if _, err := n.Read(ctx, vault, input); !errors.Is(err, ErrNoteLimit) {
			t.Fatal(err)
		}
		if _, err := n.Search(ctx, input); !errors.Is(err, ErrNoteLimit) {
			t.Fatal(err)
		}
	}
	testNoteFile(t, filepath.Join(vault, "big.md"), strings.Repeat("s", maxNoteBytes+1))
	if got, err := n.Read(ctx, vault, "big.md"); !errors.Is(err, ErrNoteLimit) || got != (Note{}) {
		t.Fatal(err)
	}
	if got, err := n.Search(ctx, "absent"); !errors.Is(err, ErrNoteLimit) || len(got) != 0 {
		t.Fatal(err)
	}
	for _, limit := range []string{"results", "content", "scan", "entries"} {
		t.Run(limit, func(t *testing.T) {
			v := t.TempDir()
			count, size, query := maxNoteResults+1, 1, "x"
			switch limit {
			case "content":
				count, size = 5, maxNoteBytes
			case "scan":
				count, size, query = 33, maxNoteBytes, "absent"
			case "entries":
				count, size, query = maxNoteEntries+1, 0, "absent"
			}
			for i := 0; i < count; i++ {
				testNoteFile(t, filepath.Join(v, fmt.Sprintf("%05d.md", i)), strings.Repeat("x", size))
			}
			handle, err := NewNotes(ctx, v, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer handle.Close()
			if got, err := handle.Search(ctx, query); !errors.Is(err, ErrNoteLimit) || len(got) != 0 {
				t.Fatalf("limit %d %v", len(got), err)
			}
		})
	}
}

func TestNotesInstructionVaultAlias(t *testing.T) {
	base := t.TempDir()
	rules := filepath.Join(base, ".claude/rules")
	testNoteFile(t, filepath.Join(rules, "ordinary.md"), "instructions")
	alias := filepath.Join(base, "vault")
	if err := os.Symlink(rules, alias); err != nil {
		t.Fatal(err)
	}
	n, err := NewNotes(context.Background(), alias, []string{base})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	for _, p := range []string{"ordinary.md", "new.md"} {
		if changed, err := n.Write(context.Background(), p, []byte("bad")); err == nil || changed != "" {
			t.Fatal("instruction vault write")
		}
	}
	testNoteBytes(t, filepath.Join(rules, "ordinary.md"), "instructions")
}

func TestNotesCancellationDuringIO(t *testing.T) {
	for _, stage := range []string{"read", "read-complete", "write", "scan"} {
		t.Run(stage, func(t *testing.T) {
			n, vault, _ := testNotes(t)
			original := strings.Repeat("old", 32<<10)
			testNoteFile(t, filepath.Join(vault, "a.md"), original)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			n.hook = func(s string) {
				if s == stage {
					calls++
					if calls == 2 || stage == "read-complete" {
						cancel()
					}
				}
			}
			var err error
			switch stage {
			case "write":
				_, err = n.Write(ctx, "a.md", []byte(strings.Repeat("new", 32<<10)))
			case "scan":
				_, err = n.Search(ctx, "old")
			default:
				_, err = n.Read(ctx, vault, "a.md")
			}
			if !errors.Is(err, context.Canceled) || calls == 0 {
				t.Fatal("I/O cancellation", err)
			}
			testNoteBytes(t, filepath.Join(vault, "a.md"), original)
		})
	}
}
func TestNotesRequestLimits(t *testing.T) {
	ctx := context.Background()
	for _, roots := range [][]string{make([]string, 32), make([]string, 17)} {
		for i := range roots {
			roots[i] = strings.Repeat("x", maxNotePath)
		}
		if n, err := NewNotes(ctx, "nonexistent", roots); !errors.Is(err, ErrNoteLimit) || n != nil {
			t.Fatal("root request limit", err)
		}
	}
	n, _, _ := testNotes(t)
	if p, err := n.Write(ctx, strings.Repeat("x", maxNotePath+1), nil); !errors.Is(err, ErrNoteLimit) || p != "" {
		t.Fatal(err)
	}
}

func TestNotesLateHardLink(t *testing.T) {
	n, vault, ref := testNotes(t)
	testNoteFile(t, filepath.Join(vault, "a.md"), "old")
	n.hook = func(stage string) {
		if stage == "access" {
			if err := os.Link(filepath.Join(vault, "a.md"), filepath.Join(ref, "alias.md")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if p, err := n.Write(context.Background(), "a.md", []byte("old")); err == nil || p != "" {
		t.Fatal("late link accepted")
	}
	testNoteBytes(t, filepath.Join(ref, "alias.md"), "old")
}
