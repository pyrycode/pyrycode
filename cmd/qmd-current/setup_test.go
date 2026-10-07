package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const currentMask = "{features,decisions}/**/*.md"

type qmdFixture struct {
	t                 *testing.T
	qmd, node, script string
	root, config, cwd string
	env               []string
}

func newQMDFixture(t *testing.T) *qmdFixture {
	t.Helper()
	qmd, err := exec.LookPath("qmd")
	if err != nil {
		t.Skip("installed QMD required for isolated collection verification")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node runtime required by QMD setup")
	}
	script, err := filepath.Abs("setup.mjs")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f := &qmdFixture{t: t, qmd: qmd, node: node, script: script,
		root: filepath.Join(dir, "corpus with spaces"), config: filepath.Join(dir, "config", "index.yml"), cwd: dir}
	// Override every QMD storage location before invoking even --version.
	overrides := map[string]string{
		"QMD_CONFIG_DIR": filepath.Dir(f.config), "INDEX_PATH": filepath.Join(dir, "index.sqlite"),
		"XDG_CONFIG_HOME": filepath.Join(dir, "xdg-config"), "XDG_CACHE_HOME": filepath.Join(dir, "cache"),
		"PWD": dir, "NO_COLOR": "1",
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; !replaced {
			f.env = append(f.env, entry)
		}
	}
	for key, value := range overrides {
		f.env = append(f.env, key+"="+value)
	}
	return f
}

func (f *qmdFixture) command(name string, args ...string) ([]byte, error) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = f.cwd, f.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		f.t.Logf("%s %v: %v\n%s\n%s", name, args, err, out, stderr.Bytes())
	}
	return out, err
}

func (f *qmdFixture) run(name string, args ...string) []byte {
	f.t.Helper()
	out, err := f.command(name, args...)
	if err != nil {
		f.t.Fatalf("command failed: %v", err)
	}
	return out
}

func testQMDWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testQMDJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (f *qmdFixture) readConfig() map[string]any {
	f.t.Helper()
	// Parse supported YAML independently of the setup command; JSON seeds are YAML too.
	code := `const fs = require('node:fs');
const yaml = require('node:module').createRequire(fs.realpathSync(process.argv[1]))('yaml');
console.log(JSON.stringify(yaml.parse(fs.readFileSync(process.argv[2], 'utf8'))));`
	var config map[string]any
	if err := json.Unmarshal(f.run(f.node, "-e", code, f.qmd, f.config), &config); err != nil {
		f.t.Fatal(err)
	}
	return config
}

func (f *qmdFixture) membership(collection string) []string {
	f.t.Helper()
	out := f.run(f.qmd, "ls", collection)
	paths := regexp.MustCompile(`qmd://[^\s]+`).FindAllString(string(out), -1)
	slices.Sort(paths)
	return paths
}

