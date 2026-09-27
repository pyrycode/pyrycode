// Package memorysearch reports search access for one selected agent and workspace.
package memorysearch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/pyrycode/pyrycode/internal/config"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// Availability describes effective search access, not saved knowledge.
type Availability string

const (
	Available   Availability = "available"
	Unavailable Availability = "unavailable"
	Unknown     Availability = "unknown"
	Absent      Availability = "absent"
)

type Provider struct {
	ID           string
	DisplayName  string
	Installed    bool
	Enabled      bool
	Availability Availability
}

type Result struct {
	Availability Availability
	Providers    []Provider
}

// Plugin is one entry from the selected launch's effective agent plugin inventory.
type Plugin struct {
	ID      string
	Enabled bool
}

// LaunchEvidence must describe the selected child, not the operator's host setup.
// A nil ChildPATH or MCPStatus means that check could not be completed.
type LaunchEvidence struct {
	Agent              string
	Workspace          string
	CodexHome          string
	Plugins            []Plugin
	PluginsKnown       bool
	ChildPATH          *string
	HostCLIs           []string
	HostCLIsKnown      bool
	MCPStatus          *turnevent.MCPStatus
	MCPStatusEffective bool
}

type Input struct {
	Agent     string
	Workspace string
	CodexHome string
	Config    config.Config
	ConfigErr error
	Launch    LaunchEvidence
}

type providerState struct {
	Provider
}

const maxEvidenceEntries = 128

var builtins = map[string]string{
	"memsearch":         "Memsearch",
	"qmd":               "QMD",
	"smart-connections": "Smart Connections",
}

// Detect classifies a bounded, read-only snapshot. It never launches a search
// program or consults the daemon's PATH or an operator's personal agent home.
func Detect(in Input) (Result, error) {
	if in.Agent != "claude" && in.Agent != "codex" {
		return Result{}, fmt.Errorf("memory search: unsupported agent")
	}
	if !canonicalPath(in.Workspace) {
		return Result{}, fmt.Errorf("memory search: workspace must be canonical and absolute")
	}

	states := make(map[string]*providerState)
	unresolved := in.ConfigErr != nil
	if in.ConfigErr == nil {
		if len(in.Config.MemorySearchProviders) > maxEvidenceEntries {
			unresolved = true
		}
		for _, declaration := range in.Config.MemorySearchProviders[:min(len(in.Config.MemorySearchProviders), maxEvidenceEntries)] {
			if declaration.Agent != in.Agent {
				continue
			}
			if !canonicalPath(declaration.Workspace) {
				unresolved = true
				continue
			}
			if declaration.Workspace != in.Workspace {
				continue
			}
			if !validID(declaration.ID) || !validDisplayName(declaration.DisplayName) || declaration.Enabled == nil {
				unresolved = true
				continue
			}
			availability := Unavailable
			if *declaration.Enabled {
				availability = Available
			}
			mark(states, declaration.ID, declaration.DisplayName, *declaration.Enabled, availability)
		}
	}

	scoped := in.Launch.Agent == in.Agent && in.Launch.Workspace == in.Workspace
	if in.Agent == "codex" {
		scoped = scoped && canonicalPath(in.CodexHome) && in.Launch.CodexHome == in.CodexHome
	}
	if !scoped {
		unresolved = true
	} else {
		if !in.Launch.PluginsKnown {
			unresolved = true
		} else {
			if len(in.Launch.Plugins) > maxEvidenceEntries {
				unresolved = true
			}
			for _, plugin := range in.Launch.Plugins[:min(len(in.Launch.Plugins), maxEvidenceEntries)] {
				if plugin.ID == "memsearch" || strings.HasPrefix(plugin.ID, "memsearch@") {
					availability := Unavailable
					if plugin.Enabled {
						availability = Available
					}
					mark(states, "memsearch", builtins["memsearch"], plugin.Enabled, availability)
				}
			}
		}

		if in.Launch.ChildPATH == nil {
			unresolved = true
		} else {
			for _, cli := range []string{"memsearch", "qmd"} {
				found, unknown := executableOnPath(*in.Launch.ChildPATH, cli)
				if unknown {
					unresolved = true
				}
				if found {
					mark(states, cli, builtins[cli], true, Available)
				}
			}
		}
		if !in.Launch.HostCLIsKnown || len(in.Launch.HostCLIs) > maxEvidenceEntries {
			unresolved = true
		}
		for _, cli := range in.Launch.HostCLIs[:min(len(in.Launch.HostCLIs), maxEvidenceEntries)] {
			if cli == "memsearch" || cli == "qmd" {
				mark(states, cli, builtins[cli], false, Unknown)
			}
		}

		if in.Launch.MCPStatus == nil || !in.Launch.MCPStatusEffective {
			unresolved = true
		} else {
			status := in.Launch.MCPStatus
			if status.DroppedServers > 0 || len(status.Servers) > maxEvidenceEntries {
				unresolved = true
			}
			for _, server := range status.Servers[:min(len(status.Servers), maxEvidenceEntries)] {
				id := mcpProvider(server.Name)
				if id == "" || (in.Agent == "claude" && (server.Scope == "user" || server.Scope == "project")) {
					continue
				}
				switch server.Status {
				case "connected":
					mark(states, id, builtins[id], true, Available)
				case "failed", "disconnected":
					mark(states, id, builtins[id], true, Unavailable)
				case "disabled":
					mark(states, id, builtins[id], false, Unavailable)
				default:
					mark(states, id, builtins[id], true, Unknown)
				}
			}
		}
	}

	result := Result{Providers: make([]Provider, 0, len(states))}
	anyAvailable, anyUnknown := false, unresolved
	for _, state := range states {
		result.Providers = append(result.Providers, state.Provider)
		anyAvailable = anyAvailable || state.Availability == Available
		anyUnknown = anyUnknown || state.Availability == Unknown
	}
	sort.Slice(result.Providers, func(i, j int) bool { return result.Providers[i].ID < result.Providers[j].ID })
	switch {
	case anyAvailable:
		result.Availability = Available
	case anyUnknown:
		result.Availability = Unknown
	case len(result.Providers) > 0:
		result.Availability = Unavailable
	default:
		result.Availability = Absent
	}
	return result, nil
}

