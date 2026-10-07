package streamsup

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// maxMCPStatusServers caps the retained prefix of one turnevent.MCPStatus.
// Claude controls the decoded array length, so the per-entry Error bound alone
// cannot bound the event. Sixteen leaves ample room above the three-server live
// capture while keeping result allocation independent of an inflated tail.
// Overflow is reported in MCPStatus.DroppedServers rather than hidden.
const maxMCPStatusServers = 16

// maxMCPStatusError caps one MCPServerStatus.Error at construction. Error is
// arbitrary Claude-authored prose rather than a token, so a byte cut remains a
// truthful partial rendering. truncateField also removes a partial trailing rune,
// keeping the event's string valid UTF-8. This separate constant makes the status
// report's budget independent of unrelated 256-byte fields.
const maxMCPStatusError = 256

type mcpStatusChildPolicy struct {
	installed bool
	eligible  bool
	attempted bool
	request   func() error
}

// beginMCPStatusChild installs parser state for one exact spawn. It is called before
// cmd.Start, so no output from that child can be classified under its predecessor's
// eligibility, once-only latch, confirmed permission mode, or pending mode writes.
func (p *Parser) beginMCPStatusChild(eligible bool, request func() error) {
	p.permissionModes.beginChild()
	// A child replacement terminates every query aimed at its predecessor. The
	// request-id prefix keeps any already-buffered late reply private even after
	// its pending entry is gone. Actuations, context-usage queries and applied-settings
	// queries retire on the same boundary and for the same reason: a waiter must not
	// outlive the exact child it targeted.
	p.mcpStatusQueries.failAll()
	p.mcpActuations.failAll()
	p.contextUsageQueries.failAll()
	p.appliedSettingsQueries.failAll()
	p.mcpStatusPolicy = mcpStatusChildPolicy{
		installed: true,
		eligible:  eligible,
		request:   request,
	}
}

func (p *Parser) registerContextUsageRequest(id string) *pendingContextUsageRequest {
	return p.contextUsageRequests.register(id)
}

func (p *Parser) removeContextUsageRequest(id string, pending *pendingContextUsageRequest) {
	p.contextUsageRequests.remove(id, pending)
}

func (p *Parser) registerContextUsageQuery(id string) *pendingContextUsageQuery {
	return p.contextUsageQueries.register(id)
}

func (p *Parser) removeContextUsageQuery(id string, pending *pendingContextUsageQuery) {
	p.contextUsageQueries.remove(id, pending)
}

func (p *Parser) failContextUsageQueries() {
	p.contextUsageQueries.failAll()
}

func (p *Parser) registerAppliedSettingsQuery(id string) *pendingAppliedSettingsQuery {
	return p.appliedSettingsQueries.register(id)
}

func (p *Parser) removeAppliedSettingsQuery(id string, pending *pendingAppliedSettingsQuery) {
	p.appliedSettingsQueries.remove(id, pending)
}

func (p *Parser) failAppliedSettingsQueries() {
	p.appliedSettingsQueries.failAll()
}

func (p *Parser) registerMCPStatusQuery(id string) *pendingMCPStatusQuery {
	return p.mcpStatusQueries.register(id)
}

func (p *Parser) removeMCPStatusQuery(id string, pending *pendingMCPStatusQuery) {
	p.mcpStatusQueries.remove(id, pending)
}

func (p *Parser) failMCPStatusQueries() {
	p.mcpStatusQueries.failAll()
}

func (p *Parser) registerMCPActuation(id string) *pendingMCPActuation {
	return p.mcpActuations.register(id)
}

func (p *Parser) removeMCPActuation(id string, pending *pendingMCPActuation) {
	p.mcpActuations.remove(id, pending)
}

func (p *Parser) failMCPActuations() {
	p.mcpActuations.failAll()
}

