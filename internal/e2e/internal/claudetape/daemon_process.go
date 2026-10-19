package claudetape

import (
	"errors"
	"io"
	"os"
)

type DaemonProcessConfig struct {
	PoolProcessConfig
	Daemon, Socket string
}

// RunDaemonProcess retains one independently supervised role's strict pipes.
// The outer owner must prove the complete role inventory and seal before cleanup.
func RunDaemonProcess(c DaemonProcessConfig, args []string, input *os.File, output, diagnostic io.Writer) (int, error) {
	permitted := false
	for _, profile := range []string{"conversation-default", "conversation-workspace", "conversation-turn-state", "conversation-prompt-argv"} {
		for _, role := range []string{"bootstrap", "created-1"} {
			if c.Scenario == profile+"-"+role {
				permitted = true
			}
		}
	}
	if !permitted || (c.Mode != "capture" && c.Mode != "replay") {
		return 0, errors.New("unsupported daemon child role")
	}
	cwd, err := os.Getwd()
	if err != nil || cwd != c.WorkDir || os.Getenv("HOME") != c.Home {
		return 0, errors.New("daemon child environment changed")
	}
	b, err := NewDaemonBinding(args, c.Home, c.WorkDir, c.Daemon, c.Socket)
	if err != nil {
		return 0, err
	}
	files, err := b.Files()
	if err != nil {
		return 0, err
	}
	return runBoundProcess(c.PoolProcessConfig, args, b, files, input, output, diagnostic)
}
