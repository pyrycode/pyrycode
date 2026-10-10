package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pyrycode/pyrycode/internal/config"
)

func TestMemoryTranscriptCredentialOverlap(t *testing.T) {
	for _, target := range []string{"credentials", ".", "credentials/child"} {
		for _, mode := range []string{"default", "separate"} {
			t.Run(target+"/"+mode, func(t *testing.T) {
				home := testManagedHome(t)
				vault := filepath.Join(home, "vault")
				testMemoryMust(t, os.Mkdir(vault, 0700))
				s := testManagedMemory()
				if mode == "separate" {
					s.Vault = config.MemoryVault{Mode: mode, Path: vault}
				}
				testMemoryMust(t, configureMemory(context.Background(), s))
				path := filepath.Join(home, ".pyry", "config.json")
				before, err := os.ReadFile(path)
				testMemoryMust(t, err)
				memory := filepath.Join(home, ".pyry", "memory")
				testMemoryMust(t, os.MkdirAll(filepath.Join(memory, "credentials", "child"), 0700))
				testMemoryMust(t, os.Symlink(filepath.Join(memory, target), filepath.Join(memory, "recent-transcripts")))
				s.Capture.Model = "replacement"
				testMemoryReject(t, configureMemory(context.Background(), s))
				out, err := testManagedCommand(t, "status")
				testMemoryCheck(t, err != nil && out == "", "unsafe transcript status reported success")
				got, err := resolveEffectiveMemory(context.Background(), s, vault)
				testMemoryCheck(t, err != nil && reflect.DeepEqual(got, effectiveMemory{}), "credential storage entered effective roots")
				after, err := os.ReadFile(path)
				testMemoryMust(t, err)
				testMemoryCheck(t, bytes.Equal(before, after), "unsafe transcript validation changed settings")
			})
		}
	}
}

func TestMemoryRootFilesystemIdentity(t *testing.T) {
	home := testManagedHome(t)
	parent := filepath.Join(home, "notes")
	child := filepath.Join(parent, "child")
	sibling := filepath.Join(home, "notes-old")
	for _, path := range []string{child, sibling} {
		testMemoryMust(t, os.MkdirAll(path, 0700))
	}
	alias := filepath.Join(home, "alias")
	testMemoryMust(t, os.Symlink(parent, alias))
	for _, paths := range [][2]string{{alias, child}, {parent, filepath.Join(alias, "child")}, {alias, parent}} {
		testMemoryCheck(t, memoryContains(paths[0], paths[1]), "same filesystem ancestor missed")
	}
	got := normalizeMemoryRoots([]string{filepath.Join(alias, "child"), parent, alias, sibling})
	testMemoryCheck(t, reflect.DeepEqual(got, []string{parent, sibling}), "identity normalization lost sibling or duplicated ancestor")
}