// mcpStatusResponseLine is the isolated decode target for a status report carried
// by a top-level control_response. MCPServers stays raw long enough to distinguish
// a present empty array from a missing or null key. Request id and every sibling
// payload key are omitted, so neither correlation nor logging can accidentally
// start depending on them.
type mcpStatusResponseLine struct {
	Response struct {
		Subtype  string `json:"subtype"`
		Response struct {
			MCPServers json.RawMessage `json:"mcpServers"`
		} `json:"response"`
	} `json:"response"`
}

// mcpStatusServerLine declares only the fields the neutral event carries. Config,
// tools and serverInfo.name never cross the decode target, which is stronger than
// decoding them and relying on a later projection to remember to discard them.
type mcpStatusServerLine struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	Scope      string `json:"scope"`
	ServerInfo struct {
		Version string `json:"version"`
	} `json:"serverInfo"`
}

// contextUsageResponseIDLine is the narrow first decode of a context-usage reply.
// It excludes subtype and payload so an exact pending id can be retired before
// either is interpreted.
type contextUsageResponseIDLine struct {
	Response struct {
		RequestID string `json:"request_id"`
	} `json:"response"`
}

type contextUsageCategoryLine struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
}

type contextUsageMCPToolLine struct {
	Name       string `json:"name"`
	ServerName string `json:"serverName"`
	Tokens     int    `json:"tokens"`
}

type contextUsageMemoryFileLine struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Tokens int    `json:"tokens"`
}

type contextUsageResponseLine struct {
	Response struct {
		Subtype  string `json:"subtype"`
		Response *struct {
			Model       string                       `json:"model"`
			TotalTokens int                          `json:"totalTokens"`
			MaxTokens   int                          `json:"maxTokens"`
			Percentage  int                          `json:"percentage"`
			Categories  []contextUsageCategoryLine   `json:"categories"`
			MCPTools    []contextUsageMCPToolLine    `json:"mcpTools"`
			MemoryFiles []contextUsageMemoryFileLine `json:"memoryFiles"`
		} `json:"response"`
	} `json:"response"`
}

// appliedSettingsResponseLine declares only the successful response discriminator
// and the applied object. Effective settings, sources and all other get_settings
// siblings are structurally unreachable from the decoder.
type appliedSettingsResponseLine struct {
	Response struct {
		Subtype  string `json:"subtype"`
		Response *struct {
			Applied json.RawMessage `json:"applied"`
		} `json:"response"`
	} `json:"response"`
}

// appliedSettingsLine uses raw fields so missing effort remains distinguishable
// from explicit null and wrong JSON types cannot collapse to zero strings.
type appliedSettingsLine struct {
	Model  json.RawMessage `json:"model"`
	Effort json.RawMessage `json:"effort"`
}

type pendingContextUsageRequest struct {
	done    chan struct{}
	success bool
	once    sync.Once
}

func (r *pendingContextUsageRequest) resolve(success bool) {
	r.once.Do(func() {
		r.success = success
		close(r.done)
	})
}

func (r *pendingContextUsageRequest) written() bool {
	<-r.done
	return r.success
}

type contextUsageRequests struct {
	mu      sync.Mutex
	pending map[string]*pendingContextUsageRequest
}

func (r *contextUsageRequests) register(id string) *pendingContextUsageRequest {
	pending := &pendingContextUsageRequest{done: make(chan struct{})}
	r.mu.Lock()
	if r.pending == nil {
		r.pending = make(map[string]*pendingContextUsageRequest)
	}
	r.pending[id] = pending
	r.mu.Unlock()
	return pending
}

func (r *contextUsageRequests) take(id string) *pendingContextUsageRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending := r.pending[id]
	delete(r.pending, id)
	return pending
}

func (r *contextUsageRequests) remove(id string, pending *pendingContextUsageRequest) {
	r.mu.Lock()
	if r.pending[id] == pending {
		delete(r.pending, id)
	}
	r.mu.Unlock()
}

type contextUsageQueryResult struct {
	usage turnevent.ContextUsage
	ok    bool
}

