package e2emcp

import (
	"context"
	"io"
	"os"
	"sort"
	"sync"
	"time"
)

// ReplayServers owns real current MCP processes and replay-owned client pipes.
// It does not reproduce the native SDK's spawning, process groups or reaping.
// The caller must validate the complete primary/RPC causal join and declare
// the exact current source configuration before constructing these processes.
type ReplayServers struct {
	logs              *ServerLogs
	ctx               context.Context
	cancel            context.CancelFunc
	coordinator       *Coordinator
	inputs            []*os.File
	files             []*os.File
	done              chan struct{}
	mu                sync.Mutex
	proofs            []ServerProof
	failed, completed bool
}

// StartReplayServers only launches the two closed command kinds, using the
// current binary and explicit current environment. No executable, arguments,
// environment or process IDs are taken from the recording. declaredKinds must
// equal the validated trace's connection set; this checks cardinality rather
// than treating a fixture connection name as launch authorization.
func StartReplayServers(ctx context.Context, b LaunchBinding, c *Coordinator, declaredKinds []string, env []string, validateStderr func(string, []byte) error) (*ReplayServers, error) {
	if ctx == nil || ctx.Err() != nil || c == nil || validateStderr == nil || b.validate() != nil {
		return nil, protocolError()
	}
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.After(time.Now()) || time.Until(deadline) > 20*time.Minute {
		return nil, protocolError()
	}
	actualEnv, err := b.serverEnvironment(env)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	valid := c.replay != nil && !c.failed && !c.closed && !c.checkpointed && len(c.sessions) == 0 && len(declaredKinds) == len(c.replay.connections)
	requests := map[string][]string{}
	if valid {
		for _, kind := range declaredKinds {
			if (kind != "mcp-files" && kind != "mcp-approve") || !c.replay.connections[kind] || requests[kind] != nil {
				valid = false
				break
			}
			requests[kind] = []string{}
		}
		for _, event := range c.replay.events {
			if event.Direction == ClientToServer {
				requests[event.Connection] = append(requests[event.Connection], event.Raw)
			}
		}
	}
	c.mu.Unlock()
	if !valid {
		return nil, protocolError()
	}
	owned, cancel := context.WithCancel(ctx)
	s := &ReplayServers{logs: newServerLogs(declaredKinds), ctx: owned, cancel: cancel, coordinator: c, done: make(chan struct{})}
	// Allocate all pipes and registrations before starting any child. A rejected
	// declaration cannot leave an unjoined partially started set of processes.
	type client struct {
		kind                   string
		pipes                  [3]*os.File
		writer, output, stderr *os.File
	}
	var clients []client
	cleanup := func() {
		cancel()
		for _, f := range s.files {
			f.Close()
		}
		c.Abort()
	}
	for _, kind := range declaredKinds {
		inR, inW, e := os.Pipe()
		if e != nil {
			cleanup()
			return nil, protocolError()
		}
		s.files = append(s.files, inR, inW)
		outR, outW, e := os.Pipe()
		if e != nil {
			cleanup()
			return nil, protocolError()
		}
		s.files = append(s.files, outR, outW)
		errR, errW, e := os.Pipe()
		if e != nil {
			cleanup()
			return nil, protocolError()
		}
		s.files = append(s.files, errR, errW)
		if c.Register(kind) != nil {
			cleanup()
			return nil, protocolError()
		}
		s.inputs = append(s.inputs, inW)
		clients = append(clients, client{kind, [3]*os.File{inR, outW, errW}, inW, outR, errR})
	}
	var wg sync.WaitGroup
	fail := func() {
		// Close actual owned pipe writers/readers before waiting for a coordinator
		// mutex potentially held by a blocked real forward.
		cancel()
		for _, f := range s.files {
			f.Close()
		}
		s.mu.Lock()
		s.failed = true
		s.mu.Unlock()
		c.Abort()
	}
	stopWatch := context.AfterFunc(owned, fail)
	for _, client := range clients {
		wg.Add(4)
		go func() {
			defer wg.Done()
			for _, raw := range requests[client.kind] {
				if WriteLine(client.writer, []byte(raw)) != nil {
					fail()
					return
				}
			}
			// Keep client stdin open through Checkpoint and genuine main termination.
		}()
		go func() {
			defer wg.Done()
			if _, e := io.Copy(io.Discard, client.output); e != nil {
				fail()
			}
		}()
		go func() {
			defer wg.Done()
			if _, e := io.Copy(io.Discard, client.stderr); e != nil {
				fail()
			}
		}()
		go func() {
			defer wg.Done()
			proof, e := runOwnedServer(owned, b, client.kind, actualEnv, client.pipes, c, func(raw []byte) error { return validateStderr(client.kind, raw) }, s.logs)
			// Only this owner has Wait'ed the actual server. NativeProxyPID remains 0.
			client.pipes[1].Close()
			client.pipes[2].Close()
			if e != nil {
				fail()
				return
			}
			s.mu.Lock()
			s.proofs = append(s.proofs, proof)
			s.mu.Unlock()
		}()
	}
	go func() { wg.Wait(); stopWatch(); close(s.done) }()
	return s, nil
}

// CompleteAndJoin must be called only after successful primary termination.
// A previously verified live prefix is necessary, but does not itself prove
// primary termination. Closing these replay-owned stdin pipes then obtains
// genuine server EOF and actual Wait status; it cannot supply native SDK facts.
func (s *ReplayServers) CompleteAndJoin(budget time.Duration) ([]ServerProof, error) {
	s.coordinator.mu.Lock()
	valid := s.coordinator.checkpointed && !s.coordinator.failed && !s.coordinator.closed
	s.coordinator.mu.Unlock()
	s.mu.Lock()
	valid = valid && !s.completed && !s.failed && s.ctx.Err() == nil && budget > 0
	if valid {
		s.completed = true
	}
	s.mu.Unlock()
	if !valid {
		s.Abort()
		return nil, protocolError()
	}
	for _, in := range s.inputs {
		in.Close()
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-s.done:
	case <-timer.C:
		s.Abort()
		return nil, protocolError()
	case <-s.ctx.Done():
		s.Abort()
		return nil, protocolError()
	}
	s.mu.Lock()
	failed := s.failed || s.ctx.Err() != nil || len(s.proofs) != len(s.inputs)
	proofs := append([]ServerProof(nil), s.proofs...)
	s.mu.Unlock()
	s.cancel()
	for _, f := range s.files {
		f.Close()
	}
	if failed {
		return nil, protocolError()
	}
	sort.Slice(proofs, func(i, j int) bool { return proofs[i].Kind < proofs[j].Kind })
	return proofs, nil
}

// Abort interrupts and joins only this object's actual processes and pipes.
// Cancellation is never accepted as terminal EOF or a successful recording.
func (s *ReplayServers) Abort() {
	s.cancel()
	for _, f := range s.files {
		f.Close()
	}
	s.coordinator.Abort()
	<-s.done
}

func (s *ReplayServers) StderrSnapshot() (map[string]string, error) { return s.logs.Snapshot() }
