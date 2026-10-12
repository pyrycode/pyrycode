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
	listen          func() (net.Listener, error)
	mu              sync.Mutex
	registry        *Registry
	node            string
	generations     map[string]*generation
	ports           map[int]bool
	previous        map[string]int
	closed          bool
	grace           time.Duration
	failures        error
	kill            func(int, syscall.Signal) error
	startup         time.Duration
	startInterval   time.Duration
	runtimeInterval time.Duration
	requestTimeout  time.Duration
	wake            chan struct{}
	reconcileCtx    context.Context
	reconcileCancel context.CancelFunc
	reconciled      chan struct{}
	unsubscribe     func()
	retries         map[string]*retryState
	now             func() time.Time
}
type generation struct {
	ctx            context.Context
	exited         chan struct{}
	healthy        bool
	healthFailures int
	cancel         context.CancelFunc
	ready, done    chan struct{}
	err            error
	record         Record
	port           int
	cmd            *exec.Cmd
	cleanupErr     error
	healthySince   time.Time
	retired        bool
}

// NewOwner selects Node once and starts asynchronous committed-intent reconciliation.
// Construction waits for neither service spawn nor fresh health confirmation.
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
		return nil, fmt.Errorf("apps: resolve node executable: %w", err)
	}
	o := &Owner{registry: registry, node: node, generations: map[string]*generation{}, ports: map[int]bool{}, previous: map[string]int{}, grace: stopGrace, kill: syscall.Kill, startup: startupDeadline, startInterval: startupInterval, runtimeInterval: healthInterval, requestTimeout: healthRequestTimeout, listen: func() (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:0") }}
	o.now = time.Now
	o.retries = make(map[string]*retryState)
	o.wake = make(chan struct{}, 1)
	o.reconciled = make(chan struct{})
	o.reconcileCtx, o.reconcileCancel = context.WithCancel(context.Background())
	o.unsubscribe = registry.Subscribe(func(Change) {
		select {
		case o.wake <- struct{}{}:
		default:
		}
	})
	go o.reconcile()
	o.signal()
	return o, nil
}

// Start resumes available intent and waits for preparation/spawn or failure.
// Concurrent attempts coalesce; scheduled recovery returns the last failure or
// ErrUnavailable without bypassing backoff. Cancelling the initiating context retires its
// generation; a coalesced caller can cancel its own wait independently.
func (o *Owner) Start(ctx context.Context, appID string) error {
	if !validID(appID) {
		return ErrInvalidIdentity
	}
	o.registry.mu.Lock()
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		o.registry.mu.Unlock()
		return ErrClosed
	}
	current := o.currentLocked(appID)
	var err error
	if current.Desired != "available" || o.retryLocked(appID).suppressed {
		current, err = o.registry.intentLocked(ctx, appID, "available")
		if err == nil {
			o.retries[appID] = &retryState{}
		}
	}
	if err != nil {
		o.mu.Unlock()
		o.registry.mu.Unlock()
		return err
	}
	if current.ActiveRelease == "" {
		o.mu.Unlock()
		o.registry.mu.Unlock()
		return nil
	}
	g := o.generations[appID]
	if g != nil && (g.ctx.Err() != nil || current.ActiveRelease != g.record.ActiveRelease) {
		select {
		case <-g.done:
		default:
			if current.ActiveRelease != g.record.ActiveRelease || g.retired {
				g.retired = true
				g.cancel()
			}
			o.mu.Unlock()
			o.registry.mu.Unlock()
			select {
			case <-g.done:
				return o.Start(ctx, appID)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if g == nil || generationDone(g) {
		if o.now().Before(o.retryLocked(appID).due) {
			err = ErrUnavailable
			if g != nil && g.err != nil {
				err = g.err
			}
			o.mu.Unlock()
			o.registry.mu.Unlock()
			return err
		}
		g = o.admitLocked(ctx, current)
	}
	o.mu.Unlock()
	o.registry.mu.Unlock()
	select {
	case <-g.ready:
		return g.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *Owner) launch(ctx context.Context, id string, g *generation) {
	defer o.completed(id, g)
	cancel := g.cancel
	defer cancel()
	r := g.record
	o.registry.mu.Lock()
	current := o.currentLocked(id)
	var err error
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if current.Revision != r.Revision || current.Desired != "available" || current.ActiveRelease != r.ActiveRelease {
		err = ErrStale
	} else {
		var committed Record
		committed, _, err = o.registry.commitObservationLocked(r.Manifest.ServerID(), id, r.Revision, Observation{State: "starting"})
		if err == nil {
			r = committed
		}
	}
	o.registry.mu.Unlock()
	o.mu.Lock()
	g.record = r
	o.mu.Unlock()
	if err != nil {
		g.err = err
		o.rememberObservation(err)
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
			if err = listener.Close(); err != nil {
				err = fmt.Errorf("apps: release service listener: %w", err)
			}
		}
		if err == nil {
			if err = g.cmd.Start(); err != nil {
				err = fmt.Errorf("apps: spawn service: %w", err)
			}
		}
		o.mu.Unlock()
		o.registry.mu.Unlock()
	}
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, ErrStale) {
			_, _, writeErr := o.observeFailure(id, g, "Service could not start")
			err = errors.Join(err, writeErr)
			if !errors.Is(writeErr, ErrStale) && !errors.Is(writeErr, ErrRemoved) {
				o.remember(writeErr)
			}
		}
		g.err = err
		close(g.ready)
		return
	}
	spawned := time.Now()
	go func() {
		_ = g.cmd.Wait() // Exit status is represented by static safe metadata below.
		close(g.exited)
	}()
	close(g.ready)
	reason := o.supervise(ctx, id, g, spawned)
	o.mu.Lock()
	g.healthy = false
	cancelled := ctx.Err() != nil
	cancel()
	o.mu.Unlock()
	if reason != "" && !cancelled {
		_, _, err = o.observeFailure(id, g, reason)
		if !errors.Is(err, ErrStale) && !errors.Is(err, ErrRemoved) {
			o.remember(err)
		}
	}
	g.cleanupErr = o.cleanup(g.cmd.Process.Pid)
	<-g.exited
	o.remember(g.cleanupErr)
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
	// Admission, ownership capture and stopped intent share the same lock order
	// as Start. No generation admitted before this commit can escape cancellation.
	o.registry.mu.Lock()
	o.mu.Lock()
	g := o.generations[appID]
	if g != nil {
		g.retired = true
		g.cancel()
	}
	o.retries[appID] = &retryState{suppressed: true}
	r, err := o.registry.intentLocked(context.Background(), appID, "stopped")
	if err == nil {
		o.retries[appID].suppressed = false
	}
	o.mu.Unlock()
	o.registry.mu.Unlock()
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
	o.reconcileCancel()
	all := make([]*generation, 0, len(o.generations))
	for _, g := range o.generations {
		g.cancel()
		all = append(all, g)
	}
	o.mu.Unlock()
	o.unsubscribe()
	<-o.reconciled
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
		listener, err := o.listen()
		if err != nil {
			return nil, 0, fmt.Errorf("apps: reserve service port: %w", err)
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
	var result, probeErr error
	signal := func(sig syscall.Signal, operation string) {
		err := o.kill(-pid, sig)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			result = errors.Join(result, fmt.Errorf("apps: %s process group: %w", operation, err))
		}
	}
	signal(syscall.SIGTERM, "terminate")
	deadline := time.Now().Add(o.grace)
	for time.Now().Before(deadline) {
		err := o.kill(-pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return errors.Join(result, probeErr)
		}
		if err != nil && probeErr == nil {
			probeErr = fmt.Errorf("apps: inspect process group: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	signal(syscall.SIGKILL, "kill")
	return errors.Join(result, probeErr)
}
