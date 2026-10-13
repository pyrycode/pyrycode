package relay

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pyrycode/pyrycode/internal/apps"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// HostedAppsAssets prepares stable confined assets bound to the exact backend generation.
// Calls and disposal run off Run; implementations must honor context cancellation.
type HostedAppsAssets interface {
	PrepareAsset(context.Context, string, string, string, apps.AssetRequest) (*apps.PreparedResponse, error)
}
type hostedResponse interface {
	Metadata() (apps.ResponseMetadata, error)
	Read([]byte) (int, error)
	Complete(error) error
	Close() error
	Done() <-chan struct{}
}
type hostedResource struct {
	s          *V2Session
	inbox      *hostedInbox
	id         uint64
	app        string
	ctx        context.Context
	cancel     context.CancelCauseFunc
	deadline   time.Time
	progress   atomic.Int64
	accepted   time.Time
	inactivity time.Duration
	credit     chan struct{}
	// Run owns the remaining fields; workers pass backendDone through results.
	backendDone        <-chan struct{}
	granted, delivered uint64
	totalChunks        uint64
	finalDelivered     bool
}

func (op *hostedResource) usable() bool {
	if op.ctx.Err() != nil || op.expired() {
		return false
	}
	select {
	case <-op.backendDone:
		return false
	default:
		return true
	}
}
func (op *hostedResource) failure() error {
	if err := context.Cause(op.ctx); err != nil {
		return err
	}
	if op.expired() {
		return context.DeadlineExceeded
	}
	return apps.ErrUnavailable
}
func (op *hostedResource) expired() bool {
	now := time.Now()
	return !now.Before(op.deadline) || now.Sub(op.accepted)-time.Duration(op.progress.Load()) >= op.inactivity
}
func (op *hostedResource) progressed() {
	if op.expired() {
		op.cancel(context.DeadlineExceeded)
		return
	}
	op.progress.Store(time.Since(op.accepted).Nanoseconds())
}

type hostedResourceResult struct {
	op          *hostedResource
	output      *hostedOutput
	backendDone <-chan struct{}
	err         error
	finished    bool
}

// Budget locks are leaves. Cleanup retains admission until backend disposal returns.
type hostedResourceBudget struct {
	mu          sync.Mutex
	count       int
	bytes       int64
	connections map[*hostedInbox]*hostedResourceUsage
	apps        map[string]int
}
type hostedResourceUsage struct {
	count int
	bytes int64
}

