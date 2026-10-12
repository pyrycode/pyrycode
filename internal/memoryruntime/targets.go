package memoryruntime

import (
	"context"
	_ "embed"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
)

//go:embed macos-arm64.lock.json
var macOSLockData []byte

func selectPins(goos, arch, mode string) (lock, error) {
	var p lock
	if mode != "openai" && mode != "local" {
		return p, ErrUnsupported
	}
	var data []byte
	switch goos + "/" + arch {
	case "linux/amd64":
		data = lockData
	case "darwin/arm64":
		data = macOSLockData
	default:
		return p, ErrUnsupported
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	if mode == "local" {
		var extra struct {
			Wheels []artifact `json:"local_wheels"`
		}
		if err := json.Unmarshal(data, &extra); err != nil {
			return p, err
		}
		p.Wheels = append(p.Wheels, extra.Wheels...)
		p.Local = true
	}
	return p, nil
}

func supportedMacOS(ctx context.Context, minimum string) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/sw_vers", "-productVersion")
	cmd.Env = []string{"LANG=C"}
	var out limitedOutput
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnsupported
	}
	if out.overflow || !macOSAtLeast(strings.TrimSpace(out.String()), minimum) {
		return ErrUnsupported
	}
	return nil
}

func macOSAtLeast(version, minimum string) bool {
	parse := func(s string) ([3]int, bool) {
		var v [3]int
		parts := strings.Split(s, ".")
		if len(parts) < 2 || len(parts) > len(v) {
			return v, false
		}
		for i, part := range parts {
			if part == "" || strings.Trim(part, "0123456789") != "" {
				return v, false
			}
			n, err := strconv.Atoi(part)
			if err != nil {
				return v, false
			}
			v[i] = n
		}
		return v, true
	}
	v, ok := parse(version)
	m, valid := parse(minimum)
	if !ok || !valid {
		return false
	}
	for i := range v {
		if v[i] != m[i] {
			return v[i] > m[i]
		}
	}
	return true
}
