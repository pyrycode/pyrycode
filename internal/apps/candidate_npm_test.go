package apps

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCandidateEnvValue(env []string, name string) string {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, name+"="); ok {
			return value
		}
	}
	return ""
}

func TestCandidateNPMEnvironment(t *testing.T) {
	inheritedTmp := filepath.Join(testRoot(t), "source", "staging", "builds", "dist")
	if err := os.MkdirAll(inheritedTmp, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", inheritedTmp)
	t.Setenv("ANTHROPIC_API_KEY", "daemon-secret")
	t.Setenv("NODE_OPTIONS", "daemon-options")
	stage := testRoot(t)
	env, cleanup, err := candidateEnvironment(stage, "/selected/node")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})
	tmp := testCandidateEnvValue(env, "TMPDIR")
	resolved, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{tmp, resolved} {
		if rel, err := filepath.Rel(stage, path); err != nil || filepath.IsLocal(rel) {
			t.Fatalf("temporary data is inside staging: %q", path)
		}
		for _, part := range strings.Split(path, string(filepath.Separator)) {
			if part == "source" || part == "staging" || part == "builds" || part == "dist" {
				t.Fatalf("unsafe temporary data: %q", path)
			}
		}
	}
	info, err := os.Stat(tmp)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("temporary data is not private: %v, %v", info, err)
	}
	if len(env) != 8 || testCandidateEnvValue(env, "ANTHROPIC_API_KEY") != "" || testCandidateEnvValue(env, "NODE_OPTIONS") != "" {
		t.Fatal("inherited daemon environment", env)
	}
	second, cleanupSecond, err := candidateEnvironment(testRoot(t), "/selected/node")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanupSecond(); err != nil {
			t.Error(err)
		}
	})
	if testCandidateEnvValue(second, "TMPDIR") == tmp {
		t.Fatal("reused temporary storage")
	}
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
		t.Fatal("temporary storage is not fresh", entries, err)
	}
	// npm's config loader keys sources by pathname and rejects double loading.
	loaded := make(map[string]bool)
	for _, name := range []string{"userconfig", "globalconfig"} {
		path := testCandidateEnvValue(env, "npm_config_"+name)
		if loaded[path] {
			t.Fatalf("npm rejects double-loading config %q", path)
		}
		loaded[path] = true
		rel, err := filepath.Rel(stage, path)
		if err != nil || !filepath.IsLocal(rel) {
			t.Fatalf("config must be private to staging: %q", path)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe config: %v, %v", info, err)
		}
		if got := string(testBytes(t, path)); got != "" {
			t.Fatalf("config is not empty: %q", got)
		}
	}
}

