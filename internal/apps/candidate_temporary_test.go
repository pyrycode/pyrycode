package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCandidateTemporaryRejectsAppTrees(t *testing.T) {
	for _, component := range []string{"source", "staging", "builds", "dist"} {
		t.Run(component, func(t *testing.T) {
			root := testRoot(t)
			unsafe := filepath.Join(root, component)
			if err := os.Mkdir(unsafe, 0700); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "alias")
			if err := os.Symlink(unsafe, link); err != nil {
				t.Fatal(err)
			}
			for _, parent := range []string{unsafe, link} {
				if _, err := candidateTemporary(parent); !errors.Is(err, ErrInvalidStorage) {
					t.Fatal("accepted lexical/resolved app tree", parent, err)
				}
				if entries, err := os.ReadDir(unsafe); err != nil || len(entries) != 0 {
					t.Fatal("rejected temporary storage leaked", entries, err)
				}
			}
		})
	}
}

func TestCandidateTemporaryCleanup(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancellation"} {
		t.Run(outcome, func(t *testing.T) {
			r, tools, c, config := testCandidate(t)
			c.Gate = "test"
			if outcome == "failure" {
				c.Fail = "test"
			}
			testCandidateConfig(t, config, c)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := r.PrepareCandidate(ctx, testHost, testApp, tools); done <- err }()
			waitFile(t, c.Ready)
			tmp := string(testBytes(t, c.Log+".tmp"))
			if info, err := os.Stat(tmp); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
				t.Fatalf("missing private temporary storage during command: %v, %v", info, err)
			}
			testArtifactWrite(t, tmp, "child-data/nested", "temporary data")
			if outcome == "cancellation" {
				cancel()
			} else if err := os.WriteFile(c.Ready+".continue", nil, 0600); err != nil {
				t.Fatal(err)
			}
			err := <-done
			if outcome == "success" && err != nil {
				t.Fatal(err)
			} else if outcome != "success" {
				testCandidateFailure(t, err, "test")
			}
			if _, err := os.Stat(tmp); !os.IsNotExist(err) {
				t.Fatalf("temporary storage survived %s: %v", outcome, err)
			}
		})
	}
}

func TestCandidateSupportedTemplate(t *testing.T) {
	node := os.Getenv("HOSTED_APP_NODE")
	if node == "" {
		var err error
		node, err = exec.LookPath("node")
		if err != nil {
			t.Skip("set HOSTED_APP_NODE for supported-template acceptance; a skip supplies no evidence")
		}
		cmd := exec.Command(node, "--version")
		cmd.Env = []string{}
		version, err := cmd.Output()
		if err != nil || !supportedNode(strings.TrimSpace(string(version))) {
			t.Skip("set HOSTED_APP_NODE for supported-template acceptance; a skip supplies no evidence")
		}
	}
	node, err := filepath.Abs(node)
	if err != nil {
		t.Fatal(err)
	}
	npm := os.Getenv("HOSTED_APP_NPM_CLI")
	if npm == "" {
		npm, err = exec.LookPath("npm")
		if err != nil {
			t.Fatal("supported-template acceptance requires actual npm", err)
		}
	}
	npm, err = filepath.EvalSymlinks(npm)
	if err != nil {
		t.Fatal(err)
	}
	r := testOpen(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	source, err := r.Create(ctx, "Checked template")
	if err != nil {
		t.Fatal(err)
	}
	before := testTreeBytes(t, source.SourcePath)
	data := filepath.Join(r.root, source.AppID, "data")
	testArtifactWrite(t, data, "app.sqlite", "persistent sentinel")
	proof := filepath.Join(testRoot(t), "npm-results.jsonl")
	wrapper := filepath.Join(testRoot(t), "npm-observer.cjs")
	// Observe the real CLI without changing its arguments, cwd or preparation environment.
	observer := fmt.Sprintf(`const fs = require('node:fs'), cp = require('node:child_process');
const args = process.argv.slice(2);
const result = cp.spawnSync(process.execPath, [%s, ...args], { encoding: 'utf8', maxBuffer: 8 * 1024 * 1024 });
fs.appendFileSync(%s, JSON.stringify({ step: args.join(' '), tmp: process.env.TMPDIR, output: result.stdout, status: result.status }) + '\n');
process.stdout.write(result.stdout || ''); process.stderr.write(result.stderr || '');
process.exit(result.status ?? 1);
`, testJSON(t, npm), testJSON(t, proof))
	if err := os.WriteFile(wrapper, []byte(observer), 0600); err != nil {
		t.Fatal(err)
	}
	release, prepareErr := r.PrepareCandidate(ctx, testHost, source.AppID, Toolchain{Node: node, NPM: wrapper})
	if release.BuildID != "" {
		t.Cleanup(func() {
			parent, err := privateDir(filepath.Dir(release.BuildRoot))
			if err != nil {
				t.Error(err)
				return
			}
			defer parent.Close()
			removeArtifactTemp(parent, release.BuildID)
		})
	}
	var steps []string
	for _, line := range strings.Split(strings.TrimSpace(string(testBytes(t, proof))), "\n") {
		var result struct {
			Step, Tmp, Output string
			Status            int
		}
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, result.Step)
		if _, err := os.Stat(result.Tmp); !os.IsNotExist(err) {
			t.Fatalf("preparation temporary storage survived: %v", err)
		}
		if result.Step == "test" {
			counts := map[string]int{}
			for _, line := range strings.Split(result.Output, "\n") {
				var name string
				var count int
				if _, err := fmt.Sscanf(line, "# %s %d", &name, &count); err == nil {
					counts[name] = count
				}
			}
			t.Logf("maintained npm test: executed=%d passed=%d failed=%d skipped=%d", counts["tests"], counts["pass"], counts["fail"], counts["skipped"])
			if result.Status != 0 || counts["tests"] == 0 || counts["tests"] != counts["pass"] || counts["fail"] != 0 || counts["skipped"] != 0 {
				t.Fatalf("maintained suite did not pass: %s", result.Output)
			}
		}
	}
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	if !reflect.DeepEqual(steps, []string{"--version", "ci", "run typecheck", "test", "run build"}) {
		t.Fatal("incorrect checked preparation order", steps)
	}
	t.Logf("checked template Node %s, npm %s", release.NodeVersion, release.NPMVersion)
	if _, err := VerifyBuild(ctx, r.root, testHost, source.AppID, release.BuildID); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(r.root, testHost)
	if err != nil {
		t.Fatal(err)
	}
	if got := ownerRecord(t, reopened, source.AppID).ReleaseHistory; len(got) != 1 || got[0] != release {
		t.Fatal("release history was not persisted", got, release)
	}
	if !reflect.DeepEqual(before, testTreeBytes(t, source.SourcePath)) || string(testBytes(t, filepath.Join(data, "app.sqlite"))) != "persistent sentinel" {
		t.Fatal("preparation changed source or persistent data")
	}
}
