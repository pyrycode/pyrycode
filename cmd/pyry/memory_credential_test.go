package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const memoryOld = "distinct-old-memory-secret"
const memoryNew = "distinct-new-memory-secret"

func testMemoryMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func testMemoryReject(t *testing.T, err error) {
	t.Helper()
	testMemoryCheck(t, !(err == nil || strings.Contains(err.Error(), memoryOld) || strings.Contains(err.Error(), memoryNew)), "failure admitted/leaked")
}

func testMemoryCheck(t *testing.T, ok bool, reason string) {
	t.Helper()
	if !ok {
		t.Fatal(reason)
	}
}

func testMemoryInput(t *testing.T, value string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	testMemoryMust(t, os.WriteFile(path, []byte(value), 0600))
	f, err := os.Open(path)
	testMemoryMust(t, err)
	t.Cleanup(func() { _ = f.Close() })
	return f
}
func testMemoryStore(t *testing.T) memoryCredentialStore {
	t.Helper()
	home := t.TempDir()
	testMemoryMust(t, os.Chmod(home, 0700))
	return memoryCredentialStore{home: home}
}
func testMemorySet(t *testing.T, s memoryCredentialStore, token string) string {
	t.Helper()
	ref, err := s.set(context.Background(), testMemoryInput(t, token))
	testMemoryMust(t, err)
	return ref
}
func testMemorySelection(t *testing.T, s memoryCredentialStore) (string, memoryCredentialSelection) {
	t.Helper()
	path := filepath.Join(s.home, ".pyry", "memory", "credentials", "openai.json")
	b, err := os.ReadFile(path)
	testMemoryMust(t, err)
	var sel memoryCredentialSelection
	testMemoryMust(t, json.Unmarshal(b, &sel))
	testMemoryCheck(t, !(strings.Contains(string(b), memoryOld) || strings.Contains(string(b), memoryNew)), "metadata leaked secret")
	return path, sel
}
func testMemoryResolve(t *testing.T, s memoryCredentialStore, ref, want string) {
	t.Helper()
	got, err := s.resolve(context.Background(), ref)
	testMemoryMust(t, err)
	testMemoryCheck(t, got == want, "credential resolution mismatch")
}

func TestMemoryCredentialInput(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		ok          bool
	}{
		{"raw", "plain", true}, {"lf", "plain\n", true}, {"crlf", "plain\r\n", true},
		{"max", strings.Repeat("x", maxAccountTokenBytes), true}, {"maxLF", strings.Repeat("x", maxAccountTokenBytes-1) + "\n", true},
		{"maxCRLF", strings.Repeat("x", maxAccountTokenBytes-2) + "\r\n", true}, {"over", strings.Repeat("x", maxAccountTokenBytes+1), false},
		{"overLF", strings.Repeat("x", maxAccountTokenBytes) + "\n", false}, {"overCRLF", strings.Repeat("x", maxAccountTokenBytes-1) + "\r\n", false},
		{"empty", "", false}, {"newline", "\n", false}, {"space", "bad value", false},
		{"doubleLF", "plain\n\n", false}, {"cr", "plain\r", false}, {"nonASCII", "tökén", false}, {"control", "a\x00b", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testMemoryStore(t)
			_, err := s.set(context.Background(), testMemoryInput(t, tc.input))
			testMemoryCheck(t, (err == nil) == tc.ok, "input acceptance mismatch")
		})
	}
}

func TestMemoryCredentialLifecycle(t *testing.T) {
	s := testMemoryStore(t)
	ok, err := s.status(context.Background())
	testMemoryMust(t, err)
	testMemoryCheck(t, !ok, "unconfigured status mismatch")
	ref := testMemorySet(t, s, memoryOld+"\r\n")
	testMemoryResolve(t, s, ref, memoryOld)
	testMemoryCheck(t, testMemorySet(t, s, memoryNew) == ref, "reference changed")
	testMemoryResolve(t, s, ref, memoryNew)
	path, sel := testMemorySelection(t, s)
	for p, mode := range map[string]os.FileMode{
		filepath.Join(s.home, ".pyry"):   os.ModeDir | 0700,
		filepath.Dir(filepath.Dir(path)): os.ModeDir | 0700,
		filepath.Dir(path):               os.ModeDir | 0700,
		path:                             0600, filepath.Join(filepath.Dir(path), sel.Generation+".token"): 0600,
	} {
		info, err := os.Stat(p)
		testMemoryMust(t, err)
		testMemoryCheck(t, info.Mode() == mode && ownedByEUID(info), "storage protection")
	}
	for _, bad := range []string{memoryOld, "/tmp/token", "keychain:Claude Code-credentials", "memory:openai:" + strings.Repeat("a", 32), ref + "/../"} {
		_, err := s.resolve(context.Background(), bad)
		testMemoryReject(t, err)
	}
}

