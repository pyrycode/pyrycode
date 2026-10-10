package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMemoryOSTool is a controlled credential tool, including hostile diagnostics.
func TestMemoryOSTool(t *testing.T) {
	if os.Getenv("PYRY_MEMORY_OS_TOOL") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	root := os.Getenv("PYRY_MEMORY_OS_ROOT")
	b, _ := json.Marshal(args)
	f, _ := os.OpenFile(filepath.Join(root, "argv"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	_, _ = f.Write(append(b, byte(10)))
	_ = f.Close()
	op, name, token := "", "", ""
	unattended := len(args) == 6 && args[0] == "-I" && args[1] == "-c"
	if unattended {
		if args[2] != memoryOSScript || (args[3] != "keychain" && args[3] != "secret-service") {
			os.Exit(9)
		}
		op, name = args[4], args[5]
		if op == "write" {
			input, _ := io.ReadAll(os.Stdin)
			token = string(input)
		}
	} else if len(args) == 1 && args[0] == "-i" {
		input, _ := io.ReadAll(os.Stdin)
		fields := strings.Fields(string(input))
		if len(fields) != 7 || fields[0] != "add-generic-password" || fields[1] != "-a" || fields[2] != "pyry-memory" || fields[3] != "-s" || fields[5] != "-X" || len(input) >= 4096 {
			os.Exit(9)
		}
		raw, err := hex.DecodeString(fields[6])
		if err != nil {
			os.Exit(9)
		}
		op, name, token = "write", fields[4], string(raw)
	} else if len(args) == 6 && args[0] == "find-generic-password" && args[1] == "-a" && args[2] == "pyry-memory" && args[3] == "-s" && args[5] == "-w" {
		op, name = "read", args[4]
	} else if len(args) == 5 && args[0] == "delete-generic-password" && args[1] == "-a" && args[2] == "pyry-memory" && args[3] == "-s" {
		op, name = "delete", args[4]
	} else if len(args) == 4 && args[0] == "store" && args[1] == "--label=Pyry memory OpenAI" && args[2] == "service" {
		b, _ := io.ReadAll(os.Stdin)
		op, name, token = "write", args[3], string(b)
	} else if len(args) == 3 && args[1] == "service" {
		name = args[2]
		if args[0] == "lookup" {
			op = "read"
		}
		if args[0] == "clear" {
			op = "delete"
		}
	}
	if op == "" || !strings.HasPrefix(name, "pyry.memory.openai.") {
		os.Exit(9)
	}
	fault, _ := os.ReadFile(filepath.Join(root, "fault"))
	current, _ := os.ReadFile(filepath.Join(root, name))
	if string(fault) == "locked" || string(fault) == "prompt-required" {
		if token == memoryOld || token == memoryNew {
			_ = os.WriteFile(filepath.Join(root, "user-token-submitted"), nil, 0600)
		}
		if unattended {
			os.Exit(8)
		}
		_ = os.WriteFile(filepath.Join(root, "prompt"), nil, 0600)
	}
	if string(fault) == "write-after" && op == "write" && token == memoryNew {
		_ = os.WriteFile(filepath.Join(root, name), []byte(token), 0600)
	}
	if (string(fault) == "write-after" && op == "write" && token == memoryNew) || (string(fault) == "new-read" && op == "read" && string(current) == memoryNew) || string(fault) == op || string(fault) == "all" || (string(fault) == "new-write" && op == "write" && token == memoryNew) {
		_, _ = io.WriteString(os.Stdout, memoryOld+memoryNew+token)
		_, _ = io.WriteString(os.Stderr, memoryOld+memoryNew+token)
		os.Exit(8)
	}
	if string(fault) == "hang-"+op {
		_ = os.WriteFile(filepath.Join(root, "started"), []byte(op), 0600)
		for {
			if _, err := os.Stat(filepath.Join(root, "release")); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	path := filepath.Join(root, name)
	switch op {
	case "write":
		if _, err := os.Stat(path); err == nil {
			os.Exit(9)
		}
		if os.WriteFile(path, []byte(token), 0600) != nil {
			os.Exit(8)
		}
	case "read":
		b, err := os.ReadFile(path)
		if err != nil {
			os.Exit(8)
		}
		if string(fault) == "invalid" {
			b = []byte("invalid secret")
		}
		if !unattended && args[0] == "find-generic-password" {
			for _, c := range b {
				if c < 0x20 || c > 0x7e {
					b = []byte(hex.EncodeToString(b))
					break
				}
			}
		}
		_, _ = os.Stdout.Write(b)
	case "delete":
		if os.Remove(path) != nil {
			os.Exit(8)
		}
	}
	os.Exit(0)
}

func testMemoryOS(t *testing.T, platform string) (memoryCredentialStore, string) {
	t.Helper()
	root := t.TempDir()
	testMemoryMust(t, os.Chmod(root, 0700))
	dir := t.TempDir()
	tool := "secret-tool"
	if platform == "darwin" {
		tool = "security"
	}
	// Shell only launches the test binary; it never sees streamed credential data.
	launcher := "#!/bin/sh\nexec '" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "' -test.run='^TestMemoryOSTool$' -- \"$@\"\n"
	testMemoryMust(t, os.WriteFile(filepath.Join(dir, tool), []byte(launcher), 0700))
	testMemoryMust(t, os.WriteFile(filepath.Join(dir, "python3"), []byte(launcher), 0700))
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv("PATH", dir)
	t.Setenv("PYRY_MEMORY_OS_TOOL", "1")
	t.Setenv("PYRY_MEMORY_OS_ROOT", root)
	s := testMemoryStore(t)
	s.goos = platform
	return s, root
}
func testMemoryOSFault(t *testing.T, root, fault string) {
	t.Helper()
	testMemoryMust(t, os.WriteFile(filepath.Join(root, "fault"), []byte(fault), 0600))
}
func testMemoryOSNoLeak(t *testing.T, s memoryCredentialStore, root string, tokens ...string) {
	t.Helper()
	path, _ := testMemorySelection(t, s)
	metadata, _ := os.ReadFile(path)
	args, _ := os.ReadFile(filepath.Join(root, "argv"))
	var fields map[string]string
	testMemoryMust(t, json.Unmarshal(metadata, &fields))
	var values []string
	for _, value := range fields {
		values = append(values, value)
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	for {
		var argv []string
		err := decoder.Decode(&argv)
		if errors.Is(err, io.EOF) {
			break
		}
		testMemoryMust(t, err)
		values = append(values, argv...)
	}
	for _, token := range append(tokens, memoryOld, memoryNew) {
		for _, value := range values {
			testMemoryCheck(t, !strings.Contains(value, token) && !strings.Contains(value, hex.EncodeToString([]byte(token))), "secret in metadata or argv")
		}
	}
}

func TestMemoryOSPreference(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, fault := range []string{"", "all", "read", "delete", "locked", "prompt-required"} {
			t.Run(platform+"/"+fault, func(t *testing.T) {
				s, root := testMemoryOS(t, platform)
				testMemoryOSFault(t, root, fault)
				ref := testMemorySet(t, s, memoryOld)
				_, sel := testMemorySelection(t, s)
				want := "file"
				if fault == "" {
					want = "secret-service"
					if platform == "darwin" {
						want = "keychain"
					}
				}
				testMemoryCheck(t, sel.Backend == want, "wrong initial backend")
				if fault == "locked" || fault == "prompt-required" {
					for _, marker := range []string{"prompt", "user-token-submitted"} {
						_, err := os.Stat(filepath.Join(root, marker))
						testMemoryCheck(t, errors.Is(err, os.ErrNotExist), "probe prompted or submitted user token before fallback")
					}
				}
				testMemoryOSFault(t, root, "")
				testMemoryResolve(t, s, ref, memoryOld)
				testMemoryCheck(t, testMemorySet(t, s, memoryNew) == ref, "changed stable reference")
				_, next := testMemorySelection(t, s)
				testMemoryCheck(t, next.Backend == want, "migrated selected backend")
				testMemoryOSNoLeak(t, s, root)
			})
		}
	}
}

func TestMemoryOSReplacement(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			s, root := testMemoryOS(t, platform)
			ref := testMemorySet(t, s, memoryOld)
			path, sel := testMemorySelection(t, s)
			before, _ := os.ReadFile(path)
			argvBefore, _ := os.ReadFile(filepath.Join(root, "argv"))
			for _, bad := range []string{"", "keychain:Claude Code-credentials", "memory:openai:" + strings.Repeat("a", 32)} {
				_, err := s.resolve(context.Background(), bad)
				testMemoryReject(t, err)
			}
			argvAfter, _ := os.ReadFile(filepath.Join(root, "argv"))
			testMemoryCheck(t, bytes.Equal(argvBefore, argvAfter), "unissued reference read a backend")
			for _, fault := range []string{"write", "write-after", "read", "new-read", "invalid", "locked", "prompt-required", "commit", "unsafe", "input"} {
				t.Run(fault, func(t *testing.T) {
					faulty := s
					input := memoryNew
					if fault == "commit" {
						faulty.rename = func(_ int, staged string, _ int, _ string) error {
							got, readErr := memoryOSRead(context.Background(), sel.Backend, strings.TrimSuffix(staged, ".selection"))
							testMemoryMust(t, readErr)
							testMemoryCheck(t, got == memoryNew, "commit failed before backend completion")
							return errors.New(memoryNew)
						}
					} else if fault == "unsafe" {
						testMemoryMust(t, os.Chmod(path, 0644))
					} else if fault == "input" {
						input += " "
					} else {
						testMemoryOSFault(t, root, fault)
					}
					_, err := faulty.set(context.Background(), testMemoryInput(t, input))
					testMemoryReject(t, err)
					after, _ := os.ReadFile(path)
					testMemoryCheck(t, bytes.Equal(before, after), "failed replacement changed selection")
					testMemoryOSFault(t, root, "")
					testMemoryMust(t, os.Chmod(path, 0600))
					testMemoryResolve(t, s, ref, memoryOld)
				})
			}
			for _, fault := range []string{"read", "invalid", "locked", "prompt-required"} {
				testMemoryOSFault(t, root, fault)
				_, err := s.resolve(context.Background(), ref)
				testMemoryReject(t, err)
				_, err = s.status(context.Background())
				testMemoryReject(t, err)
				testMemoryOSFault(t, root, "")
				testMemoryResolve(t, s, ref, memoryOld)
			}
			_, markerErr := os.Stat(filepath.Join(root, "prompt"))
			testMemoryCheck(t, errors.Is(markerErr, os.ErrNotExist), "selected backend prompted")
			secret := filepath.Join(root, "pyry.memory.openai."+sel.Generation)
			testMemoryMust(t, os.Rename(secret, secret+".backup"))
			_, err := s.status(context.Background())
			testMemoryReject(t, err)
			testMemoryMust(t, os.Rename(secret+".backup", secret))
			testMemoryResolve(t, s, ref, memoryOld)
			for i := 0; i < 2; i++ {
				testMemoryCheck(t, testMemorySet(t, s, memoryNew) == ref, "reference changed on replacement")
				testMemoryResolve(t, s, ref, memoryNew)
			}
			testMemoryOSNoLeak(t, s, root)
		})
	}
}

type testMemoryOSContext struct {
	context.Context
	done    chan struct{}
	failure error
}

func (c testMemoryOSContext) Done() <-chan struct{} { return c.done }
func (c testMemoryOSContext) Err() error {
	select {
	case <-c.done:
		return c.failure
	default:
		return nil
	}
}

func TestMemoryOSCancellation(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, operation := range []string{"probe", "read", "write"} {
			for _, deadline := range []bool{false, true} {
				t.Run(platform+"/"+operation+"/"+map[bool]string{false: "cancel", true: "timeout"}[deadline], func(t *testing.T) {
					s, root := testMemoryOS(t, platform)
					ref := ""
					if operation != "probe" {
						ref = testMemorySet(t, s, memoryOld)
					}
					fault := operation
					if fault == "probe" {
						fault = "write"
					}
					testMemoryOSFault(t, root, "hang-"+fault)
					failure := context.Canceled
					if deadline {
						failure = context.DeadlineExceeded
					}
					ctx := testMemoryOSContext{Context: context.Background(), done: make(chan struct{}), failure: failure}
					// Trigger the caller's deadline only after the requested operation starts.
					// A wall timer could otherwise expire during the prior-credential read.
					defer func() {
						select {
						case <-ctx.done:
						default:
							close(ctx.done)
						}
					}()
					done := make(chan error, 1)
					input := testMemoryInput(t, memoryNew)
					go func() { _, err := s.set(ctx, input); done <- err }()
					limit := time.After(3 * time.Second)
					for {
						if _, err := os.Stat(filepath.Join(root, "started")); err == nil {
							break
						}
						select {
						case <-limit:
							t.Fatal("tool did not start")
						default:
							time.Sleep(time.Millisecond)
						}
					}
					close(ctx.done)
					select {
					case err := <-done:
						testMemoryReject(t, err)
						testMemoryCheck(t, errors.Is(err, failure), "lost caller cancellation or deadline")
					case <-time.After(time.Second):
						t.Fatal("operation not bounded")
					}
					testMemoryMust(t, os.WriteFile(filepath.Join(root, "release"), nil, 0600))
					testMemoryOSFault(t, root, "")
					if ref != "" {
						testMemoryResolve(t, s, ref, memoryOld)
					} else {
						ok, err := s.status(context.Background())
						testMemoryMust(t, err)
						testMemoryCheck(t, !ok, "cancelled probe published selection")
					}
				})
			}
		}
	}
}

