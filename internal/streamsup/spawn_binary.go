//go:build !e2e_realclaude

package streamsup

func (r *Runner) spawnClaudeBin() (string, error) { return r.cfg.ClaudeBin, nil }
