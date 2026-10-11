package apps

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const stopGrace = 5 * time.Second

var ErrClosed = errors.New("apps: process owner shut down")

// Owner executes committed releases independently of conversation sessions.
// Use one owner per registry. Start proves spawn only, never readiness. Registry
// callbacks must not call Owner methods. Private generations remain package-owned.
type Owner struct {
	mu          sync.Mutex
	registry    *Registry
	node        string
	generations map[string]*generation
	ports       map[int]bool
	previous    map[string]int
	closed      bool
	grace       time.Duration
	failures    error
}
type generation struct {
	cancel      context.CancelFunc
	ready, done chan struct{}
	err         error
	record      Record
	port        int
	cmd         *exec.Cmd
	cleanupErr  error
}

// NewOwner selects the specified Node executable once; no runtime fallback occurs.
func NewOwner(registry *Registry, nodeExecutable string) (*Owner, error) {
	if registry == nil {
		return nil, ErrInvalidStorage
	}
	node, err := exec.LookPath(nodeExecutable)
	if err != nil {
		return nil, fmt.Errorf("apps: select node: %w", err)
	}
	node, err = filepath.Abs(node)
	if err != nil {
		return nil, err
	}
	return &Owner{registry: registry, node: node, generations: map[string]*generation{}, ports: map[int]bool{}, previous: map[string]int{}, grace: stopGrace}, nil
}

// Start resumes available intent and waits for preparation/spawn or failure.
// Concurrent attempts coalesce. Cancelling the initiating context retires its
// generation; a coalesced caller can cancel its own wait independently.
func (o *Owner) Start(ctx context.Context, appID string) error {
	if !validID(appID) {
		return ErrInvalidIdentity
	}
	o.registry.mu.Lock()
	var current Record
	for _, r := range o.registry.snapshot.Records {
		if r.AppID == appID {
			current = r
			break
		}
	}
	o.mu.Lock()
	o.registry.mu.Unlock()
	if o.closed {
		o.mu.Unlock()
		return ErrClosed
	}
	g := o.generations[appID]
	if g != nil {
		select {
		case <-g.done:
			g = nil
		default:
		}
		if g != nil {
			select {
			case <-g.ready:
				if g.err == nil && (current.ActiveRelease != g.record.ActiveRelease || current.Desired != "available") {
					g.cancel()
					o.mu.Unlock()
					<-g.done
					return o.Start(ctx, appID)
				}
			default:
			}
		}
	}
	if g == nil {
		work, cancel := context.WithCancel(ctx)
		g = &generation{cancel: cancel, ready: make(chan struct{}), done: make(chan struct{})}
		o.generations[appID] = g
		go o.launch(work, appID, g)
	}
	o.mu.Unlock()
	select {
	case <-g.ready:
		return g.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *Owner) launch(ctx context.Context, id string, g *generation) {
	defer close(g.done)
	defer g.cancel()
	r, err := o.registry.intent(ctx, id, "available")
	g.record = r
	if err != nil {
		g.err = err
		close(g.ready)
		return
	}
	if r.ActiveRelease == "" {
		close(g.ready)
		return
	}
	build, data, err := prepareRelease(ctx, o.registry.root, r, o.node)
	var listener net.Listener
	if err == nil {
		listener, g.port, err = o.reserve(id)
	}
	if listener != nil {
		defer func() { _ = listener.Close() }()
	}
	if g.port != 0 {
		defer func() { o.mu.Lock(); delete(o.ports, g.port); o.mu.Unlock() }()
	}
	if err == nil {
		g.cmd = exec.Command(o.node, "dist/service/main.js")
		g.cmd.Dir = build
		g.cmd.Env = []string{"PYRY_APP_ID=" + id, "PYRY_SERVER_ID=" + r.Manifest.ServerID(), "PYRY_APP_RELEASE=" + r.ActiveRelease, "PYRY_APP_DATA_DIR=" + data, "PYRY_APP_DB_PATH=" + data + "/app.sqlite", "PYRY_APP_BIND_HOST=127.0.0.1", "PYRY_APP_PORT=" + strconv.Itoa(g.port), "NODE_ENV=production"}
		g.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		// The last currentness check and spawn share the registry lock. A newer
		// committed intent or release cannot pass between this guard and execution.
		o.registry.mu.Lock()
		o.mu.Lock()
		err = ctx.Err()
		if err == nil && o.closed {
			err = ErrClosed
		}
		current := false
		for _, record := range o.registry.snapshot.Records {
			if record.AppID == id && record.Revision == r.Revision {
				current = true
				break
			}
		}
		if err == nil && !current {
			err = ErrStale
		}
		if err == nil {
			err = listener.Close()
		}
		if err == nil {
			err = g.cmd.Start()
		}
		o.mu.Unlock()
		o.registry.mu.Unlock()
	}
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, ErrStale) {
			_, _, writeErr := o.registry.CommitObservation(r.Manifest.ServerID(), id, r.Revision, Observation{State: "failed", LastError: &LastError{Code: "app.unavailable", Message: "Service could not start"}})
			err = errors.Join(err, writeErr)
			if !errors.Is(writeErr, ErrStale) && !errors.Is(writeErr, ErrRemoved) {
				o.remember(writeErr)
			}
		}
		g.err = err
		close(g.ready)
		return
	}
	close(g.ready)
	exited := make(chan error, 1)
	go func() { exited <- g.cmd.Wait() }()
	unexpected := false
	select {
	case <-ctx.Done():
	case <-exited:
		unexpected = true
	}
	cleanupErr := o.cleanup(g.cmd.Process.Pid)
	g.cleanupErr = cleanupErr
	if !unexpected {
		<-exited
	}
	o.remember(cleanupErr)
	if unexpected && ctx.Err() == nil {
		_, _, err = o.registry.CommitObservation(r.Manifest.ServerID(), id, r.Revision, Observation{State: "failed", LastError: &LastError{Code: "app.unavailable", Message: "Service exited unexpectedly"}})
		if !errors.Is(err, ErrStale) && !errors.Is(err, ErrRemoved) {
			o.remember(err)
		}
	}
}

