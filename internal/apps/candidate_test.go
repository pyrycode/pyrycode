package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

type candidateDouble struct {
	Node, NPM, Fail, Gate, Descendant, Fixture, Log, Ready, Mutate string
	Hold                                                           bool
}

func TestCandidateHelperProcess(t *testing.T) {
	idx := -1
	for i, arg := range os.Args {
		if arg == "--candidate" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	var c candidateDouble
	b, err := os.ReadFile(os.Args[idx+1])
	if err != nil || json.Unmarshal(b, &c) != nil {
		os.Exit(91)
	}
	args := os.Args[idx+2:]
	if args[0] == "--descendant" {
		if c.Descendant == "run build" && !c.Hold {
			signals := make(chan os.Signal, 1)
			signal.Notify(signals, syscall.SIGTERM)
			if publishOwnerPID(c.Ready+".child") != nil {
				os.Exit(99)
			}
			<-signals
			candidateHelperArtifacts(c.Fixture)
			os.Exit(0)
		}
		signal.Ignore(syscall.SIGTERM)
		if publishOwnerPID(c.Ready+".child") != nil {
			os.Exit(99)
		}
		time.Sleep(10 * time.Second)
		os.WriteFile(c.Ready+".escaped", []byte("live"), 0600)
		os.Exit(0)
	}
	if args[0] == "--version" {
		fmt.Println(c.Node)
		os.Exit(0)
	}
	args = args[1:] // npm CLI selected by the daemon
	if args[0] == "--version" {
		fmt.Println(c.NPM)
		os.Exit(0)
	}
	phase := strings.Join(args, " ")
	f, e := os.OpenFile(c.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		os.Exit(92)
	}
	fmt.Fprintln(f, phase)
	f.Close()
	if os.WriteFile(c.Log+".tmp", []byte(os.Getenv("TMPDIR")), 0600) != nil {
		os.Exit(92)
	}
	for _, k := range []string{"NOISE_SECRET", "NODE_OPTIONS", "PYRY_APP_DATA_DIR", "PYRY_APP_DB_PATH", "ANTHROPIC_API_KEY"} {
		if os.Getenv(k) != "" {
			os.Exit(93)
		}
	}
	cwd, _ := os.Getwd()
	if os.Getenv("HOME") != filepath.Join(cwd, ".home") {
		os.Exit(94)
	}
	if phase == "ci" {
		for _, n := range []string{"dist", "node_modules"} {
			if _, e := os.Lstat(n); !os.IsNotExist(e) {
				os.Exit(95)
			}
		}
	}
	if phase == "run typecheck" {
		version, e := exec.Command("node", "--version").Output()
		if e != nil || strings.TrimSpace(string(version)) != c.Node {
			os.Exit(96)
		}
	}
	if c.Gate == phase {
		os.WriteFile(c.Ready, []byte("ready"), 0600)
		for {
			if _, e := os.Stat(c.Ready + ".continue"); e == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if c.Descendant == phase {
		command := exec.Command(os.Args[0], "-test.run=^TestCandidateHelperProcess$", "--", "--candidate", os.Args[idx+1], "--descendant")
		command.Env = os.Environ()
		if command.Start() != nil {
			os.Exit(97)
		}
		for {
			if b, e := os.ReadFile(c.Ready + ".child"); e == nil && len(b) > 0 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if c.Hold {
			time.Sleep(10 * time.Second)
		}
		os.Exit(0)
	}
	if c.Fail == phase {
		fmt.Fprintln(os.Stderr, "SECRET /private/host/path")
		os.Exit(4)
	}
	if phase == "run build" {
		candidateHelperArtifacts(c.Fixture)
		if c.Mutate != "" {
			if os.WriteFile("package.json", []byte(c.Mutate), 0600) != nil {
				os.Exit(99)
			}
		}
	}
	os.Exit(0)
}

func candidateHelperArtifacts(fixture string) {
	for _, n := range []string{"dist", "node_modules"} {
		if e := filepath.WalkDir(filepath.Join(fixture, n), func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			rel, _ := filepath.Rel(fixture, p)
			if d.IsDir() {
				return os.MkdirAll(rel, 0700)
			}
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			return os.WriteFile(rel, b, 0600)
		}); e != nil {
			os.Exit(98)
		}
	}
}

func testCandidate(t *testing.T) (*Registry, Toolchain, candidateDouble, string) {
	t.Helper()
	r := testOpen(t)
	t.Cleanup(func() {
		builds := filepath.Join(r.root, testApp, "builds")
		if _, e := os.Stat(builds); os.IsNotExist(e) {
			return
		}
		parent, e := privateDir(builds)
		if os.IsNotExist(e) {
			return
		}
		if e != nil {
			t.Fatal(e)
		}
		defer parent.Close()
		entries, e := parent.ReadDir(-1)
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range entries {
			removeArtifactTemp(parent, entry.Name())
		}
	})
	m, e := ValidateManifest([]byte(testManifest), testHost)
	if e != nil {
		t.Fatal(e)
	}
	testRegister(t, r, m.JSON())
	files, e := templateFiles(m)
	if e != nil {
		t.Fatal(e)
	}
	if e = writeSource(context.Background(), r.root, testApp, files); e != nil {
		t.Fatal(e)
	}
	dir := testRoot(t)
	c := candidateDouble{Node: "v24.15.0", NPM: "11.12.0", Fixture: testArtifactTree(t), Log: filepath.Join(dir, "commands"), Ready: filepath.Join(dir, "ready")}
	config := filepath.Join(dir, "config.json")
	node := filepath.Join(dir, "selected-node")
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	wrapper := "#!/bin/sh\nexport GORACE=atexit_sleep_ms=0\nexec " + quote(binary) + " -test.run='^TestCandidateHelperProcess$' -- --candidate " + quote(config) + " \"$@\"\n"
	if e = os.WriteFile(node, []byte(wrapper), 0700); e != nil {
		t.Fatal(e)
	}
	testCandidateConfig(t, config, c)
	return r, Toolchain{Node: node, NPM: filepath.Join(dir, "npm-cli.js")}, c, config
}
func testCandidateConfig(t *testing.T, p string, c candidateDouble) {
	t.Helper()
	b, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func testCandidateVersion(t *testing.T, r *Registry, v string) {
	t.Helper()
	m := testMap(t)
	m["release_version"] = v
	testArtifactWrite(t, filepath.Join(r.root, testApp, "source"), "app.json", string(testJSON(t, m)))
}
func testCandidateFailure(t *testing.T, e error, phase string) {
	t.Helper()
	var f *CandidateFailure
	if !errors.As(e, &f) || f.Phase != phase || strings.Contains(e.Error(), "SECRET") || strings.Contains(e.Error(), "/private") {
		t.Fatalf("phase %s: %v", phase, e)
	}
}

func TestCandidateHistory(t *testing.T) {
	r, tools, c, config := testCandidate(t)
	t.Setenv("NOISE_SECRET", "SECRET")
	t.Setenv("NODE_OPTIONS", "SECRET")
	t.Setenv("PYRY_APP_DATA_DIR", "/private/data")
	testCandidateVersion(t, r, "1.9.0")
	source := testTreeBytes(t, filepath.Join(r.root, testApp, "source"))
	first, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
	if e != nil {
		t.Fatal(e)
	}
	if first.NodeVersion != "24.15.0" || first.NPMVersion != c.NPM || first.LockfileDigest != digestBytes(testBytes(t, filepath.Join(first.BuildRoot, "package-lock.json"))) {
		t.Fatal(first)
	}
	if _, e = VerifyBuild(context.Background(), r.root, testHost, testApp, first.BuildID); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(source, testTreeBytes(t, filepath.Join(r.root, testApp, "source"))) {
		t.Fatal("source changed")
	}
	if got := string(testBytes(t, c.Log)); got != "ci\nrun typecheck\ntest\nrun build\n" {
		t.Fatal(got)
	}
	testCandidateVersion(t, r, "1.10.0")
	if _, e = r.PrepareCandidate(context.Background(), testHost, testApp, tools); e != nil {
		t.Fatal(e)
	}
	testCandidateVersion(t, r, "1.9.0")
	c.Node = "v24.16.1"
	c.NPM = "12.0.0"
	testCandidateConfig(t, config, c)
	before := testBytes(t, filepath.Join(r.root, "registry.json"))
	retry, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
	if e != nil || retry != first || !reflect.DeepEqual(before, testBytes(t, filepath.Join(r.root, "registry.json"))) {
		t.Fatal(retry, e)
	}
	testArtifactWrite(t, c.Fixture, "dist/service/main.js", "changed")
	_, e = r.PrepareCandidate(context.Background(), testHost, testApp, tools)
	testCandidateFailure(t, e, "record")
	testCandidateVersion(t, r, "1.8.0")
	_, e = r.PrepareCandidate(context.Background(), testHost, testApp, tools)
	testCandidateFailure(t, e, "record")
}

func TestCandidatePhases(t *testing.T) {
	for _, phase := range []string{"ci", "run typecheck", "test", "run build"} {
		t.Run(phase, func(t *testing.T) {
			r, tools, c, config := testCandidate(t)
			c.Fail = phase
			testCandidateConfig(t, config, c)
			before := testBytes(t, filepath.Join(r.root, "registry.json"))
			_, e := r.PrepareCandidate(context.Background(), testHost, testApp, tools)
			want := map[string]string{"ci": "install", "run typecheck": "typecheck", "test": "test", "run build": "build"}[phase]
			testCandidateFailure(t, e, want)
			got := strings.Split(strings.TrimSpace(string(testBytes(t, c.Log))), "\n")
			if got[len(got)-1] != phase {
				t.Fatal(got)
			}
			if !reflect.DeepEqual(before, testBytes(t, filepath.Join(r.root, "registry.json"))) {
				t.Fatal("failed preparation mutated registry")
			}
		})
	}
	r, tools, c, config := testCandidate(t)
	c.Gate = "test"
	testCandidateConfig(t, config, c)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := r.PrepareCandidate(ctx, testHost, testApp, tools); done <- e }()
	waitFile(t, c.Ready)
	cancel()
	testCandidateFailure(t, <-done, "test")
	if strings.Contains(string(testBytes(t, c.Log)), "run build") {
		t.Fatal("built after cancellation")
	}
}
