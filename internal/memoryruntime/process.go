package memoryruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type limitedOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (b *limitedOutput) Bytes() []byte  { return b.buffer.Bytes() }
func (b *limitedOutput) String() string { return b.buffer.String() }

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (64 << 10) - b.buffer.Len()
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, err := b.buffer.Write(p)
	return n, err
}
func run(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + filepath.Join(dir, "home"), "PATH=" + filepath.Join(dir, "python/bin") + ":/usr/bin:/bin", "LANG=C.UTF-8", "PYTHONDONTWRITEBYTECODE=1", "PIP_CONFIG_FILE=/dev/null", "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1"}
	configureProcess(cmd)
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	var output, stderr limitedOutput
	cmd.Stdout = &output
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("managed command failed")
	}
	if output.overflow || stderr.overflow {
		return nil, errors.New("managed command exceeded output limit")
	}
	return output.Bytes(), nil
}
func supportedHost(ctx context.Context, p lock) error {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ErrUnsupported
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			values[k] = strings.Trim(v, "\"")
		}
	}
	if values["ID"] != "ubuntu" || values["VERSION_ID"] != "24.04" {
		return ErrUnsupported
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/getconf", "GNU_LIBC_VERSION")
	cmd.Env = []string{"LANG=C"}
	var out limitedOutput
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnsupported
	}
	fields := strings.Fields(out.String())
	if out.overflow || len(fields) != 2 || fields[0] != "glibc" {
		return ErrUnsupported
	}
	parts := strings.Split(fields[1], ".")
	if len(parts) != 2 {
		return ErrUnsupported
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	if major < 2 || (major == 2 && minor < 39) {
		return ErrUnsupported
	}
	return nil
}
func provision(ctx context.Context, dir string, p lock) error {
	python := filepath.Join(dir, "python/bin/python"+strings.Join(strings.Split(p.Python, ".")[:2], "."))
	out, err := run(ctx, dir, python, "-I", "-B", "-m", "pip", "--version")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(out), "pip "+p.Bootstrap+" ") {
		return ErrProbe
	}
	bootstrap := `import os, runpy; os.umask(0o077); runpy.run_module('pip', run_name='__main__')`
	_, err = run(ctx, dir, python, "-I", "-B", "-c", bootstrap, "--isolated", "install", "--no-index", "--no-deps", "--require-hashes", "--only-binary=:all:", "--no-cache-dir", "--no-compile", "--disable-pip-version-check", "-r", filepath.Join(dir, "requirements.txt"))
	return err
}
func probe(ctx context.Context, dir string, p lock) (map[string]string, error) {
	python := filepath.Join(dir, "python/bin/python"+strings.Join(strings.Split(p.Python, ".")[:2], "."))
	script := `import importlib.metadata as m, json, platform, sys
import memsearch.cli, pymilvus, milvus_lite, openai
print(json.dumps(dict([(name,m.version(name)) for name in json.loads(sys.argv[1])]+[('python',platform.python_version())])))`
	names := []string{"pip"}
	for _, a := range p.Wheels {
		names = append(names, a.Name)
	}
	encoded, err := json.Marshal(names)
	if err != nil {
		return nil, err
	}
	out, err := run(ctx, dir, python, "-I", "-B", "-c", script, string(encoded))
	if err != nil {
		return nil, err
	}
	versions := map[string]string{}
	if err := json.Unmarshal(bytes.TrimSpace(out), &versions); err != nil {
		return nil, ErrProbe
	}
	for _, a := range p.Wheels {
		if versions[a.Name] != a.Version {
			return nil, ErrProbe
		}
	}
	if versions["python"] != p.Python || versions["pip"] != p.Bootstrap {
		return nil, ErrProbe
	}
	out, err = run(ctx, dir, python, "-I", "-B", filepath.Join(dir, "python/bin/memsearch"), "--version")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(out)) != "memsearch, version 0.4.21" {
		return nil, fmt.Errorf("CLI version: %w", ErrProbe)
	}
	if _, err := run(ctx, dir, python, "-I", "-B", "-m", "pip", "--isolated", "check"); err != nil {
		return nil, ErrProbe
	}
	return versions, nil
}
