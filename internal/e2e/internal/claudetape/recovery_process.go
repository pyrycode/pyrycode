package claudetape

import (
	"errors"
	"io"
	"os"
)

type RecoveryProcessConfig struct {
	PoolProcessConfig
	Daemon, Socket string
}

// Only the final real-provider child is recorded. The original test-owned
// failing executable, its closed stdin and production retry/drop loop still run.
func RunRecoveryProcess(c RecoveryProcessConfig, args []string, input *os.File, output, diagnostic io.Writer) (int, error) {
	if (c.Scenario != RecoveryScenario+"-retained" && c.Scenario != RecoveryScenario+"-dropped") || (c.Mode != "capture" && c.Mode != "replay") {
		return 0, errors.New("unknown recovery process role")
	}
	cwd, err := os.Getwd()
	if err != nil || cwd != c.WorkDir || os.Getenv("HOME") != c.Home {
		return 0, errors.New("recovery child environment changed")
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
