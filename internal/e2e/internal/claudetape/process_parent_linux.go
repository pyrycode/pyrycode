//go:build linux

package claudetape

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func currentProcessParent(pid int) (int, error) {
	if pid <= 1 {
		return 0, os.ErrInvalid
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// The kernel command may itself contain spaces and ')'. PPID follows the
	// last closing parenthesis, after the one-character process state.
	text := string(raw)
	end := strings.LastIndex(text, ") ")
	if end < 0 || !strings.HasPrefix(text, strconv.Itoa(pid)+" (") {
		return 0, os.ErrInvalid
	}
	fields := strings.Fields(text[end+2:])
	if len(fields) < 2 || len(fields[0]) != 1 {
		return 0, os.ErrInvalid
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil || parent <= 1 {
		return 0, os.ErrInvalid
	}
	return parent, nil
}