func mark(states map[string]*providerState, id, name string, enabled bool, availability Availability) {
	if state, ok := states[id]; ok {
		state.Enabled = state.Enabled || enabled
		if rank(availability) > rank(state.Availability) {
			state.Availability = availability
		}
		return
	}
	states[id] = &providerState{Provider: Provider{ID: id, DisplayName: name, Installed: true, Enabled: enabled, Availability: availability}}
}

func rank(a Availability) int {
	switch a {
	case Available:
		return 3
	case Unknown:
		return 2
	default:
		return 1
	}
}

func mcpProvider(name string) string {
	switch name {
	case "qmd", "qmd-mcp":
		return "qmd"
	case "smart-connections", "smart-connections-search":
		return "smart-connections"
	default:
		return ""
	}
}

func executableOnPath(pathValue, name string) (bool, bool) {
	if pathValue == "" {
		return false, false
	}
	dirs := strings.Split(pathValue, string(os.PathListSeparator))
	unknown := len(dirs) > maxEvidenceEntries
	for _, dir := range dirs[:min(len(dirs), maxEvidenceEntries)] {
		if dir == "" || !filepath.IsAbs(dir) {
			unknown = true
			continue
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil {
			if info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
				return true, false
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			unknown = true
		}
	}
	return false, unknown
}

func canonicalPath(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && resolved == path
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 64 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, c := range id[1:] {
		if c < 'a' || c > 'z' {
			if c < '0' || c > '9' {
				if c != '-' && c != '_' && c != '.' {
					return false
				}
			}
		}
	}
	return true
}

func validDisplayName(name string) bool {
	if strings.TrimSpace(name) == "" || len(name) > 128 {
		return false
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
