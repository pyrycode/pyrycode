package memoryruntime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMacOSInstallCancelled(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("macOS arm64 host boundary")
	}
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := Install(ctx, Options{Home: home, Mode: "openai"})
	if !errors.Is(err, context.Canceled) || r.Launch != "" || r.Python != "" {
		t.Fatalf("supported macOS cancellation: runtime=%+v err=%v", r, err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled host check changed destination: %v %v", entries, err)
	}
}

func TestTargetPins(t *testing.T) {
	for _, tc := range []struct {
		goos, arch, mode, target string
	}{
		{"linux", "amd64", "openai", "ubuntu-24.04-amd64"},
		{"darwin", "arm64", "openai", "macos-arm64"},
		{"darwin", "amd64", "openai", ""},
		{"darwin", "arm64", "local", "macos-arm64"},
		{"linux", "amd64", "local", "ubuntu-24.04-amd64"},
		{"darwin", "arm64", "", ""},
		{"linux", "arm64", "openai", ""},
		{"windows", "amd64", "openai", ""},
	} {
		t.Run(tc.goos+"/"+tc.arch+"/"+tc.mode, func(t *testing.T) {
			p, err := selectPins(tc.goos, tc.arch, tc.mode)
			if tc.target == "" {
				if !errors.Is(err, ErrUnsupported) {
					t.Fatalf("unsupported combination: %v", err)
				}
				return
			}
			if err != nil || p.Target != tc.target {
				t.Fatalf("target selection: %+v %v", p, err)
			}
		})
	}
	linux, _ := selectPins("linux", "amd64", "openai")
	mac, _ := selectPins("darwin", "arm64", "openai")
	if linux.MinimumGLIBC != "2.39" || linux.MinimumMacOS != "" || mac.MinimumMacOS != "26.6.2" || mac.MinimumGLIBC != "" {
		t.Fatal("incorrect host requirements")
	}
	versions := func(p lock) map[string]string {
		v := map[string]string{"python": p.Python, "pip": p.Bootstrap}
		for _, a := range p.Wheels {
			if _, exists := v[a.Name]; exists {
				t.Fatalf("duplicate pin: %s", a.Name)
			}
			v[a.Name] = a.Version
		}
		return v
	}
	if len(mac.Wheels) != 32 || !reflect.DeepEqual(versions(linux), versions(mac)) {
		t.Fatal("macOS closure does not preserve exact component versions")
	}
	if !strings.Contains(mac.Runtime.URL, "20250918-aarch64-apple-darwin") {
		t.Fatal("wrong standalone Python target")
	}
	for _, a := range append([]artifact{mac.Runtime}, mac.Wheels...) {
		digest, err := hex.DecodeString(a.SHA256)
		if err != nil || len(digest) != 32 || !strings.HasPrefix(a.URL, "https://") {
			t.Fatalf("missing trusted artifact identity: %s", a.Name)
		}
	}
	for _, a := range mac.Wheels {
		if !strings.HasSuffix(a.File, ".whl") || strings.Contains(a.File, "linux") || strings.Contains(a.File, "x86_64") {
			t.Fatalf("non-macOS wheel: %s", a.File)
		}
	}
}

func TestMacOSMinimumVersion(t *testing.T) {
	for _, tc := range []struct {
		version, minimum string
		ok               bool
	}{
		{"26.6.2", "26.6.2", true}, {"26.6.3", "26.6.2", true},
		{"26.7", "26.6.2", true}, {"27.0", "26.6.2", true},
		{"26.6.1", "26.6.2", false}, {"26.6", "26.6.2", false},
		{"26.5.99", "26.6.2", false}, {"15.9.99", "26.6.2", false},
		{"", "26.6.2", false}, {"26", "26.6.2", false},
		{"26.6.2.1", "26.6.2", false}, {"26.6.x", "26.6.2", false},
		{"26..2", "26.6.2", false}, {"+26.6.2", "26.6.2", false},
		{"26.6.-2", "26.6.2", false}, {"9999999999999999999999.0", "26.6.2", false},
		{"26.6.2", "invalid", false},
	} {
		t.Run(tc.version+"/"+tc.minimum, func(t *testing.T) {
			if got := macOSAtLeast(tc.version, tc.minimum); got != tc.ok {
				t.Fatalf("minimum gate=%v want %v", got, tc.ok)
			}
		})
	}
}