func TestMemoryOSFraming(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			s, root := testMemoryOS(t, platform)
			for _, token := range []string{"'\"\\`$();&|<>!", "deadbeef0123456789", strings.Repeat("x", 1900), strings.Repeat("x", 4094)} {
				ref := testMemorySet(t, s, token+"\r\n")
				testMemoryResolve(t, s, ref, token)
				testMemoryOSNoLeak(t, s, root, token)
			}
			for _, input := range []string{strings.Repeat("x", 4096), strings.Repeat("x", 4095) + "\n"} {
				ref := testMemorySet(t, s, input)
				testMemoryResolve(t, s, ref, strings.TrimSuffix(input, "\n"))
			}
			ref := testMemorySet(t, s, memoryOld)
			token := strings.Repeat("x", 4097)
			_, err := s.set(context.Background(), testMemoryInput(t, token))
			testMemoryReject(t, err)
			testMemoryResolve(t, s, ref, memoryOld)
			testMemoryOSNoLeak(t, s, root)
		})
	}
}

func TestMemoryOSProcess(t *testing.T) {
	if os.Getenv("PYRY_MEMORY_OS_PROCESS") == "1" {
		s := memoryCredentialStore{home: os.Getenv("HOME"), goos: os.Getenv("PYRY_MEMORY_OS_PLATFORM")}
		var err error
		switch os.Getenv("PYRY_MEMORY_OS_ACTION") {
		case "resolve":
			var token string
			token, err = s.resolve(context.Background(), os.Getenv("PYRY_MEMORY_REF"))
			if token != memoryNew {
				os.Exit(7)
			}
		case "status":
			var ok bool
			ok, err = s.status(context.Background())
			if err == nil {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]bool{"configured": ok})
			}
		case "set":
			var ref string
			ref, err = s.set(context.Background(), os.Stdin)
			if err == nil {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"reference": ref})
			}
		}
		if err != nil {
			_, _ = io.WriteString(os.Stderr, err.Error())
			os.Exit(1)
		}
		os.Exit(0)
	}
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			s, root := testMemoryOS(t, platform)
			run := func(action, input, ref string) (string, error) {
				cmd := exec.Command(os.Args[0], "-test.run=^TestMemoryOSProcess$")
				cmd.Env = append(os.Environ(), "HOME="+s.home, "PYRY_MEMORY_OS_PROCESS=1", "PYRY_MEMORY_OS_PLATFORM="+platform, "PYRY_MEMORY_OS_ACTION="+action, "PYRY_MEMORY_REF="+ref)
				cmd.Stdin = strings.NewReader(input)
				b, err := cmd.CombinedOutput()
				return string(b), err
			}
			b, err := run("status", "", "")
			testMemoryMust(t, err)
			testMemoryCheck(t, b == "{\"configured\":false}\n", "unconfigured output")
			b, err = run("set", memoryOld, "")
			testMemoryMust(t, err)
			var result map[string]string
			testMemoryMust(t, json.Unmarshal([]byte(b), &result))
			ref := result["reference"]
			testMemoryOSFault(t, root, "new-write")
			b, err = run("set", memoryNew, "")
			testMemoryCheck(t, err != nil && !strings.Contains(b, memoryNew) && !strings.Contains(b, memoryOld), "public failure leaked")
			testMemoryOSFault(t, root, "")
			testMemoryResolve(t, s, ref, memoryOld)
			testMemorySet(t, s, memoryNew)
			b, err = run("resolve", "", ref)
			testMemoryMust(t, err)
			testMemoryCheck(t, b == "", "resolution printed token")
			b, err = run("status", "", "")
			testMemoryMust(t, err)
			testMemoryCheck(t, b == "{\"configured\":true}\n", "configured output")
			for _, fault := range []string{"all", "locked", "prompt-required"} {
				testMemoryOSFault(t, root, fault)
				b, err = run("status", "", "")
				testMemoryCheck(t, err != nil && !strings.Contains(b, memoryOld) && !strings.Contains(b, memoryNew), "selected store failure leaked or fell back")
			}
			testMemoryOSFault(t, root, "")
			testMemoryResolve(t, s, ref, memoryNew)
			s = testMemoryStore(t)
			s.goos = platform
			testMemoryOSFault(t, root, "all")
			b, err = run("set", memoryOld, "")
			testMemoryMust(t, err)
			testMemoryMust(t, json.Unmarshal([]byte(b), &result))
			ref = result["reference"]
			testMemoryOSFault(t, root, "")
			b, err = run("set", memoryNew, "")
			testMemoryMust(t, err)
			testMemoryCheck(t, b == "{\"reference\":\""+ref+"\"}\n", "fresh process changed file reference")
			_, selected := testMemorySelection(t, s)
			testMemoryCheck(t, selected.Backend == "file", "fresh process migrated existing file selection")
			b, err = run("resolve", "", ref)
			testMemoryMust(t, err)
			testMemoryCheck(t, b == "", "file resolution printed token")
		})
	}
}

func TestMemoryOSInitialWriteFailure(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			s, root := testMemoryOS(t, platform)
			testMemoryOSFault(t, root, "new-write")
			_, err := s.set(context.Background(), testMemoryInput(t, memoryNew))
			testMemoryReject(t, err)
			ok, err := s.status(context.Background())
			testMemoryMust(t, err)
			testMemoryCheck(t, !ok, "failed initial write committed fallback")
			entries, err := os.ReadDir(filepath.Join(s.home, ".pyry", "memory", "credentials"))
			testMemoryMust(t, err)
			testMemoryCheck(t, len(entries) == 0, "failed write left selected or file-backed secret")
			testMemoryOSFault(t, root, "")
			testMemorySet(t, s, memoryOld)
			_, sel := testMemorySelection(t, s)
			testMemoryCheck(t, sel.Backend != "file", "failed write persisted fallback")
		})
	}
}