func TestMemoryPathsCaseInsensitive(t *testing.T) {
	home := testManagedHome(t)
	credentials := filepath.Join(home, ".pyry", "memory", "credentials")
	testMemoryMust(t, os.MkdirAll(filepath.Join(credentials, "child"), 0700))
	variant := filepath.Join(home, ".PYRY", "MEMORY", "CREDENTIALS")
	if _, err := os.Stat(variant); os.IsNotExist(err) {
		t.Skip("case-sensitive filesystem: alternate component casing cannot resolve")
	} else {
		testMemoryMust(t, err)
	}
	canonicalHome, err := filepath.EvalSymlinks(home)
	testMemoryMust(t, err)
	vault := filepath.Join(home, "Notes")
	sibling := filepath.Join(home, "notes-old")
	for _, path := range []string{filepath.Join(vault, "Child"), sibling} {
		testMemoryMust(t, os.MkdirAll(path, 0700))
	}
	for _, path := range []string{variant, filepath.Join(variant, "CHILD"), filepath.Dir(variant)} {
		s := testManagedMemory()
		s.Vault = config.MemoryVault{Mode: "separate", Path: path}
		testMemoryReject(t, configureMemory(context.Background(), s))
		s = testManagedMemory()
		s.AdditionalRoots = []string{path}
		testMemoryReject(t, configureMemory(context.Background(), s))
		got, err := resolveEffectiveMemory(context.Background(), testManagedMemory(), path)
		testMemoryCheck(t, err != nil && reflect.DeepEqual(got, effectiveMemory{}), "wrong-case credential default vault admitted")
	}
	t.Run("execute-only-ancestor", func(t *testing.T) {
		testMemoryMust(t, os.Chmod(home, 0300))
		defer func() { testMemoryMust(t, os.Chmod(home, 0700)) }()
		if _, err := os.ReadDir(home); !os.IsPermission(err) {
			t.Skip("effective permissions do not deny ancestor listing")
		}
		got, err := resolveEffectiveMemory(context.Background(), testManagedMemory(), variant)
		testMemoryCheck(t, err != nil && reflect.DeepEqual(got, effectiveMemory{}), "unreadable case probe bypassed credential exclusion")
	})
	// Reserved leaves and existing ancestors use their on-disk spelling too.
	memory := filepath.Dir(credentials)
	testMemoryMust(t, os.Mkdir(filepath.Join(memory, "Recent-Transcripts"), 0700))
	s := testManagedMemory()
	s.Vault = config.MemoryVault{Mode: "separate", Path: filepath.Join(home, "NOTES")}
	s.AdditionalRoots = []string{filepath.Join(home, "NOTES", "CHILD"), filepath.Join(home, "notes"), vault, filepath.Join(home, "NOTES-OLD")}
	testMemoryMust(t, configureMemory(context.Background(), s))
	status, err := memoryStatus(context.Background())
	testMemoryMust(t, err)
	wantVault := filepath.Join(canonicalHome, "Notes")
	wantRoots := []string{wantVault, filepath.Join(canonicalHome, "notes-old")}
	wantTranscript := filepath.Join(canonicalHome, ".pyry", "memory", "Recent-Transcripts")
	saved, err := config.Load(filepath.Join(home, ".pyry", "config.json"))
	testMemoryMust(t, err)
	testMemoryCheck(t, saved.Memory.Vault.Path == wantVault && reflect.DeepEqual(saved.Memory.AdditionalRoots, wantRoots), "persisted paths retained caller casing")
	testMemoryCheck(t, status.Vault.Path == wantVault && reflect.DeepEqual(status.AdditionalRoots, wantRoots) && status.TranscriptPath == wantTranscript, "saved paths retained caller casing")
	got, err := resolveEffectiveMemory(context.Background(), s, vault)
	testMemoryMust(t, err)
	testMemoryCheck(t, reflect.DeepEqual(got.SearchRoots, append(wantRoots, wantTranscript)), "case variants indexed multiple times")
	testMemoryMust(t, os.Remove(filepath.Join(memory, "Recent-Transcripts")))
	testMemoryMust(t, os.Rename(memory, filepath.Join(home, ".pyry", "Memory")))
	testMemoryMust(t, os.Rename(filepath.Join(home, ".pyry"), filepath.Join(home, ".PYRY")))
	status, err = memoryStatus(context.Background())
	testMemoryMust(t, err)
	testMemoryCheck(t, status.TranscriptPath == filepath.Join(canonicalHome, ".PYRY", "Memory", "recent-transcripts"), "absent reserved leaf retained wrong ancestor casing")
}

