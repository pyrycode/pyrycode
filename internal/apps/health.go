package apps

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

const (
	startupDeadline      = 15 * time.Second
	startupInterval      = 250 * time.Millisecond
	healthInterval       = 5 * time.Second
	healthRequestTimeout = time.Second
	healthReplyLimit     = 4096
)

var ErrUnavailable = errors.New("apps: service unavailable")

func privateClient(dial func(context.Context, string, string) (net.Conn, error)) *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, DialContext: dial}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func endpoint(g *generation) string { return "127.0.0.1:" + strconv.Itoa(g.port) }
func probeHealth(ctx context.Context, client *http.Client, g *generation) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+endpoint(g)+"/_pyry/health", nil)
	if err != nil {
		return false
	}
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, healthReplyLimit+1))
	if err != nil || len(body) > healthReplyLimit || ctx.Err() != nil {
		return false
	}
	value, err := strictJSON(body)
	fields, ok := value.(map[string]any)
	return err == nil && ok && fields["ready"] == true && fields["app_id"] == g.record.AppID && fields["release_version"] == g.record.ActiveRelease
}
func (o *Owner) supervise(ctx context.Context, id string, g *generation, spawned time.Time) string {
	startup, cancel := context.WithDeadline(ctx, spawned.Add(o.startup))
	defer cancel()
	client := privateClient((&net.Dialer{Timeout: o.requestTimeout}).DialContext)
	defer client.CloseIdleConnections()
	timer := time.NewTimer(0)
	defer timer.Stop()
	running := false
	for {
		phase := ctx
		if !running {
			phase = startup
		}
		ticks := timer.C
		if running && o.healthTicks != nil {
			ticks = o.healthTicks
		}
		select {
		case <-ctx.Done():
			return ""
		case <-g.exited:
			return "Service exited unexpectedly"
		case <-phase.Done():
			return "Service health check failed"
		case <-ticks:
		}
		probedAt := time.Now()
		request, finish := context.WithTimeout(phase, o.requestTimeout)
		healthy := probeHealth(request, client, g)
		finish()
		select {
		case <-g.exited:
			return "Service exited unexpectedly"
		default:
		}
		if ctx.Err() != nil {
			return ""
		}
		if !running {
			if startup.Err() != nil {
				return "Service health check failed"
			}
			if healthy {
				if err := o.confirmRunning(startup, id, g); err != nil {
					if errors.Is(err, ErrUnavailable) {
						return "Service exited unexpectedly"
					}
					if errors.Is(err, context.DeadlineExceeded) {
						return "Service health check failed"
					}
					if !errors.Is(err, ErrStale) && !errors.Is(err, ErrRemoved) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
						o.remember(err)
					}
					return ""
				}
				running = true
			}
		} else {
			o.mu.Lock()
			if healthy {
				g.healthFailures = 0
				if o.now().Sub(g.healthySince) >= healthyReset {
					o.retryLocked(id).delay = 0
				}
			} else {
				g.healthFailures++
			}
			failed := g.healthFailures >= 3
			if failed {
				g.healthy = false
			}
			o.mu.Unlock()
			if failed {
				return "Service health check failed"
			}
		}
		interval := o.startInterval
		if running {
			if o.healthTicks != nil {
				continue
			}
			interval = o.runtimeInterval
		}
		timer.Reset(max(0, interval-time.Since(probedAt)))
	}
}
func (o *Owner) confirmRunning(ctx context.Context, id string, g *generation) error {
	o.registry.mu.Lock()
	defer o.registry.mu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if o.closed || g.ctx.Err() != nil || o.generations[id] != g {
		return ErrStale
	}
	select {
	case <-g.exited:
		return ErrUnavailable
	default:
	}
	r, _, err := o.registry.commitObservationLocked(g.record.Manifest.ServerID(), id, g.record.Revision, Observation{State: "running"})
	if err != nil {
		return err
	}
	g.record = r
	if ctx.Err() != nil {
		return ctx.Err()
	}
	select {
	case <-g.exited:
		return ErrUnavailable
	default:
	}
	g.healthy = true
	g.healthySince = o.now()
	return nil
}

