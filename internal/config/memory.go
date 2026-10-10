package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// MemorySettings describes managed memory. Choices, paths and credential
// references are parsed verbatim; their semantic validation belongs to callers.
type MemorySettings struct {
	Vault           MemoryVault     `json:"vault"`
	AdditionalRoots []string        `json:"additional_roots,omitempty"`
	Embedding       MemoryEmbedding `json:"embedding"`
	Capture         MemoryCapture   `json:"capture"`
}

// MemoryVault selects the default vault or a separate caller-resolved path.
type MemoryVault struct {
	Mode string `json:"mode"`
	Path string `json:"path,omitempty"`
}

// MemoryEmbedding selects embedding independently of the capture agent.
// CredentialReference is opaque non-secret metadata, never a credential token.
type MemoryEmbedding struct {
	Provider            string `json:"provider"`
	Model               string `json:"model"`
	CredentialReference string `json:"credential_reference,omitempty"`
}

// MemoryCapture selects the agent and model used for capture.
type MemoryCapture struct {
	Agent string `json:"agent"`
	Model string `json:"model"`
}

type memoryTempFile interface {
	io.WriteCloser
	Name() string
	Chmod(os.FileMode) error
	Sync() error
}

type memoryWriteOps struct {
	createTemp func(string, string) (memoryTempFile, error)
	rename     func(string, string) error
}

// UpdateMemory atomically replaces known memory settings at path, preserving
// unrelated and unknown JSON values. Empty optional path/reference and nil roots
// clear their old keys. It creates missing configs and parents privately, performs no
// semantic validation, and touches no vault or credential storage. Callers must
// serialize updates to the same config path, including updates from other processes.
func UpdateMemory(path string, settings MemorySettings) error {
	return updateMemory(path, settings, memoryWriteOps{
		createTemp: func(dir, pattern string) (memoryTempFile, error) {
			return os.CreateTemp(dir, pattern)
		},
		rename: os.Rename,
	})
}

func updateMemory(path string, settings MemorySettings, ops memoryWriteOps) (retErr error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	root := make(map[string]json.RawMessage)
	if err == nil {
		if err := json.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("config: parse %s object: %w", path, err)
		}
		// Unlike nested objects, a null document is not a config object.
		if root == nil {
			return fmt.Errorf("config: config must be an object")
		}
	}
	memory, err := memoryObject(root["memory"], "memory")
	if err != nil {
		return err
	}
	for _, part := range []struct {
		name  string
		value any
		keys  []string
	}{
		{"vault", settings.Vault, []string{"mode", "path"}},
		{"embedding", settings.Embedding, []string{"provider", "model", "credential_reference"}},
		{"capture", settings.Capture, []string{"agent", "model"}},
	} {
		old, err := memoryObject(memory[part.name], "memory."+part.name)
		if err != nil {
			return err
		}
		replacement, err := json.Marshal(part.value)
		if err != nil {
			return fmt.Errorf("config: encode memory.%s: %w", part.name, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(replacement, &fields); err != nil {
			return fmt.Errorf("config: parse replacement %s: %w", part.name, err)
		}
		for _, key := range part.keys {
			delete(old, key)
		}
		for key, value := range fields {
			old[key] = value
		}
		memory[part.name], err = json.Marshal(old)
		if err != nil {
			return fmt.Errorf("config: encode memory.%s: %w", part.name, err)
		}
	}
	delete(memory, "additional_roots")
	if settings.AdditionalRoots != nil {
		memory["additional_roots"], err = json.Marshal(settings.AdditionalRoots)
		if err != nil {
			return fmt.Errorf("config: encode additional roots: %w", err)
		}
	}
	root["memory"], err = json.Marshal(memory)
	if err != nil {
		return fmt.Errorf("config: encode memory: %w", err)
	}
	data, err = json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}
	f, err := ops.createTemp(dir, ".memory-*.tmp")
	if err != nil {
		return fmt.Errorf("config: create temp: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			if err := f.Close(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("config: cleanup close: %w", err))
			}
		}
		if err := os.Remove(f.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, fmt.Errorf("config: remove temp: %w", err))
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("config: chmod temp: %w", err)
	}
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("config: write temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("config: sync temp: %w", err)
	}
	closed = true // Close releases the handle even when it reports an error.
	if err := f.Close(); err != nil {
		return fmt.Errorf("config: close temp: %w", err)
	}
	if err := ops.rename(f.Name(), path); err != nil {
		return fmt.Errorf("config: rename: %w", err)
	}
	return nil
}

func memoryObject(data []byte, location string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if len(data) != 0 {
		if err := json.Unmarshal(data, &object); err != nil {
			return nil, fmt.Errorf("config: parse %s object: %w", location, err)
		}
	}
	if object == nil {
		object = make(map[string]json.RawMessage)
	}
	return object, nil
}