// pendingContextUsageQuery is one caller waiting on the reading it asked for. It is
// pendingMCPStatusQuery's shape with a different payload, and the two are deliberately
// NOT unified behind a generic: the status path is shipped and proven, and
// parameterising its result type would put it inside this diff for no behavioural gain.
//
// THE RESULT CHANNEL'S ONE SLOT IS LOAD-BEARING. claim, failAll and a canceled caller's
// remove can race; the buffer plus resultOnce is what lets the stdout forwarder complete
// a waiter that has already walked away without blocking on nobody's receive.
type pendingContextUsageQuery struct {
	writeDone  chan struct{}
	writeOK    bool
	writeOnce  sync.Once
	result     chan contextUsageQueryResult
	resultOnce sync.Once
}

func newPendingContextUsageQuery() *pendingContextUsageQuery {
	return &pendingContextUsageQuery{
		writeDone: make(chan struct{}),
		result:    make(chan contextUsageQueryResult, 1),
	}
}

func (p *pendingContextUsageQuery) resolveWrite(ok bool) {
	p.writeOnce.Do(func() {
		p.writeOK = ok
		close(p.writeDone)
	})
}

// written blocks until the write outcome is known. It is what lets a FAILED WRITE beat
// a reply the parser has already claimed: the child answering some request does not
// establish that THIS one reached it intact, so a claim arriving while the writer is
// still failing must not report a reading.
func (p *pendingContextUsageQuery) written() bool {
	<-p.writeDone
	return p.writeOK
}

func (p *pendingContextUsageQuery) complete(usage turnevent.ContextUsage, ok bool) {
	p.resultOnce.Do(func() {
		p.result <- contextUsageQueryResult{usage: usage, ok: ok}
	})
}

// contextUsageQueries is a SECOND MAP rather than a widening of contextUsageRequests
// above, for the reason mcpActuations records against mcpStatusQueries: the two carry
// different payloads — a write verdict against a reading — and, decisively, different id
// namespaces. claim consumes every unregistered id carrying ITS prefix, so merging the
// automatic lane's bare sequence ids into this map would swallow readings the shared
// sink must still publish.
type contextUsageQueries struct {
	mu      sync.Mutex
	pending map[string]*pendingContextUsageQuery
}

func (q *contextUsageQueries) register(id string) *pendingContextUsageQuery {
	pending := newPendingContextUsageQuery()
	q.mu.Lock()
	if q.pending == nil {
		q.pending = make(map[string]*pendingContextUsageQuery)
	}
	q.pending[id] = pending
	q.mu.Unlock()
	return pending
}

// claim removes an exact pending query before its payload is interpreted. An
// unregistered id in the daemon's private query namespace is still claimed: it is a
// duplicate, or a reply whose waiter was canceled, failed, or retired at a child
// boundary. Claiming it is what keeps such a late reply off the shared sink.
func (q *contextUsageQueries) claim(id string) (*pendingContextUsageQuery, bool) {
	q.mu.Lock()
	pending := q.pending[id]
	delete(q.pending, id)
	q.mu.Unlock()
	if pending != nil {
		return pending, true
	}
	return nil, strings.HasPrefix(id, contextUsageQueryIDPrefix)
}

func (q *contextUsageQueries) remove(id string, pending *pendingContextUsageQuery) {
	q.mu.Lock()
	if q.pending[id] == pending {
		delete(q.pending, id)
	}
	q.mu.Unlock()
}

func (q *contextUsageQueries) failAll() {
	q.mu.Lock()
	pending := make([]*pendingContextUsageQuery, 0, len(q.pending))
	for id, query := range q.pending {
		pending = append(pending, query)
		delete(q.pending, id)
	}
	q.mu.Unlock()
	for _, query := range pending {
		query.complete(turnevent.ContextUsage{}, false)
	}
}

type appliedSettingsQueryResult struct {
	settings AppliedSettings
	ok       bool
}

// pendingAppliedSettingsQuery keeps the writer's verdict separate from the
// response result. An early reply may be claimed while Write is still returning;
// it cannot become available until that write is known to have succeeded.
type pendingAppliedSettingsQuery struct {
	writeDone  chan struct{}
	writeOK    bool
	writeOnce  sync.Once
	result     chan appliedSettingsQueryResult
	resultOnce sync.Once
}

