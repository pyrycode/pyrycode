package claudetape

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const CLIMetadataDirectory = ".pyry-test-recording"

type CLIBinding struct {
	home  string
	args  []string
	files map[string]string
}

func NewCLIBinding(args []string, home, workdir string) (*CLIBinding, error) {
	fail := errors.New("unreviewed CLI spawn arguments")
	if !filepath.IsAbs(home) || workdir != home {
		return nil, fail
	}
	b := &CLIBinding{home: home, args: append([]string(nil), args...), files: map[string]string{}}
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if seen[flag] {
			return nil, fail
		}
		seen[flag] = true
		switch flag {
		case "--verbose":
		case "--settings", "--append-system-prompt-file":
			if i+1 == len(args) || !filepath.IsAbs(args[i+1]) {
				return nil, fail
			}
			for _, prior := range b.files {
				if prior == args[i+1] {
					return nil, fail
				}
			}
			b.files[flag] = args[i+1]
			b.args[i+1] = "${CLI_FILE:" + flag + "}"
			i++
		case "--input-format", "--output-format", "--permission-mode", "--model", "--effort", "--max-turns", "--allowed-tools":
			if i+1 == len(args) || args[i+1] == "" {
				return nil, fail
			}
			value := args[i+1]
			if flag == "--permission-mode" && value != "dontAsk" {
				return nil, fail
			}
			if (flag == "--input-format" || flag == "--output-format") && value != "stream-json" {
				return nil, fail
			}
			if flag == "--max-turns" {
				n, err := strconv.Atoi(value)
				if err != nil || n < 1 {
					return nil, fail
				}
			}
			i++
		default:
			return nil, fail
		}
	}
	for _, flag := range []string{"--input-format", "--output-format", "--verbose", "--permission-mode", "--settings", "--append-system-prompt-file", "--model", "--effort", "--max-turns", "--allowed-tools"} {
		if !seen[flag] {
			return nil, fail
		}
	}
	return b, nil
}
func (b *CLIBinding) Args() []string { return append([]string(nil), b.args...) }

// Text leaves every other byte intact, including Unicode escaping and JSONL
// formatting. It binds only the declared HOME and exact native file arguments.
func (b *CLIBinding) Text(text string, restore bool) string {
	type binding struct{ actual, label string }
	items := []binding{}
	for flag, path := range b.files {
		items = append(items, binding{path, "${CLI_FILE:" + flag + "}"})
	}
	sort.Slice(items, func(i, j int) bool { return len(items[i].actual) > len(items[j].actual) })
	items = append(items, binding{b.home, "${CLI_HOME}"})
	for _, item := range items {
		if restore {
			text = strings.ReplaceAll(text, item.label, item.actual)
		} else {
			text = strings.ReplaceAll(text, item.actual, item.label)
		}
	}
	return text
}
func (b *CLIBinding) Files() (map[string]json.RawMessage, error) {
	result := map[string]json.RawMessage{"$cwd": json.RawMessage(`"."`), "$recording-input-policy": json.RawMessage(`"single-stdin-eof/private-test-metadata-v1"`)}
	total, count := 0, 0
	add := func(key, path string) error {
		fail := errors.New("CLI input file is missing changed or oversized")
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > MaxBytes {
			return fail
		}
		f, err := os.Open(path)
		if err != nil {
			return fail
		}
		opened, statErr := f.Stat()
		raw, readErr := io.ReadAll(io.LimitReader(f, MaxBytes+1))
		closeErr := f.Close()
		if statErr != nil || !os.SameFile(info, opened) || readErr != nil || closeErr != nil || !utf8.Valid(raw) {
			return fail
		}
		total += len(raw)
		count++
		if total > MaxBytes || count > 64 {
			return fail
		}
		encoded, err := json.Marshal(struct {
			Content string `json:"content"`
			Mode    uint32 `json:"mode"`
		}{b.Text(string(raw), false), uint32(info.Mode().Perm())})
		if err != nil || Safe(encoded) != nil {
			return fail
		}
		result[key] = encoded
		return nil
	}
	for flag, path := range b.files {
		if err := add(flag, path); err != nil {
			return nil, err
		}
	}
	err := filepath.WalkDir(b.home, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(b.home, path)
		if err != nil {
			return err
		}
		if rel == CLIMetadataDirectory {
			if !entry.IsDir() {
				return errors.New("CLI metadata directory changed")
			}
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("CLI input inventory refuses symlinks")
		}
		if entry.IsDir() {
			return nil
		}
		return add("$workdir/"+filepath.ToSlash(rel), path)
	})
	if err != nil {
		return nil, err
	}
	if result["$workdir/prompt.txt"] == nil || result["$workdir/system.txt"] == nil {
		return nil, errors.New("missing actual agent-run prompt files")
	}
	return result, nil
}
func (b *CLIBinding) Check(t *CLITape, input string, files map[string]json.RawMessage) error {
	if !reflect.DeepEqual(b.Args(), t.Args) || (&Tape{Files: t.Files}).CheckFiles(files) != nil {
		return errors.New("CLI argv or input files changed")
	}
	a, err := object([]byte(b.Text(input, false)))
	if err != nil {
		return err
	}
	saved, err := object([]byte(t.Input))
	if err != nil || !reflect.DeepEqual(a, saved) {
		return errors.New("CLI stdin changed")
	}
	return nil
}

func validateCLISavedArgs(args []string) error {
	home := "/tmp/cli-argument-validation"
	concrete := append([]string(nil), args...)
	for i, arg := range args {
		if arg == "--settings" || arg == "--append-system-prompt-file" {
			if i+1 == len(args) || args[i+1] != "${CLI_FILE:"+arg+"}" {
				return errors.New("CLI saved file argument changed")
			}
			concrete[i+1] = home + "/" + arg[2:]
		}
	}
	b, err := NewCLIBinding(concrete, home, home)
	if err != nil || !reflect.DeepEqual(b.Args(), args) {
		return errors.New("CLI saved argv inventory changed")
	}
	return nil
}
