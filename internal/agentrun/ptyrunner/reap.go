package ptyrunner

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// killGrace is the SIGTERM → SIGKILL grace applied via exec.Cmd.WaitDelay when
// the operator cancels Run's context. It mirrors streamrunner.killGrace and
// supervisor.spawnWaitDelay (both 5s). The exact value is non-binding: the
// effective bounded-exit backstop is tui-driver Session.Close's shorter
// shutdown grace, which always fires first. WaitDelay only has to stay ≥ that
// grace so os/exec does not preempt Close's graceful path with an early
// SIGKILL of claude.
const killGrace = 5 * time.Second

// reapPSTimeout bounds the single `ps` snapshot in descendantPGIDs so a hung
// ps cannot wedge teardown.
const reapPSTimeout = 2 * time.Second

// reapDescendantGroups SIGKILLs every process group that contains a descendant
// of rootPid, except the caller's own group, rootPid's own group, and the
// init/invalid group (pgid <= 1). It is the deterministic half of the
// operator-SIGTERM teardown contract: claude isolates every Bash command into
// its own detached process group two levels below pyry and does NOT kill that
// group even when given a graceful SIGTERM, so pyry must reap it here (#565).
//
// Best-effort and content-blind: it reads pid/ppid/pgid triples only — never
// process command lines — and logs enumeration or signal failures at Warn
// (pids/pgids only) without propagating them; claude still receives its
// SIGTERM regardless. Safe to call on the os/exec watcher goroutine: it reads
// only its arguments, shells out to ps, and issues kills — no shared state.
//
// The caller wires this into cmd.Cancel, which fires only once, at the single
// teardown moment when rootPid and its whole descendant tree are guaranteed
// alive and not-yet-signalled — so the ps snapshot sees the live tree and the
// walk is race-free, not a hopeful defer-time scan.
func reapDescendantGroups(rootPid int, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), reapPSTimeout)
	defer cancel()

	pgids, err := descendantPGIDs(ctx, rootPid)
	if err != nil {
		logger.Warn("ptyrunner: descendant reap: enumerate process table failed", "root_pid", rootPid, "err", err)
		return
	}

	// The self-group and pgid<=1 guards are load-bearing: getting them wrong
	// SIGKILLs pyry's own group or init. rootPid's own group is claude's (it
	// is its session/group leader, so pgid == pid) — sess.Close owns claude's
	// teardown, so this never kills claude's own group.
	self := syscall.Getpgrp()
	var reaped []int
	for pgid := range pgids {
		if pgid <= 1 || pgid == self || pgid == rootPid {
			continue
		}
		if kerr := syscall.Kill(-pgid, syscall.SIGKILL); kerr != nil {
			if errors.Is(kerr, syscall.ESRCH) {
				continue // group already exited in the teardown window — benign
			}
			logger.Warn("ptyrunner: descendant reap: kill group failed", "pgid", pgid, "err", kerr)
			continue
		}
		reaped = append(reaped, pgid)
	}
	if len(reaped) > 0 {
		logger.Info("ptyrunner: reaped claude descendant process groups", "count", len(reaped), "pgids", reaped)
	}
}

// descendantPGIDs returns the distinct process-group ids of every transitive
// child of rootPid, discovered from a single `ps -axo pid=,ppid=,pgid=`
// snapshot (one portable enumeration across Linux + macOS — no //go:build
// split, no cgo, no new dependency). The trailing `=` on each column
// suppresses the header, so every output line is three whitespace-separated
// integers. Returns empty on enumeration failure.
func descendantPGIDs(ctx context.Context, rootPid int) (map[int]struct{}, error) {
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,pgid=").Output()
	if err != nil {
		return nil, err
	}

	children := make(map[int][]int)
	pgidOf := make(map[int]int)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		pid, perr := strconv.Atoi(fields[0])
		ppid, pperr := strconv.Atoi(fields[1])
		pgid, pgerr := strconv.Atoi(fields[2])
		if perr != nil || pperr != nil || pgerr != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
		pgidOf[pid] = pgid
	}

	// BFS from rootPid's children over the ppid→children map. seen guards
	// against a cycle that a stale snapshot with pid reuse could introduce.
	pgids := make(map[int]struct{})
	seen := make(map[int]bool)
	queue := append([]int(nil), children[rootPid]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if pgid, ok := pgidOf[pid]; ok {
			pgids[pgid] = struct{}{}
		}
		queue = append(queue, children[pid]...)
	}
	return pgids, nil
}