func newPendingAppliedSettingsQuery() *pendingAppliedSettingsQuery {
	return &pendingAppliedSettingsQuery{
		writeDone: make(chan struct{}),
		result:    make(chan appliedSettingsQueryResult, 1),
	}
}

func (p *pendingAppliedSettingsQuery) resolveWrite(ok bool) {
	p.writeOnce.Do(func() {
		p.writeOK = ok
		close(p.writeDone)
	})
}

func (p *pendingAppliedSettingsQuery) written() bool {
	<-p.writeDone
	return p.writeOK
}

func (p *pendingAppliedSettingsQuery) complete(settings AppliedSettings, ok bool) {
	p.resultOnce.Do(func() {
		p.result <- appliedSettingsQueryResult{settings: settings, ok: ok}
	})
}

type appliedSettingsQueries struct {
	mu      sync.Mutex
	pending map[string]*pendingAppliedSettingsQuery
}

func (q *appliedSettingsQueries) register(id string) *pendingAppliedSettingsQuery {
	pending := newPendingAppliedSettingsQuery()
	q.mu.Lock()
	if q.pending == nil {
		q.pending = make(map[string]*pendingAppliedSettingsQuery)
	}
	q.pending[id] = pending
	q.mu.Unlock()
	return pending
}

// claim consumes late ids in the private namespace even after their waiter was
// canceled or retired. That keeps a predecessor's payload off every shared path.
func (q *appliedSettingsQueries) claim(id string) (*pendingAppliedSettingsQuery, bool) {
	q.mu.Lock()
	pending := q.pending[id]
	delete(q.pending, id)
	q.mu.Unlock()
	if pending != nil {
		return pending, true
	}
	return nil, strings.HasPrefix(id, appliedSettingsQueryIDPrefix)
}

func (q *appliedSettingsQueries) remove(id string, pending *pendingAppliedSettingsQuery) {
	q.mu.Lock()
	if q.pending[id] == pending {
		delete(q.pending, id)
	}
	q.mu.Unlock()
}

func (q *appliedSettingsQueries) failAll() {
	q.mu.Lock()
	pending := make([]*pendingAppliedSettingsQuery, 0, len(q.pending))
	for id, query := range q.pending {
		pending = append(pending, query)
		delete(q.pending, id)
	}
	q.mu.Unlock()
	for _, query := range pending {
		query.complete(AppliedSettings{}, false)
	}
}

type mcpStatusQueryResult struct {
	status turnevent.MCPStatus
	ok     bool
}

type pendingMCPStatusQuery struct {
	writeDone  chan struct{}
	writeOK    bool
	writeOnce  sync.Once
	result     chan mcpStatusQueryResult
	resultOnce sync.Once
}

func newPendingMCPStatusQuery() *pendingMCPStatusQuery {
	return &pendingMCPStatusQuery{
		writeDone: make(chan struct{}),
		result:    make(chan mcpStatusQueryResult, 1),
	}
}

func (p *pendingMCPStatusQuery) resolveWrite(ok bool) {
	p.writeOnce.Do(func() {
		p.writeOK = ok
		close(p.writeDone)
	})
}

func (p *pendingMCPStatusQuery) written() bool {
	<-p.writeDone
	return p.writeOK
}

func (p *pendingMCPStatusQuery) complete(status turnevent.MCPStatus, ok bool) {
	p.resultOnce.Do(func() {
		p.result <- mcpStatusQueryResult{status: status, ok: ok}
	})
}

type mcpStatusQueries struct {
	mu      sync.Mutex
	pending map[string]*pendingMCPStatusQuery
}

func (q *mcpStatusQueries) register(id string) *pendingMCPStatusQuery {
	pending := newPendingMCPStatusQuery()
	q.mu.Lock()
	if q.pending == nil {
		q.pending = make(map[string]*pendingMCPStatusQuery)
	}
	q.pending[id] = pending
	q.mu.Unlock()
	return pending
}