func (o *Owner) remember(err error) {
	if err != nil {
		o.mu.Lock()
		o.failures = errors.Join(o.failures, err)
		o.mu.Unlock()
	}
}

// Stop cancels owned work even if stopped intent cannot be persisted. Success
// includes durable intent and process-group retirement, followed by observation.
func (o *Owner) Stop(appID string) error {
	if !validID(appID) {
		return ErrInvalidIdentity
	}
	o.mu.Lock()
	g := o.generations[appID]
	if g != nil {
		g.cancel()
	}
	o.mu.Unlock()
	r, err := o.registry.intent(context.Background(), appID, "stopped")
	if g != nil {
		<-g.done
	}
	if err != nil {
		return err
	}
	_, _, err = o.registry.CommitObservation(r.Manifest.ServerID(), appID, r.Revision, Observation{State: "stopped"})
	if errors.Is(err, ErrStale) {
		rows, _ := o.registry.List()
		for _, current := range rows {
			if current.AppID == appID && current.Desired == "stopped" && current.State == "stopped" {
				err = nil
				break
			}
		}
	}
	if g != nil {
		return errors.Join(err, g.cleanupErr)
	}
	return err
}

// Shutdown closes launch admission and joins every owned group without changing
// desired intent. Repeated calls join the same retired workers.
func (o *Owner) Shutdown() error {
	o.mu.Lock()
	o.closed = true
	all := make([]*generation, 0, len(o.generations))
	for _, g := range o.generations {
		g.cancel()
		all = append(all, g)
	}
	o.mu.Unlock()
	var result error
	for _, g := range all {
		<-g.done
		if g.record.Revision != 0 {
			_, _, err := o.registry.CommitObservation(g.record.Manifest.ServerID(), g.record.AppID, g.record.Revision, Observation{State: "stopped"})
			if !errors.Is(err, ErrStale) && !errors.Is(err, ErrRemoved) {
				result = errors.Join(result, err)
			}
		}
	}
	o.mu.Lock()
	result = errors.Join(result, o.failures)
	o.mu.Unlock()
	return result
}

func (o *Owner) reserve(id string) (net.Listener, int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, 0, ErrClosed
	}
	for attempt := 0; attempt < 128; attempt++ {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, 0, err
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if o.ports[port] || o.previous[id] == port {
			_ = listener.Close()
			continue
		}
		o.ports[port] = true
		o.previous[id] = port
		return listener, port, nil
	}
	return nil, 0, errors.New("apps: port reservation exhausted")
}

// cleanup watches the group rather than only its leader: a leader may already
// have exited while descendants still hold service resources.
func (o *Owner) cleanup(pid int) error {
	var result error
	signal := func(sig syscall.Signal) {
		err := syscall.Kill(-pid, sig)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			result = errors.Join(result, err)
		}
	}
	signal(syscall.SIGTERM)
	deadline := time.Now().Add(o.grace)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
			return result
		}
		time.Sleep(10 * time.Millisecond)
	}
	signal(syscall.SIGKILL)
	return result
}
