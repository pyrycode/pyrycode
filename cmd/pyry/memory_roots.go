package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/pyrycode/pyrycode/internal/config"
	"golang.org/x/sys/unix"
)

type memoryReservedPaths struct{ transcripts, credentials string }

// effectiveMemory keeps write destinations and ownership separate from the
// normalized index union. Resolution is a snapshot; runtime users revalidate.
type effectiveMemory struct {
	VaultPath       string
	AdditionalRoots []string
	TranscriptPath  string
	SearchRoots     []string
}

func memoryDirectory(path string, access uint32) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errMemorySettings
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", errMemorySettings
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() || unix.Faccessat(unix.AT_FDCWD, real, access, unix.AT_EACCESS) != nil {
		return "", errMemorySettings
	}
	return real, nil
}

// memoryReservedPath resolves existing ancestors without mistaking dangling
// symlinks for absent directories. It never creates daemon-owned storage.
func memoryReservedPath(path string) (string, error) {
	missing := []string{}
	for {
		_, err := os.Lstat(path)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", errMemorySettings
		}
		missing = append(missing, filepath.Base(path))
		parent := filepath.Dir(path)
		if parent == path {
			return "", errMemorySettings
		}
		path = parent
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errMemorySettings
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", errMemorySettings
	}
	for i := len(missing) - 1; i >= 0; i-- {
		real = filepath.Join(real, missing[i])
	}
	return real, nil
}
func memoryReserved(home string) (memoryReservedPaths, error) {
	var paths memoryReservedPaths
	var err error
	paths.transcripts, err = memoryReservedPath(filepath.Join(home, ".pyry", "memory", "recent-transcripts"))
	if err != nil {
		return memoryReservedPaths{}, err
	}
	paths.credentials, err = memoryReservedPath(filepath.Join(home, ".pyry", "memory", "credentials"))
	if err != nil {
		return memoryReservedPaths{}, err
	}
	if memoryOverlaps(paths.transcripts, paths.credentials) {
		return memoryReservedPaths{}, errMemorySettings
	}
	return paths, nil
}
func memoryContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
func memoryOverlaps(a, b string) bool { return memoryContains(a, b) || memoryContains(b, a) }
func normalizeMemoryRoots(paths []string) []string {
	roots := []string{}
	for _, path := range paths {
		covered := false
		for _, root := range roots {
			if memoryContains(root, path) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		kept := roots[:0]
		for _, root := range roots {
			if !memoryContains(path, root) {
				kept = append(kept, root)
			}
		}
		roots = append(kept, path)
	}
	return roots
}
func validateMemoryVault(path string, reserved memoryReservedPaths) (string, error) {
	real, err := memoryDirectory(path, unix.W_OK|unix.X_OK)
	if err != nil || memoryOverlaps(real, reserved.transcripts) || memoryOverlaps(real, reserved.credentials) {
		return "", errMemorySettings
	}
	return real, nil
}
func validateMemorySettings(ctx context.Context, s config.MemorySettings, home string) (config.MemorySettings, memoryReservedPaths, error) {
	fail := func() (config.MemorySettings, memoryReservedPaths, error) {
		return config.MemorySettings{}, memoryReservedPaths{}, errMemorySettings
	}
	if strings.TrimSpace(s.Embedding.Model) == "" || strings.TrimSpace(s.Capture.Model) == "" || (s.Capture.Agent != "claude" && s.Capture.Agent != "codex") {
		return fail()
	}
	switch s.Embedding.Provider {
	case "local":
		if s.Embedding.CredentialReference != "" {
			return fail()
		}
	case "openai":
		if _, err := resolveMemoryCredential(ctx, s.Embedding.CredentialReference); err != nil {
			return config.MemorySettings{}, memoryReservedPaths{}, errMemorySelection
		}
	default:
		return fail()
	}
	reserved, err := memoryReserved(home)
	if err != nil {
		return fail()
	}
	switch s.Vault.Mode {
	case "default":
		if s.Vault.Path != "" {
			return fail()
		}
	case "separate":
		s.Vault.Path, err = validateMemoryVault(s.Vault.Path, reserved)
		if err != nil {
			return fail()
		}
	default:
		return fail()
	}
	roots := make([]string, 0, len(s.AdditionalRoots))
	for _, path := range s.AdditionalRoots {
		real, err := memoryDirectory(path, unix.R_OK|unix.X_OK)
		if err != nil || memoryOverlaps(real, reserved.credentials) {
			return fail()
		}
		roots = append(roots, real)
	}
	s.AdditionalRoots = normalizeMemoryRoots(roots)
	if len(s.AdditionalRoots) == 0 {
		s.AdditionalRoots = nil
	}
	return s, reserved, nil
}

// resolveEffectiveMemory uses the explicit base returned by
// resolveStartupWorkspaceBase, never the seeded channel or a hosted session cwd.
func resolveEffectiveMemory(ctx context.Context, s config.MemorySettings, startupWorkspaceBase string) (effectiveMemory, error) {
	home, err := memoryHome()
	if err != nil {
		return effectiveMemory{}, err
	}
	s, reserved, err := validateMemorySettings(ctx, s, home)
	if err != nil {
		return effectiveMemory{}, err
	}
	vault := s.Vault.Path
	if s.Vault.Mode == "default" {
		vault = startupWorkspaceBase
	}
	vault, err = validateMemoryVault(vault, reserved)
	if err != nil {
		return effectiveMemory{}, err
	}
	roots := append([]string{vault}, s.AdditionalRoots...)
	roots = append(roots, reserved.transcripts)
	return effectiveMemory{VaultPath: vault, AdditionalRoots: s.AdditionalRoots, TranscriptPath: reserved.transcripts, SearchRoots: normalizeMemoryRoots(roots)}, nil
}
