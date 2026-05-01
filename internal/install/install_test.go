package install

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstall_Systemd_BareTemplate(t *testing.T) {
	home := t.TempDir()
	path, plat, err := Install(Options{
		Platform: PlatformSystemd,
		Name:     "pyry",
		Binary:   "/home/test/.local/bin/pyry",
		HomeDir:  home,
		EnvPath:  "", // explicitly empty → fall back to conservative default
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if plat != PlatformSystemd {
		t.Errorf("plat = %v, want systemd", plat)
	}
	wantPath := filepath.Join(home, ".config/systemd/user/pyry.service")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(body)

	for _, want := range []string{
		"[Unit]",
		"WorkingDirectory=%h/pyry-workspace",
		"ExecStart=/home/test/.local/bin/pyry",
		"customize the claude flags pyry forwards",
		"Restart=always",
		"WantedBy=default.target",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n--- file ---\n%s", want, got)
		}
	}

	// Bare template must NOT have baked-in claude flags.
	for _, banned := range []string{
		"--dangerously-skip-permissions plugin:",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("bare template contained baked-in flag fragment %q", banned)
		}
	}
}

func TestInstall_Systemd_BakedFlags(t *testing.T) {
	home := t.TempDir()
	path, _, err := Install(Options{
		Platform: PlatformSystemd,
		Name:     "pyry",
		Binary:   "/home/test/.local/bin/pyry",
		HomeDir:  home,
		ClaudeArgs: []string{
			"--dangerously-skip-permissions",
			"--channels",
			"plugin:discord@claude-plugins-official",
		},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	body, _ := os.ReadFile(path)
	got := string(body)

	wantExec := "ExecStart=/home/test/.local/bin/pyry --dangerously-skip-permissions --channels plugin:discord@claude-plugins-official"
	if !strings.Contains(got, wantExec) {
		t.Errorf("ExecStart missing or wrong\nwant substring: %s\n--- file ---\n%s", wantExec, got)
	}

	// Baked mode should NOT include the "customize the claude flags" guidance.
	if strings.Contains(got, "customize the claude flags pyry forwards") {
		t.Errorf("baked-flags unit contained the bare-template comment block")
	}
}

func TestInstall_Systemd_NamedInstance(t *testing.T) {
	home := t.TempDir()
	path, _, err := Install(Options{
		Platform: PlatformSystemd,
		Name:     "elli",
		Binary:   "/home/test/.local/bin/pyry",
		HomeDir:  home,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if want := filepath.Join(home, ".config/systemd/user/elli.service"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	body, _ := os.ReadFile(path)
	got := string(body)
	if !strings.Contains(got, "ExecStart=/home/test/.local/bin/pyry -pyry-name elli") {
		t.Errorf("non-default name should bake -pyry-name elli into ExecStart\n--- file ---\n%s", got)
	}
}

func TestInstall_Launchd_BareTemplate(t *testing.T) {
	home := t.TempDir()
	path, plat, err := Install(Options{
		Platform: PlatformLaunchd,
		Name:     "pyry",
		Binary:   "/Users/test/.local/bin/pyry",
		HomeDir:  home,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if plat != PlatformLaunchd {
		t.Errorf("plat = %v, want launchd", plat)
	}
	wantPath := filepath.Join(home, "Library/LaunchAgents/dev.pyrycode.pyry.plist")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}

	body, _ := os.ReadFile(path)
	got := string(body)

	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`<key>Label</key>`,
		`<string>dev.pyrycode.pyry</string>`,
		`<string>/Users/test/.local/bin/pyry</string>`,
		filepath.Join(home, "pyry-workspace"), // expanded WorkDir
		`/tmp/pyry.pyry.out.log`,
		"customize the claude flags",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n--- file ---\n%s", want, got)
		}
	}
}

func TestInstall_Launchd_BakedFlags(t *testing.T) {
	home := t.TempDir()
	path, _, err := Install(Options{
		Platform: PlatformLaunchd,
		Name:     "pyry",
		Binary:   "/Users/test/.local/bin/pyry",
		HomeDir:  home,
		ClaudeArgs: []string{
			"--dangerously-skip-permissions",
			"--model", "sonnet",
		},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	body, _ := os.ReadFile(path)
	got := string(body)

	for _, want := range []string{
		`<string>--dangerously-skip-permissions</string>`,
		`<string>--model</string>`,
		`<string>sonnet</string>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("baked plist missing %q\n--- file ---\n%s", want, got)
		}
	}
}

func TestInstall_RefusesExistingFile(t *testing.T) {
	home := t.TempDir()

	// Pre-create the destination.
	dst := filepath.Join(home, ".config/systemd/user/pyry.service")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dst, []byte("PRE-EXISTING"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, _, err := Install(Options{
		Platform: PlatformSystemd,
		Name:     "pyry",
		Binary:   "/home/test/.local/bin/pyry",
		HomeDir:  home,
	})
	if !errors.Is(err, ErrFileExists) {
		t.Fatalf("err = %v, want ErrFileExists", err)
	}

	// File must still be the pre-existing content.
	got, _ := os.ReadFile(dst)
	if string(got) != "PRE-EXISTING" {
		t.Errorf("Install clobbered pre-existing file: %q", got)
	}
}

func TestInstall_ForceOverwrites(t *testing.T) {
	home := t.TempDir()

	dst := filepath.Join(home, ".config/systemd/user/pyry.service")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dst, []byte("PRE-EXISTING"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, _, err := Install(Options{
		Platform: PlatformSystemd,
		Name:     "pyry",
		Binary:   "/home/test/.local/bin/pyry",
		HomeDir:  home,
		Force:    true,
	})
	if err != nil {
		t.Fatalf("Install with force: %v", err)
	}

	got, _ := os.ReadFile(dst)
	if string(got) == "PRE-EXISTING" {
		t.Errorf("Force did not overwrite the existing file")
	}
}

func TestPlatformAuto_Detect(t *testing.T) {
	plat := PlatformAuto.Detect()
	switch runtime.GOOS {
	case "linux":
		if plat != PlatformSystemd {
			t.Errorf("on linux, Detect() = %v, want systemd", plat)
		}
	case "darwin":
		if plat != PlatformLaunchd {
			t.Errorf("on darwin, Detect() = %v, want launchd", plat)
		}
	}
}

func TestDerivePathEnv_Systemd_RewritesHomePrefix(t *testing.T) {
	home := "/home/pyry"
	envPath := "/home/pyry/.local/bin:/home/pyry/.nvm/versions/node/v24/bin:/home/linuxbrew/.linuxbrew/bin:/usr/bin:/bin"
	got := derivePathEnv(PlatformSystemd, envPath, home)
	want := "%h/.local/bin:%h/.nvm/versions/node/v24/bin:/home/linuxbrew/.linuxbrew/bin:/usr/bin:/bin"
	if got != want {
		t.Errorf("derivePathEnv:\n got: %s\nwant: %s", got, want)
	}
}

func TestDerivePathEnv_Launchd_KeepsAbsolutePaths(t *testing.T) {
	home := "/Users/test"
	envPath := "/Users/test/.local/bin:/opt/homebrew/bin:/usr/bin:/bin"
	got := derivePathEnv(PlatformLaunchd, envPath, home)
	// Launchd doesn't have a %h equivalent — paths stay literal.
	want := "/Users/test/.local/bin:/opt/homebrew/bin:/usr/bin:/bin"
	if got != want {
		t.Errorf("derivePathEnv:\n got: %s\nwant: %s", got, want)
	}
}

func TestDerivePathEnv_DropsDuplicatesAndEmpty(t *testing.T) {
	got := derivePathEnv(PlatformSystemd,
		"/usr/bin::/usr/bin:/bin:/usr/bin:", "/home/test")
	want := "/usr/bin:/bin"
	if got != want {
		t.Errorf("derivePathEnv:\n got: %s\nwant: %s", got, want)
	}
}

func TestDerivePathEnv_FallsBackOnEmpty(t *testing.T) {
	got := derivePathEnv(PlatformSystemd, "", "/home/test")
	if !strings.Contains(got, "%h/.local/bin") {
		t.Errorf("empty PATH should fall back to a conservative default with %%h, got: %s", got)
	}
}

func TestInstall_InheritsEnvPath(t *testing.T) {
	home := t.TempDir()
	_, _, err := Install(Options{
		Platform: PlatformSystemd,
		Binary:   "/home/test/.local/bin/pyry",
		HomeDir:  home,
		EnvPath:  home + "/.local/bin:" + home + "/.nvm/versions/node/v24/bin:/usr/bin:/bin",
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(home, ".config/systemd/user/pyry.service"))
	got := string(body)
	want := `Environment="PATH=%h/.local/bin:%h/.nvm/versions/node/v24/bin:/usr/bin:/bin"`
	if !strings.Contains(got, want) {
		t.Errorf("inherited PATH not rewritten with %%h prefix\nwant substring: %s\n--- file ---\n%s", want, got)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"with-dash", "with-dash"},
		{"path/like:ok", "path/like:ok"},
		{"has space", "'has space'"},
		{"has'quote", `'has'\''quote'`},
		{"", "''"},
	}
	for _, tt := range tests {
		if got := shellQuote(tt.in); got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
