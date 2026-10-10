package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pyrycode/pyrycode/internal/config"
)

var errMemorySettings = errors.New("memory: invalid settings or unavailable directories")

// memoryConfigurationStatus deliberately excludes credential references and tokens.
type memoryConfigurationStatus struct {
	Configured      bool                  `json:"configured"`
	Vault           config.MemoryVault    `json:"vault"`
	AdditionalRoots []string              `json:"additional_roots"`
	Embedding       memoryEmbeddingStatus `json:"embedding"`
	Capture         config.MemoryCapture  `json:"capture"`
	TranscriptPath  string                `json:"transcript_path"`
}
type memoryEmbeddingStatus struct {
	Provider             string `json:"provider"`
	Model                string `json:"model"`
	CredentialConfigured bool   `json:"credential_configured"`
}

func memoryHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", errors.New("memory: home unavailable")
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		return "", errors.New("memory: home unavailable")
	}
	return filepath.Clean(home), nil
}

// configureMemory replaces all known settings. Callers serialize config writes.
func configureMemory(ctx context.Context, settings config.MemorySettings) error {
	home, err := memoryHome()
	if err != nil {
		return err
	}
	settings, _, err = validateMemorySettings(ctx, settings, home)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := config.UpdateMemory(filepath.Join(home, ".pyry", "config.json"), settings); err != nil {
		return errors.New("memory: configuration save failed")
	}
	return nil
}

// memoryStatus reports saved choices, without resolving the default vault or
// asserting runtime readiness. Credentials are freshly validated on every call.
func memoryStatus(ctx context.Context) (memoryConfigurationStatus, error) {
	var status memoryConfigurationStatus
	home, err := memoryHome()
	if err != nil {
		return status, err
	}
	data, err := os.ReadFile(filepath.Join(home, ".pyry", "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, errors.New("memory: configuration unavailable")
	}
	var object map[string]json.RawMessage
	var cfg config.Config
	if json.Unmarshal(data, &object) != nil || object == nil || json.Unmarshal(data, &cfg) != nil {
		return status, errors.New("memory: invalid configuration")
	}
	if cfg.Memory == nil {
		return status, nil
	}
	_, reserved, err := validateMemorySettings(ctx, *cfg.Memory, home)
	if err != nil {
		return status, err
	}
	roots := cfg.Memory.AdditionalRoots
	if roots == nil {
		roots = []string{}
	}
	return memoryConfigurationStatus{
		Configured: true, Vault: cfg.Memory.Vault, AdditionalRoots: roots,
		Embedding: memoryEmbeddingStatus{cfg.Memory.Embedding.Provider, cfg.Memory.Embedding.Model, cfg.Memory.Embedding.Provider == "openai"},
		Capture:   cfg.Memory.Capture, TranscriptPath: reserved.transcripts,
	}, nil
}

func parseMemoryConfiguration(args []string) (config.MemorySettings, error) {
	s := config.MemorySettings{}
	values := make(map[string]string)
	for i := 0; i < len(args); i++ {
		key, value, inline := strings.Cut(args[i], "=")
		switch key {
		case "--vault", "--embedding-provider", "--embedding-model", "--capture-agent", "--capture-model", "--knowledge-folder", "--credential-reference":
		default:
			return s, errors.New("memory configure: unsupported argument")
		}
		if !inline {
			i++
			if i >= len(args) || strings.HasPrefix(args[i], "--") {
				return s, errors.New("memory configure: missing flag value")
			}
			value = args[i]
		}
		if key == "--knowledge-folder" {
			s.AdditionalRoots = append(s.AdditionalRoots, value)
			continue
		}
		if _, seen := values[key]; seen {
			return s, errors.New("memory configure: repeated flag")
		}
		values[key] = value
	}
	for _, key := range []string{"--vault", "--embedding-provider", "--embedding-model", "--capture-agent", "--capture-model"} {
		if strings.TrimSpace(values[key]) == "" {
			return s, errors.New("memory configure: all five main choices are required")
		}
	}
	s.Vault = config.MemoryVault{Mode: "separate", Path: values["--vault"]}
	if values["--vault"] == "default" {
		s.Vault = config.MemoryVault{Mode: "default"}
	}
	s.Embedding = config.MemoryEmbedding{Provider: values["--embedding-provider"], Model: values["--embedding-model"], CredentialReference: values["--credential-reference"]}
	if s.Embedding.Provider == "local" {
		if _, supplied := values["--credential-reference"]; supplied {
			return s, errors.New("memory configure: local embeddings reject credential references")
		}
	}
	s.Capture = config.MemoryCapture{Agent: values["--capture-agent"], Model: values["--capture-model"]}
	return s, nil
}

func runMemoryConfiguration(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("memory: expected configure, status or credential")
	}
	switch args[0] {
	case "configure":
		settings, err := parseMemoryConfiguration(args[1:])
		if err != nil {
			return err
		}
		if err := configureMemory(ctx, settings); err != nil {
			return err
		}
	case "status":
		if len(args) != 1 {
			return errors.New("memory status: takes no arguments")
		}
		status, err := memoryStatus(ctx)
		if err != nil {
			return err
		}
		if status.Configured {
			return json.NewEncoder(out).Encode(status)
		}
		return json.NewEncoder(out).Encode(struct {
			Configured bool `json:"configured"`
		}{false})
	default:
		return errors.New("memory: expected configure, status or credential")
	}
	return json.NewEncoder(out).Encode(struct {
		Configured bool `json:"configured"`
	}{true})
}
