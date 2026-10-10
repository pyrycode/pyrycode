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
