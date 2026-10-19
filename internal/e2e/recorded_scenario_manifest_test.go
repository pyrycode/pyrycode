//go:build e2e

package e2e

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"testing"
)

//go:embed testdata/claude-scenarios.json
var recordedManifestJSON []byte

type recordedScenarioDefinition struct {
	ComparisonPolicy string            `json:"comparison_policy"`
	InitialYOLO      bool              `json:"initial_yolo"`
	Name             string            `json:"name"`
	ClaudeArgs       []string          `json:"claude_args"`
	StdioPermission  bool              `json:"stdio_permission"`
	LiveTests        []string          `json:"live_tests"`
	RetainedLive     []string          `json:"retained_live"`
	LiveContracts    map[string]string `json:"live_contracts"`
}

type recordedManifest struct {
	Version       int                          `json:"version"`
	ProviderRoots []string                     `json:"provider_roots"`
	Scenarios     []recordedScenarioDefinition `json:"scenarios"`
	Batches       map[string][]string          `json:"batches"`
	Reviews       map[string]struct {
		Disposition    string `json:"disposition"`
		Reason         string `json:"reason"`
		ContractSHA256 string `json:"contract_sha256"`
	} `json:"reviews"`
}

func (m recordedManifest) scenario(name string) recordedScenarioDefinition {
	for _, s := range m.Scenarios {
		if s.Name == name {
			return s
		}
	}
	return recordedScenarioDefinition{}
}

func recordedManifestFor(t *testing.T) recordedManifest {
	t.Helper()
	var m recordedManifest
	d := json.NewDecoder(bytes.NewReader(recordedManifestJSON))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || m.Version != 1 || len(m.Scenarios) == 0 {
		t.Fatal("invalid recorded scenario manifest")
	}
	names := map[string]bool{}
	for _, s := range m.Scenarios {
		if !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(s.Name) || names[s.Name] || recordedScenarioDrivers[s.Name] == nil {
			t.Fatal("scenario manifest has duplicate, invalid or unimplemented driver")
		}
		if s.ComparisonPolicy != "strict_ordered" && (s.ComparisonPolicy != "native_coding_prefix" || s.Name != "native-suggestions") {
			t.Fatal("unsupported scenario comparison policy")
		}
		if s.Name == "native-suggestions" && s.ComparisonPolicy != "native_coding_prefix" {
			t.Fatal("native suggestions must declare their bounded comparison policy")
		}
		names[s.Name] = true
	}
	if len(names) != len(recordedScenarioDrivers) {
		t.Fatal("registered driver is missing from scenario manifest")
	}
	return m
}

func selectedRecordedScenarios(t *testing.T) []string {
	t.Helper()
	m := recordedManifestFor(t)
	var selected []string
	raw, set := os.LookupEnv("PYRY_CLAUDE_TAPE_SCENARIOS")
	if set {
		if json.Unmarshal([]byte(raw), &selected) != nil || len(selected) == 0 {
			t.Fatal("invalid or empty recorded scenario selection")
		}
	} else {
		for _, s := range m.Scenarios {
			selected = append(selected, s.Name)
		}
	}
	seen := map[string]bool{}
	for _, name := range selected {
		if seen[name] || m.scenario(name).Name == "" {
			t.Fatal("unknown or duplicate recorded scenario selection")
		}
		seen[name] = true
	}
	return selected
}
