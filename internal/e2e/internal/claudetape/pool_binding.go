package claudetape

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PoolBinding describes one fresh direct-runner child. It deliberately has no
// resume, session rotation, multiple launch or arbitrary identifier rebinding.
// Session identity is substituted only in its exact argv flag and top-level
// native session_id field; user/tool text and nested objects stay literal.
type PoolBinding struct {
	session, home, workdir string
	filePaths              map[string]string
	args                   []string
}

const poolSession = "${POOL_SESSION}"

var poolUUID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func NewPoolBinding(args []string, home, workdir string) (*PoolBinding, error) {
	fail := errors.New("invalid fresh pool child binding")
	if !filepath.IsAbs(home) || !filepath.IsAbs(workdir) {
		return nil, fail
	}
	rel, err := filepath.Rel(home, workdir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fail
	}
	p := &PoolBinding{home: home, workdir: workdir, filePaths: map[string]string{}, args: append([]string(nil), args...)}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--resume", "--continue":
			return nil, fail
		case "--session-id":
			if p.session != "" || i+1 == len(args) || !poolUUID.MatchString(args[i+1]) {
				return nil, fail
			}
			p.session = args[i+1]
			p.args[i+1] = poolSession
			i++
		case "--input-format", "--output-format", "--model", "--effort", "--permission-mode":
			if i+1 == len(args) || args[i+1] == "" {
				return nil, fail
			}
			if (args[i] == "--input-format" || args[i] == "--output-format") && args[i+1] != "stream-json" {
				return nil, fail
			}
			i++
		case "--verbose", "--include-partial-messages", "--forward-subagent-text", "--replay-user-messages", "--dangerously-skip-permissions", "--prompt-suggestions":
		case "--settings", "--append-system-prompt-file", "--mcp-config":
			flag := args[i]
			if i+1 == len(args) || !filepath.IsAbs(args[i+1]) || p.filePaths[flag] != "" {
				return nil, fail
			}
			p.filePaths[flag] = args[i+1]
			p.args[i+1] = "${POOL_FILE:" + flag + "}"
			i++
		default:
			return nil, fail
		}
	}
	if p.session == "" {
		return nil, fail
	}
	return p, nil
}
func (p *PoolBinding) Args() []string { return append([]string(nil), p.args...) }

func (p *PoolBinding) Frame(raw []byte, restore bool) ([]byte, error) {
	v, err := object(raw)
	if err != nil {
		return nil, err
	}
	if id, exists := v["session_id"]; exists {
		want, got := any(p.session), poolSession
		if restore {
			want, got = poolSession, p.session
		}
		if id != want {
			return nil, errors.New("pool child native session identity changed")
		}
		v["session_id"] = got
	}
	// Only exact native cwd metadata has a path binding. There is no recursive
	// rewriting of prose, user requests, tool payloads or nested session fields.
	if v["type"] == "system" && v["subtype"] == "init" {
		if cwd, exists := v["cwd"]; exists {
			want, got := any(p.workdir), "${POOL_CWD}"
			if restore {
				want, got = "${POOL_CWD}", p.workdir
			}
			if cwd != want {
				return nil, errors.New("pool child native cwd changed")
			}
			v["cwd"] = got
		}
	}
	return json.Marshal(v)
}

// Files captures every supplied known input, file mode and the controlled cwd.
// Plain system prompts are encoded as JSON strings; all other inputs must be
// complete JSON. Unknown private paths are rejected rather than scrubbed.
func (p *PoolBinding) Files() (map[string]json.RawMessage, error) {
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
			return nil, errors.New("invalid pool input file")
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, errors.New("missing pool input file")
		}
		opened, statErr := f.Stat()
		raw, readErr := io.ReadAll(io.LimitReader(f, MaxBytes+1))
		closeErr := f.Close()
		if statErr != nil || !os.SameFile(info, opened) || readErr != nil || closeErr != nil || len(raw) > MaxBytes {
			return nil, errors.New("pool input file read failed")
		}
		var content any
		if flag == "--append-system-prompt-file" {
			content = string(raw)
		} else {
			d := json.NewDecoder(bytes.NewReader(raw))
			d.UseNumber()
			if d.Decode(&content) != nil || d.Decode(new(any)) != io.EOF {
				return nil, errors.New("invalid pool input file JSON")
			}
		}
		encoded, err := json.Marshal(struct {
			Content any    `json:"content"`
			Mode    uint32 `json:"mode"`
		}{content, uint32(info.Mode().Perm())})
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
