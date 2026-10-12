package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type memoryOwnerEvidence struct {
	OS           string                     `json:"os"`
	Architecture string                     `json:"architecture"`
	CapturedAt   string                     `json:"captured_at_utc"`
	Invocation   string                     `json:"invocation"`
	OSRelease    string                     `json:"os_release,omitempty"`
	Libc         string                     `json:"libc,omitempty"`
	MacOSVersion string                     `json:"macos_version,omitempty"`
	MacOSBuild   string                     `json:"macos_build,omitempty"`
	Modes        map[string]memoryOwnerMode `json:"modes"`
	Executed     int                        `json:"executed"`
	Passed       int                        `json:"passed"`
}
type memoryOwnerMode struct {
	Model    string            `json:"model"`
	Versions map[string]string `json:"versions"`
	Checks   map[string]string `json:"checks"`
	Executed int               `json:"executed"`
	Passed   int               `json:"passed"`
}

var memoryOwnerChecks = []string{"runtime_verified", "provider_probe", "backend_connected", "exclusive_owner", "backend_release", "compatible_reopen", "no_source_indexing", "provider_mode_isolation"}

func validateMemoryOwnerEvidence(e memoryOwnerEvidence) error {
	invalid := errors.New("missing or incompatible native owner evidence")
	if _, err := time.Parse(time.RFC3339, e.CapturedAt); err != nil {
		return invalid
	}
	if !strings.Contains(e.Invocation, "go test -tags memory_runtime_smoke -run '^TestSmoke$' -count=1 -v ./cmd/pyry") {
		return invalid
	}
	target := e.OS + "-" + e.Architecture
	switch target {
	case "linux-amd64":
		if !strings.Contains(e.OSRelease, `ID=ubuntu`) || !strings.Contains(e.OSRelease, `VERSION_ID="24.04"`) {
			return invalid
		}
		version := strings.TrimPrefix(strings.TrimSpace(e.Libc), "glibc ")
		parts := strings.Split(version, ".")
		if len(parts) != 2 {
			return invalid
		}
		major, _ := strconv.Atoi(parts[0])
		minor, _ := strconv.Atoi(parts[1])
		if major < 2 || (major == 2 && minor < 39) {
			return invalid
		}
	case "darwin-arm64":
		parts := strings.Split(e.MacOSVersion, ".")
		if len(parts) != 3 || e.MacOSBuild == "" {
			return invalid
		}
		a, _ := strconv.Atoi(parts[0])
		b, _ := strconv.Atoi(parts[1])
		c, _ := strconv.Atoi(parts[2])
		if a < 26 || (a == 26 && (b < 6 || b == 6 && c < 2)) {
			return invalid
		}
	default:
		return invalid
	}
	pinsFile := "linux-amd64.lock.json"
	if e.OS == "darwin" {
		pinsFile = "macos-arm64.lock.json"
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "memoryruntime", pinsFile))
	if err != nil {
		return err
	}
	var pins struct {
		Python string                           `json:"python"`
		Pip    string                           `json:"bootstrap_pip"`
		Wheels []struct{ Name, Version string } `json:"wheels"`
		Local  []struct{ Name, Version string } `json:"local_wheels"`
	}
	if json.Unmarshal(data, &pins) != nil {
		return invalid
	}
	total := 0
	if len(e.Modes) != 2 {
		return invalid
	}
	for _, provider := range []string{"openai", "local"} {
		mode := e.Modes[provider]
		expected := map[string]string{"python": pins.Python, "pip": pins.Pip}
		for _, wheel := range pins.Wheels {
			expected[wheel.Name] = wheel.Version
		}
		if provider == "local" {
			for _, wheel := range pins.Local {
				expected[wheel.Name] = wheel.Version
			}
		}
		if !reflect.DeepEqual(expected, mode.Versions) || mode.Model == "" || mode.Executed != len(memoryOwnerChecks) || mode.Passed != mode.Executed || len(mode.Checks) != len(memoryOwnerChecks) {
			return invalid
		}
		for _, check := range memoryOwnerChecks {
			if mode.Checks[check] != "PASS" {
				return invalid
			}
		}
		total += mode.Executed
	}
	if e.Executed != total || e.Passed != total {
		return invalid
	}
	return nil
}
func readMemoryOwnerEvidence(path string) (memoryOwnerEvidence, error) {
	var record memoryOwnerEvidence
	data, err := os.ReadFile(path)
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	return record, validateMemoryOwnerEvidence(record)
}
func TestMemoryOwnerEvidenceValidation(t *testing.T) {
	for _, target := range []string{"linux-amd64", "macos-arm64"} {
		// The existing installer native record supplies independently captured host/pins.
		data, err := os.ReadFile(filepath.Join("..", "..", "internal", "memoryruntime", "smoke-"+target+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var valid memoryOwnerEvidence
		if err := json.Unmarshal(data, &valid); err != nil {
			t.Fatal(err)
		}
		valid.Invocation = "go test -tags memory_runtime_smoke -run '^TestSmoke$' -count=1 -v ./cmd/pyry"
		valid.Executed = 0
		valid.Passed = 0
		for name, mode := range valid.Modes {
			mode.Model = "model"
			mode.Checks = map[string]string{}
			for _, check := range memoryOwnerChecks {
				mode.Checks[check] = "PASS"
			}
			mode.Executed = len(memoryOwnerChecks)
			mode.Passed = mode.Executed
			valid.Modes[name] = mode
			valid.Executed += mode.Executed
			valid.Passed += mode.Passed
		}
		if err := validateMemoryOwnerEvidence(valid); err != nil {
			t.Fatalf("synthetic valid %s: %v", target, err)
		}
		for _, mutation := range []string{"missing", "pins", "target", "checks", "counts"} {
			t.Run(target+"/"+mutation, func(t *testing.T) {
				encoded, _ := json.Marshal(valid)
				var bad memoryOwnerEvidence
				_ = json.Unmarshal(encoded, &bad)
				switch mutation {
				case "missing":
					bad.Modes = nil
				case "pins":
					bad.Modes["local"].Versions["memsearch"] = "wrong"
				case "target":
					bad.Architecture = "wrong"
				case "checks":
					delete(bad.Modes["openai"].Checks, "backend_release")
				case "counts":
					bad.Passed = 0
				}
				if validateMemoryOwnerEvidence(bad) == nil {
					t.Fatal("invalid evidence accepted")
				}
			})
		}
	}
	if _, err := readMemoryOwnerEvidence(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("missing record accepted")
	}
}
func TestMemoryOwnerNativeEvidence(t *testing.T) {
	for _, target := range []string{"linux-amd64", "macos-arm64"} {
		t.Run(target, func(t *testing.T) {
			path := filepath.Join("testdata", "memory-owner-"+target+".json")
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skip("blocked on #3290: native owner capture pending dispatcher companion execution; requires committed record")
			}
			if _, err := readMemoryOwnerEvidence(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}