// claim removes an exact pending query before its payload is interpreted. An
// unregistered id in the daemon's private query namespace is still claimed: it
// is a duplicate or a response whose waiter was canceled, failed, or replaced.
func (q *mcpStatusQueries) claim(id string) (*pendingMCPStatusQuery, bool) {
	q.mu.Lock()
	pending := q.pending[id]
	delete(q.pending, id)
	q.mu.Unlock()
	if pending != nil {
		return pending, true
	}
	return nil, strings.HasPrefix(id, mcpStatusQueryIDPrefix)
}

func (q *mcpStatusQueries) remove(id string, pending *pendingMCPStatusQuery) {
	q.mu.Lock()
	if q.pending[id] == pending {
		delete(q.pending, id)
	}
	q.mu.Unlock()
}

func (q *mcpStatusQueries) failAll() {
	q.mu.Lock()
	pending := make([]*pendingMCPStatusQuery, 0, len(q.pending))
	for id, query := range q.pending {
		pending = append(pending, query)
		delete(q.pending, id)
	}
	q.mu.Unlock()
	for _, query := range pending {
		query.complete(turnevent.MCPStatus{}, false)
	}
}

// pendingMCPActuation is one caller waiting on its MCP reconnect, toggle or task
// stop ack. The result is a BARE VERDICT and that is the MCP capture's finding, not a
// simplification: internal/e2e/realclaude/testdata/mcp_status_v2.1.259.json records
// claude answering both verbs with a control_response carrying subtype and
// request_id and NOTHING ELSE — no server list, no per-server row. There is no
// payload to carry back, so a caller wanting a fresh inventory asks QueryMCPStatus
// for one separately.
//
// The shape is pendingMCPStatusQuery's deliberately, writeDone gate included, and
// the two are NOT unified behind a generic: the status path is shipped and proven,
// and parameterising its result type would put it inside an actuation diff for no
// behavioural gain.
type pendingMCPActuation struct {
	writeDone  chan struct{}
	writeOK    bool
	writeOnce  sync.Once
	result     chan bool
	resultOnce sync.Once
}

func newPendingMCPActuation() *pendingMCPActuation {
	return &pendingMCPActuation{
		writeDone: make(chan struct{}),
		result:    make(chan bool, 1),
	}
}

func (p *pendingMCPActuation) resolveWrite(ok bool) {
	p.writeOnce.Do(func() {
		p.writeOK = ok
		close(p.writeDone)
	})
}

// written blocks until the write outcome is known. It is what lets a FAILED WRITE
// beat an ack the parser has already claimed: the child answering some request does
// not establish that THIS one reached it intact, so a claim that arrives while the
// writer is still failing must not report accepted.
func (p *pendingMCPActuation) written() bool {
	<-p.writeDone
	return p.writeOK
}

func (p *pendingMCPActuation) complete(accepted bool) {
	p.resultOnce.Do(func() {
		p.result <- accepted
	})
}

type mcpActuations struct {
	mu      sync.Mutex
	pending map[string]*pendingMCPActuation
}

func (a *mcpActuations) register(id string) *pendingMCPActuation {
	pending := newPendingMCPActuation()
	a.mu.Lock()
	if a.pending == nil {
		a.pending = make(map[string]*pendingMCPActuation)
	}
	a.pending[id] = pending
	a.mu.Unlock()
	return pending
}

// claim removes an exact pending actuation before its subtype is interpreted. An
// unregistered id in the actuation namespace is still claimed: it is a duplicate or
// an ack whose waiter was canceled, failed, or retired at a child boundary.
func (a *mcpActuations) claim(id string) (*pendingMCPActuation, bool) {
	a.mu.Lock()
	pending := a.pending[id]
	delete(a.pending, id)
	a.mu.Unlock()
	if pending != nil {
		return pending, true
	}
	return nil, strings.HasPrefix(id, mcpActuationIDPrefix)
}

func (a *mcpActuations) remove(id string, pending *pendingMCPActuation) {
	a.mu.Lock()
	if a.pending[id] == pending {
		delete(a.pending, id)
	}
	a.mu.Unlock()
}