func TestMemoryCredentialRefusals(t *testing.T) {
	for _, target := range []string{"directory", "selection", "token"} {
		for _, fault := range []string{"mode", "symlink", "fifo", "missing", "invalid", "owner"} {
			if (target == "directory" && (fault == "missing" || fault == "invalid")) || (target == "selection" && fault == "missing") {
				continue
			}
			t.Run(target+"/"+fault, func(t *testing.T) {
				s := testMemoryStore(t)
				ref := testMemorySet(t, s, memoryOld)
				selection, sel := testMemorySelection(t, s)
				path := selection
				if target == "directory" {
					path = filepath.Dir(selection)
				}
				if target == "token" {
					path = filepath.Join(filepath.Dir(selection), sel.Generation+".token")
				}
				backup := path + ".backup"
				testMemoryMust(t, os.Rename(path, backup))
				defer func() { _ = os.Remove(path); _ = os.Rename(backup, path) }()
				switch fault {
				case "mode", "owner":
					mode := os.FileMode(0600)
					if target == "directory" {
						mode = 0700
					}
					if fault == "mode" {
						mode |= 0040
					}
					if target == "directory" {
						testMemoryMust(t, os.Mkdir(path, mode))
					} else {
						b, err := os.ReadFile(backup)
						testMemoryMust(t, err)
						testMemoryMust(t, os.WriteFile(path, b, mode))
					}
					if fault == "owner" {
						f, err := os.Open(path)
						testMemoryMust(t, err)
						defer f.Close()
						info, err := f.Stat()
						testMemoryMust(t, err)
						info.Sys().(*syscall.Stat_t).Uid ^= 1
						testMemoryCheck(t, !memoryProtectedInfo(info, target == "directory"), "foreign owner admitted")
						return
					}
				case "symlink":
					testMemoryMust(t, os.Symlink(backup, path))
				case "fifo":
					testMemoryMust(t, syscall.Mkfifo(path, 0600))
				case "invalid":
					testMemoryMust(t, os.WriteFile(path, []byte(memoryNew+" bad"), 0600))
				}
				_, err := s.resolve(context.Background(), ref)
				testMemoryReject(t, err)
				_, err = s.set(context.Background(), testMemoryInput(t, memoryNew))
				testMemoryReject(t, err)
				_, err = s.status(context.Background())
				testMemoryReject(t, err)
				_ = os.Remove(path)
				testMemoryMust(t, os.Rename(backup, path))
				testMemoryResolve(t, s, ref, memoryOld)
			})
		}
	}
}

func TestMemoryCredentialFailedSave(t *testing.T) {
	s := testMemoryStore(t)
	ref := testMemorySet(t, s, memoryOld)
	path, _ := testMemorySelection(t, s)
	before, _ := os.ReadFile(path)
	for _, input := range []string{memoryNew, memoryNew + " "} {
		faulty := s
		faulty.rename = func(int, string, int, string) error { return errors.New(memoryNew) }
		_, err := faulty.set(context.Background(), testMemoryInput(t, input))
		testMemoryReject(t, err)
		after, err := os.ReadFile(path)
		testMemoryMust(t, err)
		testMemoryCheck(t, bytes.Equal(before, after), "selection changed on failure")
		testMemoryResolve(t, s, ref, memoryOld)
	}
}