func TestCandidateNPMPinning(t *testing.T) {
	stage := filepath.Join(testRoot(t), "stage with 'quotes'")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(testRoot(t), "selected-node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\nprintf 'v24.15.0\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	env, cleanup, err := candidateEnvironment(stage, node)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})
	for _, location := range []string{stage, filepath.Dir(stage)} {
		t.Run(filepath.Base(location), func(t *testing.T) {
			bin := filepath.Join(location, "node_modules", ".bin")
			if err := os.MkdirAll(bin, 0700); err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string]string{
				"node":  "#!/bin/sh\nprintf 'v20.0.0\\n'\n",
				"probe": "#!/usr/bin/env node\n",
			} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			// Model @npmcli/run-script's stage-to-root node_modules/.bin prepend.
			var bins []string
			for path := stage; ; path = filepath.Dir(path) {
				bins = append(bins, filepath.Join(path, "node_modules", ".bin"))
				if filepath.Dir(path) == path {
					break
				}
			}
			lifecycle := append([]string(nil), env...)
			for i, entry := range lifecycle {
				if strings.HasPrefix(entry, "PATH=") {
					lifecycle[i] = "PATH=" + strings.Join(bins, ":") + ":" + strings.TrimPrefix(entry, "PATH=")
				}
			}
			shell := testCandidateEnvValue(env, "npm_config_script_shell")
			if shell == "" {
				shell = "/bin/sh" // npm's default lifecycle shell
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := candidateCommand(ctx, stage, lifecycle, shell, []string{"-c", "node --version; probe"}, true)
			if err != nil || got != "v24.15.0\nv24.15.0" {
				t.Fatalf("lifecycle command/shebang selected another Node: %q, %v", got, err)
			}
			if err := os.RemoveAll(filepath.Join(location, "node_modules")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCandidateRealNPM(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("real Node is unavailable; deterministic npm fixtures still run")
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("real npm is unavailable; deterministic npm fixtures still run")
	}
	npm, err = filepath.EvalSymlinks(npm)
	if err != nil {
		t.Fatal(err)
	}
	stage := testRoot(t)
	env, cleanup, err := candidateEnvironment(stage, node)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})
	// Only local tarball installation is allowed, including npm's audit requests.
	env = append(env, "npm_config_offline=true", "npm_config_audit=false", "npm_config_fund=false")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	want, err := candidateCommand(ctx, stage, env, node, []string{"--version"}, true)
	if err != nil {
		t.Fatal(err)
	}
	version, err := candidateCommand(ctx, stage, env, node, []string{npm, "--version"}, true)
	if err != nil || !validRelease(version) {
		t.Fatalf("real npm could not load candidate configuration: %q, %v", version, err)
	}
	t.Logf("real Node %s, npm %s", want, version)
	probe := "node --version > $npm_lifecycle_event.node; runtime-probe > $npm_lifecycle_event.shebang"
	pkg := map[string]any{
		"name": "candidate-probe", "version": "1.0.0", "type": "module",
		"dependencies": map[string]string{"alternate-node": "file:./alternate-node.tgz"},
		"scripts":      map[string]string{"preinstall": probe, "install": probe, "postinstall": probe, "typecheck": probe, "test": probe, "build": probe},
	}
	testArtifactWrite(t, stage, "package.json", string(testJSON(t, pkg)))
	testArtifactWrite(t, stage, "package-lock.json", string(testJSON(t, map[string]any{
		"name": "candidate-probe", "version": "1.0.0", "lockfileVersion": 3,
		"packages": map[string]any{
			"": map[string]any{"name": "candidate-probe", "version": "1.0.0", "dependencies": pkg["dependencies"]},
			"node_modules/alternate-node": map[string]any{
				"version": "1.0.0", "resolved": "file:alternate-node.tgz", "hasInstallScript": true,
				"bin": map[string]string{"node": "node", "runtime-probe": "probe"},
			},
		},
	})))
	dependencyProbe := strings.ReplaceAll(probe, "runtime-probe", "./probe")
	testCandidateNPMTarball(t, filepath.Join(stage, "alternate-node.tgz"), map[string]string{
		"package.json": string(testJSON(t, map[string]any{
			"name": "alternate-node", "version": "1.0.0", "bin": map[string]string{"node": "node", "runtime-probe": "probe"},
			"scripts": map[string]string{"preinstall": dependencyProbe, "install": dependencyProbe, "postinstall": dependencyProbe},
		})),
		"node": "#!/bin/sh\nprintf 'v20.0.0\\n'\n", "probe": "#!/usr/bin/env node\nconsole.log(process.version);\n",
	})
	for _, args := range [][]string{{"ci"}, {"run", "typecheck"}, {"test"}, {"run", "build"}} {
		if _, err := candidateCommand(ctx, stage, env, node, append([]string{npm}, args...), false); err != nil {
			t.Fatalf("real npm %v: %v", args, err)
		}
	}
	for _, root := range []string{stage, filepath.Join(stage, "node_modules", "alternate-node")} {
		events := []string{"preinstall", "install", "postinstall"}
		if root == stage {
			events = append(events, "typecheck", "test", "build")
		}
		for _, event := range events {
			for _, suffix := range []string{".node", ".shebang"} {
				if got := strings.TrimSpace(string(testBytes(t, filepath.Join(root, event+suffix)))); got != want {
					t.Fatalf("%s/%s%s used %q, want %q", root, event, suffix, got, want)
				}
			}
		}
	}
}

func testCandidateNPMTarball(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + name, Mode: 0700, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}