func (a *mcpActuations) failAll() {
	a.mu.Lock()
	pending := make([]*pendingMCPActuation, 0, len(a.pending))
	for id, actuation := range a.pending {
		pending = append(pending, actuation)
		delete(a.pending, id)
	}
	a.mu.Unlock()
	for _, actuation := range pending {
		actuation.complete(false)
	}
}

// emitContextUsage publishes a context reading only for an exact pending id minted
// by RequestContextUsage. The id is decoded and retired before subtype or payload,
// making every matched reply terminal even when its payload is unusable. This path
// logs nothing; emitModelList remains the sole control-response record owner.
func (p *Parser) emitContextUsage(line []byte) {
	var idLine contextUsageResponseIDLine
	if err := json.Unmarshal(line, &idLine); err != nil || idLine.Response.RequestID == "" {
		return
	}
	pending := p.contextUsageRequests.take(idLine.Response.RequestID)
	if pending == nil || !pending.written() {
		return
	}
	usage, ok := decodeContextUsage(line)
	if !ok {
		return
	}
	p.emit(usage)
}

// decodeContextUsage turns one top-level control_response into a bounded reading,
// reading no request id at all: correlation belongs to the caller, exactly as
// decodeMCPStatus splits it for the two MCP status consumers.
//
// ONE DECODER FOR BOTH LANES is the point rather than tidying. The shared sink reaches
// it through emitContextUsage and a waiting caller through claimContextUsageQuery, and
// a second decoder written for the second lane would bypass maxContextUsageStringBytes
// and boundContextUsageEntries while every existing bound test stayed green — handing
// that caller unbounded child-authored strings. Every rejection below is therefore a
// bound BOTH lanes inherit, not a courtesy to one of them.
//
// It has no logger and emits nothing; it returns false for every rejection.
func decodeContextUsage(line []byte) (turnevent.ContextUsage, bool) {
	var response contextUsageResponseLine
	if err := json.Unmarshal(line, &response); err != nil ||
		response.Response.Subtype != controlResponseSuccess || response.Response.Response == nil {
		return turnevent.ContextUsage{}, false
	}
	payload := response.Response.Response
	if len(payload.Model) > maxContextUsageStringBytes {
		return turnevent.ContextUsage{}, false
	}
	categories, dropped := boundContextUsageEntries(
		payload.Categories,
		func(category contextUsageCategoryLine) int { return category.Tokens },
		func(category *contextUsageCategoryLine) []*string { return []*string{&category.Name} },
	)
	eventCategories := make([]turnevent.ContextUsageCategory, len(categories))
	for i, category := range categories {
		eventCategories[i] = turnevent.ContextUsageCategory{Name: category.Name, Tokens: category.Tokens}
	}
	mcpTools, droppedMCPTools := boundContextUsageEntries(
		payload.MCPTools,
		func(tool contextUsageMCPToolLine) int { return tool.Tokens },
		func(tool *contextUsageMCPToolLine) []*string { return []*string{&tool.Name, &tool.ServerName} },
	)
	eventMCPTools := make([]turnevent.ContextUsageMCPTool, len(mcpTools))
	for i, tool := range mcpTools {
		eventMCPTools[i] = turnevent.ContextUsageMCPTool{
			Name: tool.Name, ServerName: tool.ServerName, Tokens: tool.Tokens,
		}
	}
	memoryFiles, droppedMemoryFiles := boundContextUsageEntries(
		payload.MemoryFiles,
		func(file contextUsageMemoryFileLine) int { return file.Tokens },
		func(file *contextUsageMemoryFileLine) []*string { return []*string{&file.Path, &file.Type} },
	)
	eventMemoryFiles := make([]turnevent.ContextUsageMemoryFile, len(memoryFiles))
	for i, file := range memoryFiles {
		eventMemoryFiles[i] = turnevent.ContextUsageMemoryFile{
			Path: file.Path, Type: file.Type, Tokens: file.Tokens,
		}
	}
	return turnevent.ContextUsage{
		Model:              strings.Clone(payload.Model),
		TotalTokens:        payload.TotalTokens,
		MaxTokens:          payload.MaxTokens,
		Percentage:         payload.Percentage,
		Categories:         eventCategories,
		DroppedCategories:  dropped,
		MCPTools:           eventMCPTools,
		DroppedMCPTools:    droppedMCPTools,
		MemoryFiles:        eventMemoryFiles,
		DroppedMemoryFiles: droppedMemoryFiles,
	}, true
}