func (b *hostedResourceBudget) admit(op *hostedResource) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.connections == nil {
		b.connections = make(map[*hostedInbox]*hostedResourceUsage)
		b.apps = make(map[string]int)
	}
	u := b.connections[op.inbox]
	if u == nil {
		u = &hostedResourceUsage{}
	}
	if b.count >= 128 || u.count >= 8 || b.apps[op.app] >= 32 {
		return false
	}
	b.connections[op.inbox] = u
	u.count++
	b.count++
	b.apps[op.app]++
	return true
}
func (b *hostedResourceBudget) reserve(op *hostedResource, n int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	u := b.connections[op.inbox]
	if n < 0 || op.ctx.Err() != nil || u == nil || n > (16<<20)-u.bytes || n > (64<<20)-b.bytes {
		return false
	}
	u.bytes += n
	b.bytes += n
	return true
}
func (b *hostedResourceBudget) release(op *hostedResource, n int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.connections[op.inbox].bytes -= n
	b.bytes -= n
}
func (b *hostedResourceBudget) finish(op *hostedResource) {
	b.mu.Lock()
	defer b.mu.Unlock()
	u := b.connections[op.inbox]
	u.count--
	b.count--
	b.apps[op.app]--
	if b.apps[op.app] == 0 {
		delete(b.apps, op.app)
	}
	if u.count == 0 && u.bytes == 0 {
		delete(b.connections, op.inbox)
	}
}
func (m *V2SessionManager) resourceDurations() (time.Duration, time.Duration) {
	deadline, inactivity := m.cfg.hostedDeadline, m.cfg.hostedInactivity
	if deadline <= 0 {
		deadline = 30 * time.Second
	}
	if inactivity <= 0 {
		inactivity = 5 * time.Second
	}
	return deadline, inactivity
}
func (m *V2SessionManager) startHostedAsset(ctx context.Context, s *V2Session, req hostedRequest) {
	h := s.hosted
	if h.resources[req.id] != nil || (h.pending != nil && h.pending.id == req.id) {
		m.hostedError(ctx, s, req.id, "app.invalid_request", false)
		return
	}
	deadline, inactivity := m.resourceDurations()
	accepted := time.Now()
	timed, stopDeadline := context.WithDeadline(h.inbox.ctx, accepted.Add(deadline))
	work, cancelCause := context.WithCancelCause(timed)
	cancel := func(err error) { cancelCause(err); stopDeadline() }
	op := &hostedResource{s: s, inbox: h.inbox, id: req.id, app: req.asset.AppID, ctx: work, cancel: cancel, deadline: accepted.Add(deadline), accepted: accepted, inactivity: inactivity, credit: make(chan struct{}, 1)}
	op.progressed()
	if !m.resourceBudget.admit(op) {
		cancel(context.Canceled)
		m.hostedError(ctx, s, req.id, "app.busy", true)
		return
	}
	if h.resources == nil {
		h.resources = make(map[uint64]*hostedResource)
	}
	h.resources[req.id] = op
	go m.runHostedAsset(op, req.asset)
}
func (m *V2SessionManager) prepareHostedAsset(op *hostedResource, req protocol.AppAssetRequestPayload) (hostedResponse, error) {
	in := apps.AssetRequest{Path: req.Path, Query: req.Query, Navigation: req.Navigation, Reserve: func(n int64) bool { return m.resourceBudget.reserve(op, n) }, Release: func(n int64) { m.resourceBudget.release(op, n) }, Progress: op.progressed}
	if m.cfg.hostedPrepareAsset != nil {
		return m.cfg.hostedPrepareAsset(op.ctx, m.cfg.ServerID, op.app, req.ReleaseVersion, in)
	}
	if m.cfg.HostedAppsAssets == nil {
		return nil, apps.ErrUnavailable
	}
	return m.cfg.HostedAppsAssets.PrepareAsset(op.ctx, m.cfg.ServerID, op.app, req.ReleaseVersion, in)
}
func (m *V2SessionManager) resourceResult(res hostedResourceResult) bool {
	select {
	case m.resourceDone <- res:
		return true
	case <-res.op.inbox.ctx.Done():
		return false
	}
}
func (m *V2SessionManager) resourceOutput(op *hostedResource, p hostedResponse, typ string, payload any) error {
	if _, err := p.Metadata(); err != nil {
		return err
	}
	outcome := make(chan error, 1)
	o := &hostedOutput{resource: op, typ: typ, payload: payload, reply: op.id, outcome: outcome}
	if !m.resourceResult(hostedResourceResult{op: op, output: o, backendDone: p.Done()}) {
		return context.Canceled
	}
	select {
	case err := <-outcome:
		return err
	case <-op.inbox.ctx.Done():
		return context.Canceled
	}
}
func (m *V2SessionManager) runHostedAsset(op *hostedResource, req protocol.AppAssetRequestPayload) {
	defer m.resourceBudget.finish(op)
	defer m.cfg.Logger.Debug("relay: asset worker exited", "event", "v2.hosted.asset_exited", "conn_id", op.s.connID)
	p, err := m.prepareHostedAsset(op, req)
	if err != nil {
		m.resourceResult(hostedResourceResult{op: op, err: err})
		return
	}
	defer p.Close()
	meta, err := p.Metadata()
	if err == nil && (meta.Size < 0 || meta.Size > 8<<20) {
		err = apps.ErrResponseLimit
	}
	if err == nil {
		payload := protocol.AppResponsePayload{AppID: op.app, ReleaseVersion: req.ReleaseVersion, Status: meta.Status, Headers: meta.Headers, SizeBytes: uint64(meta.Size), TotalChunks: uint64((meta.Size + 32767) / 32768), SHA256: meta.SHA256}
		err = m.resourceOutput(op, p, protocol.TypeAppResponse, payload)
		for i := uint64(0); err == nil && i < payload.TotalChunks; i++ {
			if i > 0 {
				select {
				case <-op.credit:
				case <-op.ctx.Done():
					err = op.failure()
				case <-p.Done():
					err = apps.ErrUnavailable
				}
			}
			if err != nil {
				break
			}
			size := min(int64(32768), meta.Size-int64(i)*32768)
			if !m.resourceBudget.reserve(op, size) {
				err = apps.ErrResponseCapacity
				break
			}
			chunk := make([]byte, int(size))
			// ReadFull may combine several checked backend reads; each positive read is progress.
			_, err = io.ReadFull(hostedResourceReader{op: op, response: p}, chunk)
			if err == nil {
				err = m.resourceOutput(op, p, protocol.TypeAppResponseChunk, protocol.AppResponseChunkPayload{AppID: op.app, Index: i, Data: base64.StdEncoding.EncodeToString(chunk)})
			}
			m.resourceBudget.release(op, size)
		}
	}
	if err == nil {
		err = p.Complete(nil)
	}
	m.resourceResult(hostedResourceResult{op: op, err: err, finished: err == nil})
}

type hostedResourceReader struct {
	op       *hostedResource
	response hostedResponse
}