func TestQMDCurrentSetup(t *testing.T) {
	for _, scenario := range []string{"fresh", "wrong-root", "wrong-pattern", "ignore-wrong-scope", "ignore-correct-scope", "anchored-current", "aliased-current"} {
		t.Run(scenario, func(t *testing.T) {
			f := newQMDFixture(t)
			t.Logf("installed version: %s", bytes.TrimSpace(f.run(f.qmd, "--version")))
			included := map[string]string{
				"features/feature.md": "quasarfeature", "features/nested/nested.md": "quasarnestedfeature",
				"decisions/decision.md": "quasardecision", "decisions/nested/nested.md": "quasarnesteddecision",
			}
			excluded := map[string]string{
				"specs/historical.md": "quasarspec", "knowledge/codebase/history.md": "quasarhistory",
				"knowledge/architecture/other.md": "quasarother", "knowledge/features/plain.txt": "quasarfeaturetext",
				"knowledge/decisions/plain.txt": "quasardecisiontext",
			}
			knowledge := filepath.Join(f.root, "docs", "knowledge")
			for path, word := range included {
				testQMDWrite(t, filepath.Join(knowledge, path), "# Current explanation\n\n"+word+"\n")
			}
			for path, word := range excluded {
				testQMDWrite(t, filepath.Join(f.root, "docs", path), "# Excluded explanation\n\n"+word+"\n")
			}
			testQMDWrite(t, filepath.Join(f.root, "notes", "operator.md"), "# Operator\n\nquasaroperator\n")
			canonical, err := filepath.EvalSymlinks(knowledge)
			if err != nil {
				t.Fatal(err)
			}
			collections := map[string]any{
				"pyrycode-docs": map[string]any{"path": filepath.Join(f.root, "docs"), "pattern": "**/*",
					"ignore":  []string{"unrelated/**"},
					"context": map[string]any{"/": "Broad collection", "/knowledge/features": "Feature path"}},
				"operator-notes": map[string]any{"path": filepath.Join(f.root, "notes"), "pattern": "**/*.md",
					"includeByDefault": false, "context": map[string]any{"/": "Other collection", "/operator.md": "Leaf context"}},
			}
			if scenario != "fresh" {
				path, pattern := knowledge, "**/*"
				if scenario == "wrong-root" {
					path, pattern = filepath.Join(f.root, "docs"), currentMask
				}
				collections["pyrycode-current"] = map[string]any{"path": path, "pattern": pattern,
					"includeByDefault": false, "context": map[string]any{
						"/": "Current collection", "/features": "Current features", "/features/nested": "Nested path"}}
				current := collections["pyrycode-current"].(map[string]any)
				switch scenario {
				case "ignore-wrong-scope":
					current["path"], current["pattern"] = filepath.Join(f.root, "docs"), "**/*"
					current["ignore"] = []string{"**/features/**"}
				case "ignore-correct-scope":
					current["path"] = canonical
					current["pattern"], current["ignore"] = currentMask, []string{"features/**"}
				case "anchored-current", "aliased-current":
					current["path"] = filepath.Join(f.root, "docs")
				}
			}
			seed := map[string]any{"collections": collections, "global_context": "Global context: ää\nsecond line",
				"models": map[string]any{"embed": "custom-preserved-model"}}
			testQMDWrite(t, f.config, testQMDJSON(t, seed))
			if scenario == "anchored-current" || scenario == "aliased-current" {
				anchor, alias := "pyrycode-current", "pyrycode-docs"
				if scenario == "aliased-current" {
					anchor, alias = alias, anchor
				}
				// Real YAML sharing, accepted and indexed by QMD before reconciliation.
				yaml := "collections:\n  " + anchor + ": &shared " + testQMDJSON(t, collections["pyrycode-current"]) +
					"\n  " + alias + ": *shared\n  operator-notes: " + testQMDJSON(t, collections["operator-notes"]) +
					"\nglobal_context: " + testQMDJSON(t, seed["global_context"]) +
					"\nmodels: " + testQMDJSON(t, seed["models"]) + "\n"
				testQMDWrite(t, f.config, yaml)
			}
			seed = f.readConfig()
			collections = seed["collections"].(map[string]any)
			f.run(f.qmd, "update")
			contexts := f.run(f.qmd, "context", "list")
			otherMembership := map[string][]string{}
			for _, collection := range []string{"pyrycode-docs", "operator-notes"} {
				otherMembership[collection] = f.membership(collection)
			}
			if scenario == "wrong-pattern" && len(f.membership("pyrycode-current")) <= len(included) {
				t.Fatal("incorrect scope control did not index excluded documents")
			}
			if strings.HasPrefix(scenario, "ignore-") {
				before := f.membership("pyrycode-current")
				if len(before) == 0 || strings.Contains(strings.Join(before, "\n"), "/features/") {
					t.Fatalf("ignore control did not exclude feature documents: %v", before)
				}
			}
			f.run(f.node, f.script, "--repo", f.root)
			if scenario == "fresh" {
				collections["pyrycode-current"] = map[string]any{}
			}
			current := collections["pyrycode-current"].(map[string]any)
			current["path"], current["pattern"] = canonical, currentMask
			delete(current, "ignore")
			first, err := os.ReadFile(f.config)
			if err != nil {
				t.Fatal(err)
			}
			f.run(f.node, f.script, "--repo", f.root)
			second, err := os.ReadFile(f.config)
			if err != nil || !bytes.Equal(first, second) {
				t.Fatalf("rerun changed configuration: %v", err)
			}
			f.run(f.qmd, "update")
			for collection, before := range otherMembership {
				if after := f.membership(collection); !reflect.DeepEqual(before, after) {
					t.Fatalf("%s membership changed: before %v, after %v", collection, before, after)
				}
			}
			if after := f.run(f.qmd, "context", "list"); !bytes.Equal(contexts, after) {
				t.Fatalf("CLI contexts changed:\nbefore %s\nafter %s", contexts, after)
			}
			var expected []string
			for path := range included {
				expected = append(expected, "qmd://pyrycode-current/"+path)
			}
			slices.Sort(expected)
			if got := f.membership("pyrycode-current"); !reflect.DeepEqual(got, expected) {
				t.Fatalf("indexed membership: got %v, want %v", got, expected)
			}
			t.Logf("membership (%d): %v", len(expected), expected)
			search := func(word, collection string) []struct{ File string } {
				t.Helper()
				var results []struct{ File string }
				if err := json.Unmarshal(f.run(f.qmd, "search", word, "-c", collection, "--json"), &results); err != nil {
					t.Fatal(err)
				}
				return results
			}
			for path, word := range included {
				if results := search(word, "pyrycode-current"); len(results) != 1 || results[0].File != "qmd://pyrycode-current/"+path {
					t.Fatalf("included search %q: %v", word, results)
				}
			}
			for path, word := range excluded {
				if results := search(word, "pyrycode-current"); len(results) != 0 {
					t.Fatalf("excluded search %q: %v", word, results)
				}
				if results := search(word, "pyrycode-docs"); len(results) != 1 || results[0].File != "qmd://pyrycode-docs/"+path {
					t.Fatalf("broad positive control %q: %v", word, results)
				}
			}
			if got := f.readConfig(); !reflect.DeepEqual(got, seed) {
				t.Fatalf("configuration preservation: got %#v, want %#v", got, seed)
			}
			t.Log("PASS: setup, rerun, collection/context preservation, 4 included searches, 5 excluded searches and 5 broad positive controls")
		})
	}
}

func TestQMDCurrentDefaultAndInvalidConfig(t *testing.T) {
	f := newQMDFixture(t)
	f.run(f.node, f.script)
	checkout, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(f.script), "..", "..", "docs", "knowledge"))
	if err != nil {
		t.Fatal(err)
	}
	current := f.readConfig()["collections"].(map[string]any)["pyrycode-current"].(map[string]any)
	if current["path"] != checkout || current["pattern"] != currentMask {
		t.Fatalf("default checkout scope: %v", current)
	}
	for _, invalid := range []string{"collections: [", "collections: []", "collections:\n  pyrycode-current: invalid"} {
		testQMDWrite(t, f.config, invalid)
		if _, err := f.command(f.node, f.script); err == nil {
			t.Fatalf("accepted invalid configuration %q", invalid)
		}
		if after, err := os.ReadFile(f.config); err != nil || string(after) != invalid {
			t.Fatalf("corrupted invalid configuration: %q, %v", after, err)
		}
	}
}