func TestMacOSHostGate(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS host command")
	}
	if err := supportedHost(context.Background(), lock{MinimumMacOS: "999.0"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("older host accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := supportedMacOS(ctx, "26.6.2"); !errors.Is(err, context.Canceled) {
		t.Fatalf("host command lost cancellation: %v", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := supportedHost(ctx, lock{MinimumMacOS: "26.6.2"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("host gate lost deadline: %v", err)
	}
}

type smokeMode struct {
	Versions map[string]string `json:"versions"`
	Checks   map[string]string `json:"checks"`
	Executed int               `json:"executed"`
	Passed   int               `json:"passed"`
}

type smokeEvidence struct {
	CapturedAt   string               `json:"captured_at_utc"`
	OS           string               `json:"os"`
	Invocation   string               `json:"invocation"`
	Target       string               `json:"target"`
	OSRelease    string               `json:"os_release"`
	Architecture string               `json:"architecture"`
	Libc         string               `json:"libc"`
	MacOSVersion string               `json:"macos_version,omitempty"`
	MacOSBuild   string               `json:"macos_build,omitempty"`
	Modes        map[string]smokeMode `json:"modes"`
	Executed     int                  `json:"executed"`
	Passed       int                  `json:"passed"`
}

func smokeChecks(mode string) []string {
	checks := []string{"installer", "repeat_verified_setup", "python_and_dependency_versions", "memsearch_pymilvus_milvus_lite_openai_imports", "memsearch_cli_version", "pip_dependency_compatibility", "temporary_database_open_close", "retained_launch_verified"}
	if mode == "local" {
		checks = append(checks, "local_imports_and_cpu_tensor")
	}
	return checks
}

func TestSmokeEvidence(t *testing.T) {
	for _, target := range []struct{ file, goos, arch string }{{"smoke-linux-amd64.json", "linux", "amd64"}, {"smoke-macos-arm64.json", "darwin", "arm64"}} {
		t.Run(target.goos, func(t *testing.T) {
			data, err := os.ReadFile(target.file)
			if err != nil {
				t.Fatal(err)
			}
			var record smokeEvidence
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			if os.Getenv("PYRY_MEMORY_SMOKE_EVIDENCE") != "" && len(record.Modes) == 0 {
				t.Skip("native capture is armed for legacy evidence; validate both committed modes after promotion")
			}
			if record.OS != target.goos || record.Architecture != target.arch || len(record.Modes) != 2 || record.CapturedAt == "" || !strings.Contains(record.Invocation, "-tags memory_runtime_smoke -run '^TestSmoke$'") {
				t.Fatal("incomplete smoke identity/modes/invocation")
			}
			for _, mode := range []string{"openai", "local"} {
				p, err := selectPins(target.goos, target.arch, mode)
				if err != nil {
					t.Fatal(err)
				}
				if target.goos == "darwin" {
					if !macOSAtLeast(record.MacOSVersion, p.MinimumMacOS) || record.MacOSBuild == "" {
						t.Fatal("evidence target mismatch")
					}
				} else if !strings.Contains(record.OSRelease, "ID=ubuntu\n") || !strings.Contains(record.OSRelease, `VERSION_ID="24.04"`) || !macOSAtLeast(strings.TrimSpace(strings.TrimPrefix(record.Libc, "glibc ")), p.MinimumGLIBC) {
					t.Fatal("evidence target mismatch")
				}
				result := record.Modes[mode]
				if result.Versions["python"] != p.Python || result.Versions["pip"] != p.Bootstrap || len(result.Versions) != len(p.Wheels)+2 {
					t.Fatal("evidence bootstrap/closure mismatch:", mode)
				}
				for _, a := range p.Wheels {
					if result.Versions[a.Name] != a.Version {
						t.Fatalf("unproved %s pin: %s", mode, a.Name)
					}
				}
				checks := smokeChecks(mode)
				if result.Executed != len(checks) || result.Passed != len(checks) || len(result.Checks) != len(checks) {
					t.Fatal("smoke checks unexecuted or incomplete:", mode)
				}
				for _, check := range checks {
					if result.Checks[check] != "PASS" {
						t.Fatalf("unproved %s check: %s", mode, check)
					}
				}
			}
			if record.Executed != 17 || record.Passed != record.Executed {
				t.Fatal("aggregate smoke checks unexecuted or incomplete")
			}
		})
	}
}