func (r hostedResourceReader) Read(buf []byte) (int, error) {
	n, err := r.response.Read(buf)
	if n > 0 {
		r.op.progressed()
	}
	return n, err
}
func (m *V2SessionManager) handleHostedResource(ctx context.Context, res hostedResourceResult) {
	op := res.op
	s := op.s
	if s.state != V2StateOpen || m.sessions[s.connID] != s || s.hosted.resources[op.id] != op {
		if res.output != nil {
			res.output.finish(context.Canceled)
		}
		return
	}
	if !m.hostedAuthorized(s) {
		if res.output != nil {
			res.output.finish(context.Canceled)
		}
		m.closeWith(ctx, s, StatusUnauthorized, nil)
		return
	}
	if res.backendDone != nil {
		op.backendDone = res.backendDone
	}
	if res.finished && op.ctx.Err() == nil {
		delete(s.hosted.resources, op.id)
		op.cancel(context.Canceled)
		return
	}
	if res.err != nil || !op.usable() {
		if res.output != nil {
			res.output.finish(context.Canceled)
		}
		err := res.err
		if err == nil || op.ctx.Err() != nil {
			err = op.failure()
		}
		m.retireHostedResource(ctx, s, op, hostedResourceCode(err))
		return
	}
	if res.output.typ == protocol.TypeAppResponse {
		op.totalChunks = res.output.payload.(protocol.AppResponsePayload).TotalChunks
	}
	m.hostedEnqueue(ctx, s, res.output)
}
func (m *V2SessionManager) creditHostedResource(ctx context.Context, s *V2Session, c protocol.AppResponseCreditPayload) {
	op := s.hosted.resources[c.RequestID]
	if op == nil || op.finalDelivered {
		return
	}
	if !op.usable() {
		m.retireHostedResource(ctx, s, op, hostedResourceCode(op.failure()))
		return
	}
	if c.AppID != op.app {
		m.retireHostedResource(ctx, s, op, "app.invalid_request")
		return
	}
	if c.NextIndex <= op.granted {
		return
	}
	if c.NextIndex != op.granted+1 || c.NextIndex != op.delivered {
		m.retireHostedResource(ctx, s, op, "app.invalid_request")
		return
	}
	op.granted = c.NextIndex
	op.progressed()
	select {
	case op.credit <- struct{}{}:
	default:
		m.retireHostedResource(ctx, s, op, "app.invalid_request")
	}
}
func (m *V2SessionManager) retireHostedResource(ctx context.Context, s *V2Session, op *hostedResource, code string) {
	h := s.hosted
	if h.resources[op.id] != op {
		return
	}
	delete(h.resources, op.id)
	kept := h.queue[:0]
	for _, o := range h.queue {
		if o.resource == op {
			o.finish(context.Canceled)
			m.releaseHostedOutput(s, o)
		} else {
			kept = append(kept, o)
		}
	}
	clear(h.queue[len(kept):])
	h.queue = kept
	op.cancel(context.Canceled)
	if h.active != nil && h.active.resource == op {
		m.closeWith(ctx, s, StatusSessionGone, nil)
		return
	}
	if code != "" {
		m.hostedError(ctx, s, op.id, code, code == "app.busy" || code == "app.timeout" || code == "app.unavailable" || code == "app.io_failed")
	}
}
func (m *V2SessionManager) sweepHostedResources(ctx context.Context) {
	now := time.Now()
	_, inactivity := m.resourceDurations()
	for _, s := range m.sessions {
		if s.hosted == nil {
			continue
		}
		m.drainHostedDelivery(ctx, s)
		for _, op := range s.hosted.resources {
			code := ""
			if !now.Before(op.deadline) || now.Sub(op.accepted)-time.Duration(op.progress.Load()) >= inactivity {
				code = "app.timeout"
			} else if !op.finalDelivered && !op.usable() {
				code = hostedResourceCode(op.failure())
			}
			if code != "" {
				m.retireHostedResource(ctx, s, op, code)
			}
		}
	}
}
func (m *V2SessionManager) releaseHostedOutput(s *V2Session, o *hostedOutput) {
	if o.resource == nil {
		m.releaseHosted(s, 1)
		return
	}
	m.hostedMu.Lock()
	s.hosted.inbox.resourceUsed--
	m.hostedMu.Unlock()
}
func hostedResourceCode(err error) string {
	switch {
	case errors.Is(err, apps.ErrAssetPath):
		return "app.invalid_request"
	case errors.Is(err, apps.ErrNotFound):
		return "app.not_found"
	case errors.Is(err, apps.ErrReleaseChanged):
		return "app.release_changed"
	case errors.Is(err, apps.ErrUnavailable):
		return "app.unavailable"
	case errors.Is(err, apps.ErrResponseLimit):
		return "app.limit_exceeded"
	case errors.Is(err, apps.ErrResponseCapacity):
		return "app.busy"
	case errors.Is(err, context.DeadlineExceeded):
		return "app.timeout"
	default:
		return "app.io_failed"
	}
}