// Target is an opaque, generation-owned service handle. Its zero value is unusable.
// It exposes no address; every request and connection is checked against ownership.
type Target struct {
	owner      *Owner
	generation *generation
}

// Lookup returns only a health-confirmed live target matching committed local state.
func (o *Owner) Lookup(serverID, appID string) (*Target, error) {
	o.registry.mu.Lock()
	defer o.registry.mu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	if serverID != o.registry.snapshot.ServerID {
		return nil, ErrUnavailable
	}
	g := o.generations[appID]
	if !o.usableLocked(g) {
		return nil, ErrUnavailable
	}
	return &Target{owner: o, generation: g}, nil
}

// usableLocked requires registry then owner locks. Record revisions may change for
// metadata, but identity, release, intent, observed state and ownership must match.
func (o *Owner) usableLocked(g *generation) bool {
	if g == nil || o.closed || !g.healthy || g.ctx.Err() != nil || o.generations[g.record.AppID] != g {
		return false
	}
	select {
	case <-g.exited:
		return false
	default:
	}
	for _, r := range o.registry.snapshot.Records {
		if r.AppID == g.record.AppID {
			return r.Manifest.ServerID() == o.registry.snapshot.ServerID && r.Desired == "available" && r.State == "running" && r.ActiveRelease == g.record.ActiveRelease
		}
	}
	return false
}
func (t *Target) valid() bool {
	if t == nil || t.owner == nil || t.generation == nil {
		return false
	}
	o := t.owner
	o.registry.mu.Lock()
	defer o.registry.mu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.usableLocked(t.generation)
}

// Do routes an HTTP request to the private service using the caller's path/query.
// Redirects and proxies are disabled. Retirement cancels headers and body reads;
// callers must close the response body. Resource limits belong to the consumer.
func (t *Target) Do(req *http.Request) (*http.Response, error) {
	if !t.valid() || req == nil || req.URL == nil {
		return nil, ErrUnavailable
	}
	g := t.generation
	o := t.owner
	ctx, cancel := context.WithCancel(req.Context())
	detach := context.AfterFunc(g.ctx, cancel)
	finish := func() { detach(); cancel() }
	routed := req.Clone(ctx)
	url := *req.URL
	url.Scheme = "http"
	url.Host = endpoint(g)
	url.User = nil
	routed.URL = &url
	routed.Host = url.Host
	routed.RequestURI = ""
	client := privateClient(func(ctx context.Context, network, address string) (net.Conn, error) {
		o.registry.mu.Lock()
		defer o.registry.mu.Unlock()
		o.mu.Lock()
		defer o.mu.Unlock()
		if !o.usableLocked(g) {
			return nil, ErrUnavailable
		}
		return (&net.Dialer{Timeout: healthRequestTimeout}).DialContext(ctx, "tcp4", endpoint(g))
	})
	response, err := client.Do(routed)
	if err != nil {
		finish()
		client.CloseIdleConnections()
		return nil, err
	}
	if !t.valid() {
		response.Body.Close()
		finish()
		return nil, ErrUnavailable
	}
	response.Body = &targetBody{ReadCloser: response.Body, target: t, finish: finish}
	return response, nil
}

type targetBody struct {
	io.ReadCloser
	target *Target
	finish func()
}

func (b *targetBody) Read(p []byte) (int, error) {
	if !b.target.valid() {
		return 0, ErrUnavailable
	}
	n, err := b.ReadCloser.Read(p)
	if !b.target.valid() {
		return 0, ErrUnavailable
	}
	return n, err
}
func (b *targetBody) Close() error { b.finish(); return b.ReadCloser.Close() }
