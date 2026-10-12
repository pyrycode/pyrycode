package apps

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"sync"
	"unicode"
	"unicode/utf8"
)

var (
	ErrReleaseChanged   = errors.New("apps: active release changed")
	ErrResponseMetadata = errors.New("apps: invalid response metadata")
	ErrResponseLimit    = errors.New("apps: response byte limit exceeded")
	ErrResponseCapacity = errors.New("apps: response capacity unavailable")
	ErrResponseIO       = errors.New("apps: response I/O failed")
)

// ResponseInput supplies identity-encoded Body with a size hint (-1 unknown).
// Limit bounds decoded bytes; proving EOF can need one extra reserved byte.
// Reserve acquires all requested bytes atomically or returns false; Release returns them.
// Callbacks must be prompt and never reenter the result. Body must support concurrent
// Close that promptly unblocks Read, including on process retirement.
type ResponseInput struct {
	Body        io.ReadCloser
	Size, Limit int64
	Status      int
	Headers     map[string]string
	Reserve     func(int64) bool
	Release     func(int64)
}

// ResponseMetadata describes the complete stable body. Headers are detached.
type ResponseMetadata struct {
	Status  int
	Headers map[string]string
	Size    int64
	SHA256  string
}

// PreparedResponse owns staged bytes/reservations until Complete or Close.
// Read and Metadata check cancellation/generation; copied metadata expires on retirement.
// EOF retains capacity: Complete(nil) confirms delivery, Complete(err) reports failure,
// and Close abandons it. Methods may race; cancellation cleans up retained results.
type PreparedResponse struct {
	mu                      sync.Mutex
	ctx, caller             context.Context
	cancel                  context.CancelFunc
	target                  *Target
	source                  io.ReadCloser
	sourceOnce, disposeOnce sync.Once
	sourceErr               error
	unsubscribe             func()
	watched                 chan struct{}
	err                     error
	chunks                  [][]byte
	offset                  int
	held                    int64
	release                 func(int64)
	metadata                ResponseMetadata
}

// Prepare owns nonnil Body immediately, including on failure; success closes it.
// serverID comes from the authenticated boundary; this method authenticates no connection.
// Do not mutate source/headers during Prepare. Later changes cannot affect output.
// Consumers own status restrictions, header projection and HTTP no-body semantics.
func (o *Owner) Prepare(ctx context.Context, serverID, appID, release string, in ResponseInput) (_ *PreparedResponse, err error) {
	work, cancel := context.WithCancel(ctx)
	p := &PreparedResponse{ctx: work, caller: ctx, cancel: cancel, source: in.Body, release: in.Release, watched: make(chan struct{}), unsubscribe: func() {}}
	started := false
	defer func() {
		if err != nil {
			p.dispose(err)
			if started {
				<-p.watched
			}
		}
	}()
	p.unsubscribe = o.registry.Subscribe(func(c Change) {
		if c.AppID == appID && (c.Record == nil || c.Record.Desired != "available" || c.Record.State != "running" || c.Record.ActiveRelease != release) {
			cancel()
		}
	})
	o.registry.mu.Lock()
	o.mu.Lock()
	r := o.currentLocked(appID)
	switch {
	case serverID != o.registry.snapshot.ServerID || r.AppID == "" || r.Manifest.ServerID() != serverID:
		err = ErrNotFound
	case r.ActiveRelease != release:
		err = ErrReleaseChanged
	case !o.usableLocked(o.generations[appID]):
		err = ErrUnavailable
	default:
		p.target = &Target{owner: o, generation: o.generations[appID]}
	}
	o.mu.Unlock()
	o.registry.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if in.Body == nil || in.Size < -1 || in.Limit < 0 || in.Reserve == nil || in.Release == nil || !validResponseMetadata(in.Status, in.Headers) {
		return nil, ErrResponseMetadata
	}
	p.metadata = ResponseMetadata{Status: in.Status, Headers: maps.Clone(in.Headers)}
	if p.metadata.Headers == nil {
		p.metadata.Headers = map[string]string{}
	}
	started = true
	go func() {
		defer close(p.watched)
		select {
		case <-work.Done():
		case <-p.target.generation.ctx.Done():
		case <-p.target.generation.exited:
		}
		p.dispose(ErrUnavailable)
	}()
	if err = p.stage(in); err != nil {
		return nil, err
	}
	if p.closeSource() != nil {
		return nil, ErrResponseIO
	}
	if _, err = p.Metadata(); err != nil {
		return nil, err
	}
	return p, nil
}

func validResponseMetadata(status int, headers map[string]string) bool {
	if status < 200 || status > 599 || len(headers) > 8 {
		return false
	}
	for key, value := range headers {
		switch key {
		case "content-type", "cache-control", "etag", "last-modified":
		default:
			return false
		}
		if len(value) > 1024 || !utf8.ValidString(value) {
			return false
		}
		for _, r := range value {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	b, err := json.Marshal(headers)
	return err == nil && len(b) <= 4096
}
func (p *PreparedResponse) closeSource() error {
	p.sourceOnce.Do(func() {
		if p.source != nil {
			p.sourceErr = p.source.Close()
		}
	})
	return p.sourceErr
}
func (p *PreparedResponse) dispose(err error) {
	p.disposeOnce.Do(func() {
		p.cancel()
		p.closeSource() // Close must unblock an in-progress staging read before locking.
		p.mu.Lock()
		if err == nil {
			err = ErrClosed
		}
		p.err = err
		p.chunks = nil
		if p.held > 0 {
			p.release(p.held)
			p.held = 0
		}
		p.mu.Unlock()
		p.unsubscribe()
	})
}
func (p *PreparedResponse) checkLocked() error {
	if err := p.caller.Err(); err != nil {
		return err
	}
	if p.err != nil {
		return p.err
	}
	if p.ctx.Err() != nil || !p.target.valid() {
		return ErrUnavailable
	}
	return p.caller.Err()
}

// Metadata returns a detached snapshot only while the response remains usable.
func (p *PreparedResponse) Metadata() (ResponseMetadata, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		return ResponseMetadata{}, err
	}
	m := p.metadata
	m.Headers = maps.Clone(m.Headers)
	return m, nil
}
func (p *PreparedResponse) Read(buf []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		return 0, err
	}
	if len(buf) == 0 {
		return 0, nil
	}
	if len(p.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(buf, p.chunks[0][p.offset:])
	if err := p.checkLocked(); err != nil {
		clear(buf[:n])
		return 0, err
	}
	p.offset += n
	if p.offset == len(p.chunks[0]) {
		p.chunks = p.chunks[1:]
		p.offset = 0
	}
	return n, nil
}

// Complete releases ownership, reporting a sanitized delivery failure when nonnil.
func (p *PreparedResponse) Complete(deliveryErr error) error {
	p.mu.Lock()
	err := p.checkLocked()
	p.mu.Unlock()
	if err == nil && deliveryErr != nil {
		err = ErrResponseIO
	}
	p.dispose(err)
	<-p.watched
	return err
}

// Close abandons delivery. Repeated calls join the same cleanup and return nil.
func (p *PreparedResponse) Close() error { p.dispose(nil); <-p.watched; return nil }