func TestMemoryPathsCaseSensitiveSiblings(t *testing.T) {
	home := testManagedHome(t)
	home, err := filepath.EvalSymlinks(home)
	testMemoryMust(t, err)
	memory := filepath.Join(home, ".pyry", "memory")
	testMemoryMust(t, os.MkdirAll(filepath.Join(memory, "credentials"), 0700))
	upper := filepath.Join(memory, "CREDENTIALS")
	if _, err := os.Stat(upper); err == nil {
		t.Skip("case-insensitive filesystem: case-differing siblings cannot coexist")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	lower := filepath.Join(home, "notes")
	mixed := filepath.Join(home, "Notes")
	for _, path := range []string{upper, lower, mixed} {
		testMemoryMust(t, os.Mkdir(path, 0700))
	}
	s := testManagedMemory()
	s.Vault = config.MemoryVault{Mode: "separate", Path: upper}
	s.AdditionalRoots = []string{upper, lower, mixed}
	testMemoryMust(t, configureMemory(context.Background(), s))
	status, err := memoryStatus(context.Background())
	testMemoryMust(t, err)
	testMemoryCheck(t, status.Vault == s.Vault && reflect.DeepEqual(status.AdditionalRoots, s.AdditionalRoots), "distinct case-sensitive sibling rejected or collapsed")
}

func TestMemoryPathValidation(t *testing.T) {
	home := testManagedHome(t)
	vault := filepath.Join(home, "vault")
	root := filepath.Join(home, "root")
	for _, p := range []string{vault, root} {
		testMemoryMust(t, os.Mkdir(p, 0700))
	}
	link := filepath.Join(home, "link")
	testMemoryMust(t, os.Symlink(vault, link))
	s := testManagedMemory()
	s.Vault = config.MemoryVault{Mode: "separate", Path: link + "/../link"}
	s.AdditionalRoots = []string{link, vault}
	testMemoryMust(t, configureMemory(context.Background(), s))
	cfg, err := config.Load(filepath.Join(home, ".pyry", "config.json"))
	testMemoryMust(t, err)
	testMemoryCheck(t, cfg.Memory.Vault.Path == vault && reflect.DeepEqual(cfg.Memory.AdditionalRoots, []string{vault}), "canonical persisted paths")
	file := filepath.Join(home, "file")
	testMemoryMust(t, os.WriteFile(file, []byte("unchanged"), 0600))
	dangling := filepath.Join(home, "dangling")
	testMemoryMust(t, os.Symlink(filepath.Join(home, "missing"), dangling))
	loop := filepath.Join(home, "loop")
	testMemoryMust(t, os.Symlink(loop, loop))
	credentials := filepath.Join(home, ".pyry", "memory", "credentials")
	transcripts := filepath.Join(filepath.Dir(credentials), "recent-transcripts")
	for _, p := range []string{credentials, transcripts} {
		testMemoryMust(t, os.MkdirAll(filepath.Join(p, "child"), 0700))
	}
	alias := filepath.Join(home, "alias")
	testMemoryMust(t, os.Symlink(credentials, alias))
	for _, p := range []string{"relative", file, filepath.Join(home, "missing"), dangling, loop, credentials, filepath.Join(credentials, "child"), filepath.Dir(credentials), home, alias, transcripts, filepath.Join(transcripts, "child")} {
		t.Run(p, func(t *testing.T) {
			s := testManagedMemory()
			s.Vault = config.MemoryVault{Mode: "separate", Path: p}
			testMemoryReject(t, configureMemory(context.Background(), s))
			s = testManagedMemory()
			s.AdditionalRoots = []string{p}
			err := configureMemory(context.Background(), s)
			if p == transcripts || p == filepath.Join(transcripts, "child") {
				testMemoryMust(t, err)
			} else {
				testMemoryReject(t, err)
			}
		})
	}
	for _, tc := range []struct {
		name       string
		mode       os.FileMode
		additional bool
	}{
		{"vault-no-write", 0500, false}, {"vault-no-traverse", 0600, false}, {"root-no-read", 0300, true}, {"root-no-traverse", 0400, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testMemoryMust(t, os.Chmod(root, tc.mode))
			defer func() { _ = os.Chmod(root, 0700) }()
			s := testManagedMemory()
			if tc.additional {
				s.AdditionalRoots = []string{root}
			} else {
				s.Vault = config.MemoryVault{Mode: "separate", Path: root}
			}
			testMemoryReject(t, configureMemory(context.Background(), s))
		})
	}
	for _, reserved := range []string{credentials, transcripts} {
		backup := reserved + "-backup"
		testMemoryMust(t, os.Rename(reserved, backup))
		for _, target := range []string{dangling, file, loop} {
			testMemoryMust(t, os.Symlink(target, reserved))
			testMemoryReject(t, configureMemory(context.Background(), testManagedMemory()))
			testMemoryMust(t, os.Remove(reserved))
		}
		testMemoryMust(t, os.Symlink(backup, reserved))
		testMemoryMust(t, configureMemory(context.Background(), testManagedMemory()))
		testMemoryMust(t, os.Remove(reserved))
		testMemoryMust(t, os.Rename(backup, reserved))
	}
}

