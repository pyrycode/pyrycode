package apps

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type runtimeVersionOutput struct {
	data     [64]byte
	n        int
	overflow bool
}

func (b *runtimeVersionOutput) Write(p []byte) (int, error) {
	n := copy(b.data[b.n:], p)
	b.n += n
	b.overflow = b.overflow || n != len(p)
	return len(p), nil
}

// CheckRuntime probes the owner's selected executable with a bounded lifetime
// and output. Call before message processing and cache the result for readiness;
// this attests infrastructure independently of registrations and app health.
func (o *Owner) CheckRuntime(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.node, "--version")
	cmd.Env = []string{}
	cmd.WaitDelay = time.Second
	var out runtimeVersionOutput
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("apps: check runtime: %w", err)
	}
	if out.overflow || !supportedNode(strings.TrimSpace(string(out.data[:out.n]))) {
		return errors.New("apps: unsupported node runtime")
	}
	return nil
}
