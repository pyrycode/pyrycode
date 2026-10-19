package claudetape

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DaemonBinding describes one fresh child in a named daemon process role.
// The outer suite owns role membership; this binding rejects resume/rotation.
// Session identity is substituted only in its exact argv flag and top-level
// native session_id field; user/tool text and nested objects stay literal.
type DaemonBinding struct {
	session, home, workdir, daemon, socket string
	filePaths                              map[string]string
	args                                   []string
}

const daemonSession = "${CHILD_SESSION}"

func NewDaemonBinding(args []string, home, workdir, daemon, socket string) (*DaemonBinding, error) {
	fail := errors.New("invalid fresh daemon child binding")
	if !filepath.IsAbs(home) || !filepath.IsAbs(workdir) || !filepath.IsAbs(daemon) || !filepath.IsAbs(socket) {
		return nil, fail
	}
	rel, err := filepath.Rel(home, workdir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fail
	}
	p := &DaemonBinding{home: home, workdir: workdir, daemon: daemon, socket: socket, filePaths: map[string]string{}, args: append([]string(nil), args...)}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--resume", "--continue":
			return nil, fail
		case "--session-id":
			if p.session != "" || i+1 == len(args) || !poolUUID.MatchString(args[i+1]) {
				return nil, fail
			}
			p.session = args[i+1]
			p.args[i+1] = daemonSession
			i++
		case "--input-format", "--output-format", "--model", "--effort", "--permission-mode", "--permission-prompt-tool":
			if i+1 == len(args) || args[i+1] == "" {
				return nil, fail
			}
			if (args[i] == "--input-format" || args[i] == "--output-format") && args[i+1] != "stream-json" {
				return nil, fail
			}
			i++
		case "--verbose", "--include-partial-messages", "--forward-subagent-text", "--replay-user-messages", "--dangerously-skip-permissions", "--prompt-suggestions", "--strict-mcp-config":
		case "--settings", "--append-system-prompt-file", "--mcp-config":
			flag := args[i]
			if i+1 == len(args) || !filepath.IsAbs(args[i+1]) || p.filePaths[flag] != "" {
				return nil, fail
			}
			p.filePaths[flag] = args[i+1]
			p.args[i+1] = "${CHILD_FILE:" + flag + "}"
			i++
		default:
			return nil, fail
		}
	}
	// These roots always compose settings and a daemon system prompt. MCP
	// configuration is optional: their original launch bypasses approvals and
	// supplies no MCP flag. Every supplied MCP file is still inventoried.
	if p.session == "" || p.filePaths["--settings"] == "" || p.filePaths["--append-system-prompt-file"] == "" {
		return nil, fail
	}
	return p, nil
}
func (p *DaemonBinding) Args() []string { return append([]string(nil), p.args...) }

func (p *DaemonBinding) Frame(raw []byte, restore bool) ([]byte, error) {
	v, err := object(raw)
	if err != nil {
		return nil, err
	}
	if id, exists := v["session_id"]; exists {
		want, got := any(p.session), daemonSession
		if restore {
			want, got = daemonSession, p.session
		}
		if id != want {
			return nil, errors.New("daemon child native session identity changed")
		}
		v["session_id"] = got
	}
	// Only exact native cwd metadata has a path binding. There is no recursive
	// rewriting of prose, user requests, tool payloads or nested session fields.
	if v["type"] == "system" && v["subtype"] == "init" {
		if cwd, exists := v["cwd"]; exists {
			want, got := any(p.workdir), "${CHILD_CWD}"
			if restore {
				want, got = "${CHILD_CWD}", p.workdir
			}
			if cwd != want {
				return nil, errors.New("daemon child native cwd changed")
			}
			v["cwd"] = got
		}
	}
	return json.Marshal(p.paths(v, restore))
}

// Files captures every supplied known input, file mode and the controlled cwd.
// Plain system prompts are encoded as JSON strings; all other inputs must be
// complete JSON. Unknown private paths are rejected rather than scrubbed.
func (p *DaemonBinding) Files() (map[string]json.RawMessage, error) {
	files := map[string]json.RawMessage{}
	rel, err := filepath.Rel(p.home, p.workdir)
	if err != nil {
		return nil, err
	}
	files["$cwd"], err = json.Marshal(rel)
	if err != nil {
		return nil, err
	}
	for flag, path := range p.filePaths {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > MaxBytes {
			return nil, errors.New("invalid daemon input file")
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, errors.New("missing daemon input file")
		}
		opened, statErr := f.Stat()
		raw, readErr := io.ReadAll(io.LimitReader(f, MaxBytes+1))
		closeErr := f.Close()
		if statErr != nil || !os.SameFile(info, opened) || readErr != nil || closeErr != nil || len(raw) > MaxBytes {
			return nil, errors.New("daemon input file read failed")
		}
		var content any
		if flag == "--append-system-prompt-file" {
			content = string(raw)
		} else {
			d := json.NewDecoder(bytes.NewReader(raw))
			d.UseNumber()
			if d.Decode(&content) != nil || d.Decode(new(any)) != io.EOF {
				return nil, errors.New("invalid daemon input file JSON")
			}
		}
		encoded, err := json.Marshal(struct {
			Content any    `json:"content"`
			Mode    uint32 `json:"mode"`
		}{content, uint32(info.Mode().Perm())})
		if err != nil {
			return nil, err
		}
		encoded, err = p.normalizePaths(encoded, false)
		if err != nil {
			return nil, err
		}
		if err := Safe(encoded); err != nil {
			return nil, err
		}
		files[flag] = encoded
	}
	return files, nil
}

// paths binds only declared filesystem roots. Session identity is handled above
// solely at the native top-level field and exact argv slot, never in prose.
func (p *DaemonBinding) paths(value any, restore bool) any {
	switch v := value.(type) {
	case string:
		for _, b := range []struct{ actual, label string }{{p.daemon, "${CHILD_DAEMON}"}, {p.socket, "${CHILD_SOCKET}"}, {p.home, "${CHILD_HOME}"}} {
			if restore {
				v = strings.ReplaceAll(v, b.label, b.actual)
			} else {
				v = strings.ReplaceAll(v, b.actual, b.label)
			}
		}
		return v
	case map[string]any:
		for k, x := range v {
			v[k] = p.paths(x, restore)
		}
	case []any:
		for i, x := range v {
			v[i] = p.paths(x, restore)
		}
	}
	return value
}
func (p *DaemonBinding) normalizePaths(raw []byte, restore bool) ([]byte, error) {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&v) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid daemon path input")
	}
	return json.Marshal(p.paths(v, restore))
}