// claimContextUsageQuery consumes a requester-private context reading before any
// shared control-response consumer sees it — including emitContextUsage, which is
// what keeps a claimed reading off the shared event sink and therefore out of the
// unsolicited context_usage lane the automatic post-turn ask publishes on.
//
// Matching the id retires the query before subtype and payload decoding, so the
// first response is terminal whatever it carries, exactly as claimMCPStatusQuery's
// own doc block states. Nothing here logs: a claim returns above emitModelList,
// which remains the sole control-response record owner.
func (p *Parser) claimContextUsageQuery(line []byte) bool {
	var idLine contextUsageResponseIDLine
	if err := json.Unmarshal(line, &idLine); err != nil || idLine.Response.RequestID == "" {
		return false
	}
	pending, claimed := p.contextUsageQueries.claim(idLine.Response.RequestID)
	if !claimed {
		return false
	}
	if pending == nil {
		return true
	}
	if !pending.written() {
		pending.complete(turnevent.ContextUsage{}, false)
		return true
	}
	pending.complete(decodeContextUsage(line))
	return true
}

// decodeAppliedSettings returns only bounded, independently owned model and effort
// values. Raw messages preserve missing-versus-null for effort; every other sibling
// in get_settings is absent from the decode types and therefore cannot escape.
func decodeAppliedSettings(line []byte) (AppliedSettings, bool) {
	var response appliedSettingsResponseLine
	if err := json.Unmarshal(line, &response); err != nil ||
		response.Response.Subtype != controlResponseSuccess ||
		response.Response.Response == nil ||
		len(response.Response.Response.Applied) == 0 {
		return AppliedSettings{}, false
	}

	var applied appliedSettingsLine
	if err := json.Unmarshal(response.Response.Response.Applied, &applied); err != nil ||
		len(applied.Model) == 0 || len(applied.Effort) == 0 {
		return AppliedSettings{}, false
	}
	var model string
	if err := json.Unmarshal(applied.Model, &model); err != nil ||
		model == "" || len(model) > maxModelResolved {
		return AppliedSettings{}, false
	}
	settings := AppliedSettings{Model: strings.Clone(model)}
	if bytes.Equal(bytes.TrimSpace(applied.Effort), []byte("null")) {
		return settings, true
	}
	var effort string
	if err := json.Unmarshal(applied.Effort, &effort); err != nil ||
		len(effort) > maxModelEffortLevel {
		return AppliedSettings{}, false
	}
	effort = strings.Clone(effort)
	settings.Effort = &effort
	return settings, true
}

// claimAppliedSettingsQuery consumes a requester-private get_settings reply before
// any shared control-response consumer sees it. Matching the id is terminal even when
// the subtype or payload is unusable; a later duplicate cannot revive the query.
func (p *Parser) claimAppliedSettingsQuery(line []byte) bool {
	var idLine contextUsageResponseIDLine
	if err := json.Unmarshal(line, &idLine); err != nil || idLine.Response.RequestID == "" {
		return false
	}
	pending, claimed := p.appliedSettingsQueries.claim(idLine.Response.RequestID)
	if !claimed {
		return false
	}
	if pending == nil {
		return true
	}
	if !pending.written() {
		pending.complete(AppliedSettings{}, false)
		return true
	}
	settings, ok := decodeAppliedSettings(line)
	pending.complete(settings, ok)
	return true
}

