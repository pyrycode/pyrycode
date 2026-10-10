package apps

// Lifecycle replaces the committed display and lifecycle fields as one change.
// Empty releases and a nil LastError mean unset. Callers supply confirmed observed
// state and static safe errors, never raw parser, process or I/O diagnostics.
type Lifecycle struct {
	Title          string
	Desired        string
	State          string
	ActiveRelease  string
	PendingRelease string
	LastError      *LastError
}

type lifecycleUpdate struct {
	serverID string
	fields   Lifecycle
}

// UpdateLifecycle commits confirmed observations for an existing local identity.
// A cutover supplies its new title/release, running state and unset pending/error
// together. The registry neither confirms health nor activates manifest candidates.
func (r *Registry) UpdateLifecycle(serverID, appID string, fields Lifecycle) (bool, error) {
	return r.change("lifecycle", appID, nil, &lifecycleUpdate{serverID: serverID, fields: fields})
}

func applyLifecycle(record *Record, fields Lifecycle) {
	record.Title, record.Desired, record.State = fields.Title, fields.Desired, fields.State
	record.ActiveRelease, record.PendingRelease = fields.ActiveRelease, fields.PendingRelease
	record.LastError = fields.LastError
	*record = cloneRecord(*record)
}

func cloneRecord(record Record) Record {
	if record.LastError != nil {
		copy := *record.LastError
		record.LastError = &copy
	}
	return record
}

// Change is a current-process committed update. Record is detached and complete;
// nil denotes an identity/revision tombstone. No prior changes are replayed.
type Change struct {
	ServerID string
	AppID    string
	Revision uint64
	Record   *Record
}

// Subscribe delivers future changes synchronously in increasing revision order.
// Each consumer owns its delivered data. Callbacks must return promptly and must
// not call registry methods, including the returned idempotent unsubscribe.
// A nil consumer installs nothing. Subscriptions are neither stored nor replayed.
func (r *Registry) Subscribe(consumer func(Change)) func() {
	if consumer == nil {
		return func() {}
	}
	r.mu.Lock()
	if r.consumers == nil {
		r.consumers = make(map[*func(Change)]struct{})
	}
	r.consumers[&consumer] = struct{}{}
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		delete(r.consumers, &consumer)
		r.mu.Unlock()
	}
}

// notify runs under the registry lock after durable commit and snapshot swap.
func (r *Registry) notify(appID string) {
	change := Change{ServerID: r.snapshot.ServerID, AppID: appID, Revision: r.snapshot.Revision}
	for _, record := range r.snapshot.Records {
		if record.AppID == appID {
			change.Record = &record
			break
		}
	}
	for consumer := range r.consumers {
		detached := change
		if change.Record != nil {
			copy := cloneRecord(*change.Record)
			detached.Record = &copy
		}
		(*consumer)(detached)
	}
}

func normalizedState(record Record) string {
	if record.Desired == "available" && record.ActiveRelease != "" {
		return "starting"
	}
	return "stopped"
}

func normalizeSnapshot(root string, s *snapshot, persist func(string, snapshot) error) error {
	var count uint64
	for _, record := range s.Records {
		if record.State != normalizedState(record) {
			count++
		}
	}
	if count == 0 {
		return nil
	}
	if count > maxRevision-s.Revision {
		return ErrExhausted
	}
	next := cloneSnapshot(*s)
	for i := range next.Records {
		record := &next.Records[i]
		if state := normalizedState(*record); record.State != state {
			next.Revision++
			record.State, record.Revision = state, next.Revision
		}
	}
	if e := persist(root, next); e != nil {
		return e
	}
	*s = next
	return nil
}