func TestMemoryInvalidSavedChoices(t *testing.T) {
	home := testManagedHome(t)
	for _, change := range []func(*config.MemorySettings){
		func(s *config.MemorySettings) { s.Vault.Mode = "other" }, func(s *config.MemorySettings) { s.Vault.Path = home },
		func(s *config.MemorySettings) { s.Embedding.Provider = "other" }, func(s *config.MemorySettings) { s.Embedding.Model = " \t" },
		func(s *config.MemorySettings) { s.Capture.Agent = "other" }, func(s *config.MemorySettings) { s.Capture.Model = "\n" },
		func(s *config.MemorySettings) { s.Embedding.CredentialReference = memoryOld },
	} {
		s := testManagedMemory()
		change(&s)
		testMemoryReject(t, configureMemory(context.Background(), s))
		testMemoryMust(t, config.UpdateMemory(filepath.Join(home, ".pyry", "config.json"), s))
		out, err := testManagedCommand(t, "status")
		testMemoryCheck(t, err != nil && out == "", "invalid saved settings admitted")
	}
}

func TestMemoryReservedAbsentAncestors(t *testing.T) {
	home := testManagedHome(t)
	vault := filepath.Join(home, "vault")
	testMemoryMust(t, os.Mkdir(vault, 0700))
	testMemoryMust(t, os.Mkdir(filepath.Join(home, ".pyry"), 0700))
	memory := filepath.Join(home, ".pyry", "memory")
	testMemoryMust(t, os.Symlink(vault, memory))
	s := testManagedMemory()
	s.Vault = config.MemoryVault{Mode: "separate", Path: vault}
	testMemoryReject(t, configureMemory(context.Background(), s))
	s = testManagedMemory()
	s.AdditionalRoots = []string{vault}
	testMemoryReject(t, configureMemory(context.Background(), s))
	testMemoryMust(t, os.Remove(memory))
	testMemoryMust(t, os.Mkdir(memory, 0700))
	archive := filepath.Join(home, "archive")
	transcripts := filepath.Join(archive, "transcripts")
	testMemoryMust(t, os.MkdirAll(transcripts, 0700))
	testMemoryMust(t, os.Symlink(transcripts, filepath.Join(memory, "recent-transcripts")))
	s = testManagedMemory()
	s.AdditionalRoots = []string{archive}
	got, err := resolveEffectiveMemory(context.Background(), s, vault)
	testMemoryMust(t, err)
	testMemoryCheck(t, got.TranscriptPath == transcripts && reflect.DeepEqual(got.SearchRoots, []string{vault, archive}), "transcript owner lost or subtree indexed twice")
	_, err = os.Stat(filepath.Join(memory, "credentials"))
	testMemoryCheck(t, os.IsNotExist(err), "created reserved storage")
	file := filepath.Join(home, "file")
	testMemoryMust(t, os.WriteFile(file, []byte("x"), 0600))
	_, err = memoryReservedPath(filepath.Join(file, "absent"))
	testMemoryReject(t, err)
}