// decodeMCPStatus recognises a status report by shape rather than request
// correlation. Its input is the complete top-level control_response line selected
// by consumeLine; nested strings are never re-scanned into this function.
//
// It has no logger and returns false for every rejection or decode failure. That
// makes Parser.logControlResponse, called independently by emitModelList, the one
// content-free record owner for the line. The dedicated decode types also make
// request id, config, tools, raw response bytes and serverInfo.name unreachable.
func decodeMCPStatus(line []byte) (turnevent.MCPStatus, bool) {
	var response mcpStatusResponseLine
	if err := json.Unmarshal(line, &response); err != nil ||
		response.Response.Subtype != controlResponseSuccess {
		return turnevent.MCPStatus{}, false
	}

	var entries []mcpStatusServerLine
	if len(response.Response.Response.MCPServers) == 0 ||
		json.Unmarshal(response.Response.Response.MCPServers, &entries) != nil || entries == nil {
		// A present [] decodes to a non-nil empty slice and is emitted. Missing and
		// null both leave entries nil; any non-array shape fails the second decode.
		return turnevent.MCPStatus{}, false
	}

	dropped := 0
	if len(entries) > maxMCPStatusServers {
		dropped = len(entries) - maxMCPStatusServers
		entries = entries[:maxMCPStatusServers]
	}
	servers := make([]turnevent.MCPServerStatus, len(entries))
	for i, entry := range entries {
		errorText, _ := truncateField(entry.Error, maxMCPStatusError)
		servers[i] = turnevent.MCPServerStatus{
			Name:    entry.Name,
			Status:  entry.Status,
			Error:   errorText,
			Scope:   entry.Scope,
			Version: entry.ServerInfo.Version,
		}
	}
	return turnevent.MCPStatus{Servers: servers, DroppedServers: dropped}, true
}

// claimMCPStatusQuery consumes a requester-private MCP response before any
// shared control-response consumer sees it. Matching the id retires the query
// before subtype and payload decoding, so the first response is terminal.
func (p *Parser) claimMCPStatusQuery(line []byte) bool {
	var idLine contextUsageResponseIDLine
	if err := json.Unmarshal(line, &idLine); err != nil || idLine.Response.RequestID == "" {
		return false
	}
	pending, claimed := p.mcpStatusQueries.claim(idLine.Response.RequestID)
	if !claimed {
		return false
	}
	if pending == nil {
		return true
	}
	if !pending.written() {
		pending.complete(turnevent.MCPStatus{}, false)
		return true
	}
	status, ok := decodeMCPStatus(line)
	pending.complete(status, ok)
	return true
}

// claimMCPActuation consumes the ack to a daemon-private MCP reconnect, toggle or task stop
// before any shared control-response consumer sees it. Matching the id retires the
// actuation before the subtype is read, so the first ack is terminal whatever it says.
//
// ACCEPTANCE IS subtype == controlResponseSuccess AND NOTHING ELSE. Matching the id
// says only that the child answered THIS request; the observed `error` reply to
// reconnecting a server whose command does not exist (recorded in
// docs/knowledge/features/e2e-realclaude-mcp-status-capture-test-go.md under
// "Readiness belongs to the control replies") is what makes the distinction load-bearing
// rather than defensive. Every other value — an invented subtype, an absent one — takes
// the not-accepted arm, which is what makes the classification total.
//
// It decodes into controlAckLine, REUSED rather than re-declared, and that reuse is
// what makes this path's privacy structural: that target declares subtype and
// request_id and no payload key at all, so a server name and claude-authored error
// text are unreachable from here — not merely unlogged. Nothing on this path emits a
// record; logControlResponse belongs to emitModelList, which a claim returns ahead of.
func (p *Parser) claimMCPActuation(line []byte) bool {
	var ack controlAckLine
	if err := json.Unmarshal(line, &ack); err != nil || ack.Response.RequestID == "" {
		return false
	}
	pending, claimed := p.mcpActuations.claim(ack.Response.RequestID)
	if !claimed {
		return false
	}
	if pending == nil {
		return true
	}
	if !pending.written() {
		pending.complete(false)
		return true
	}
	pending.complete(ack.Response.Subtype == controlResponseSuccess)
	return true
}