func TestMemoryCredentialCancellation(t *testing.T) {
	for _, blocked := range []string{"stdin", "lock"} {
		for _, cause := range []string{"cancel", "deadline", "operation"} {
			t.Run(blocked+"/"+cause, func(t *testing.T) {
				s := testMemoryStore(t)
				ref := testMemorySet(t, s, memoryOld)
				input := testMemoryInput(t, memoryNew)
				var release func()
				if blocked == "stdin" {
					r, w, err := os.Pipe()
					testMemoryMust(t, err)
					input = r
					release = func() { _ = w.Close(); _ = r.Close() }
				} else {
					f, err := os.Open(filepath.Join(s.home, ".pyry", "memory", "credentials"))
					testMemoryMust(t, err)
					testMemoryMust(t, unix.Flock(int(f.Fd()), unix.LOCK_EX))
					release = func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }
				}
				defer func() { release() }()
				ctx, cancel := context.WithCancel(context.Background())
				if cause == "deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
				} else if cause == "cancel" {
					timer := time.AfterFunc(50*time.Millisecond, cancel)
					defer timer.Stop()
				}
				defer cancel()
				start := time.Now()
				_, err := s.set(ctx, input)
				limit := time.Second
				if cause == "operation" {
					limit = 11 * time.Second
				}
				testMemoryCheck(t, err != nil && time.Since(start) <= limit, "blocked operation did not stop")
				if blocked == "lock" {
					_, statusErr := s.status(ctx)
					_, resolveErr := s.resolve(ctx, ref)
					testMemoryCheck(t, !(statusErr == nil || resolveErr == nil), "cancelled read accepted")
				}
				release()
				release = func() {}
				testMemoryResolve(t, s, ref, memoryOld)
			})
		}
	}
}

func TestMemoryCredentialProcess(t *testing.T) {
	if os.Getenv("PYRY_MEMORY_HELPER") == "1" {
		mode := os.Getenv("PYRY_MEMORY_MODE")
		if mode == "resolve" {
			token, err := resolveMemoryCredential(context.Background(), os.Getenv("PYRY_MEMORY_REF"))
			if err != nil || token != memoryNew {
				os.Exit(7)
			}
			os.Exit(0)
		}
		args := strings.Split(mode, " ")
		if err := runArgs(append([]string{"pyry"}, args...)); err != nil {
			_, _ = io.WriteString(os.Stderr, err.Error())
			os.Exit(1)
		}
		os.Exit(0)
	}
	s := testMemoryStore(t)
	run := func(mode, input, ref string) (string, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMemoryCredentialProcess$")
		cmd.Env = append(os.Environ(), "HOME="+s.home, "PYRY_MEMORY_HELPER=1", "PYRY_MEMORY_MODE="+mode, "PYRY_MEMORY_REF="+ref)
		cmd.Stdin = strings.NewReader(input)
		b, err := cmd.CombinedOutput()
		return string(b), err
	}
	if b, err := run("help", "", ""); err != nil || !strings.Contains(b, "pyry memory credential set openai") || !strings.Contains(b, "pyry memory credential status openai") {
		t.Fatal("help missing commands", err)
	}
	if b, err := run("memory credential status openai", "", ""); err != nil || b != "{\"configured\":false}\n" {
		t.Fatal(b, err)
	}
	b, err := run("memory credential set openai", memoryOld, "")
	testMemoryMust(t, err)
	var out map[string]string
	if err = json.Unmarshal([]byte(b), &out); err != nil || out["reference"] == "" || b != "{\"reference\":\""+out["reference"]+"\"}\n" {
		t.Fatal("set JSON", err)
	}
	testMemoryCheck(t, !strings.Contains(b, memoryOld), "set leaked")
	testMemorySet(t, s, memoryNew)
	if b, err = run("resolve", "", out["reference"]); err != nil || b != "" {
		t.Fatal("fresh resolution", err)
	}
	if b, err = run("memory credential status openai", "", ""); err != nil || b != "{\"configured\":true}\n" {
		t.Fatal(b, err)
	}
	for _, args := range []string{"memory", "memory credential", "memory credential get openai", "memory credential set other", "memory credential set openai " + memoryNew, "memory credential status openai " + memoryOld} {
		b, err = run(args, "", "")
		testMemoryCheck(t, !(err == nil || strings.Contains(b, memoryNew) || strings.Contains(b, memoryOld)), "arguments admitted/leaked")
	}
}
