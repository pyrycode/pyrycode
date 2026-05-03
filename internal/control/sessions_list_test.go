package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
)

// setEvictedState sets the LifecycleState field of the given SessionInfo to
// the package-private stateEvicted value. The control package cannot name
// sessions.lifecycleState directly (unexported, by design — see lessons.md
// § "Wire enums: prefer self-documenting strings"), so reflect bridges the
// gap for tests that need to construct an evicted entry without spinning a
// real Pool. The stable contract is that lifecycleState's underlying kind
// is uint8 and stateEvicted == 1; both are pinned by Pool.List today and
// would have to change in lockstep with this helper.
func setEvictedState(t *testing.T, info *sessions.SessionInfo) {
	t.Helper()
	field := reflect.ValueOf(info).Elem().FieldByName("LifecycleState")
	if !field.IsValid() || field.Kind() != reflect.Uint8 {
		t.Fatalf("LifecycleState field unexpected: valid=%v kind=%v", field.IsValid(), field.Kind())
	}
	field.SetUint(1) // stateEvicted
}

// canonicaliseTime takes a time.Time through a JSON round-trip so the
// monotonic-clock component matches what a wire-decoded value carries.
// Required because encoding/json strips the monotonic component on
// MarshalJSON / UnmarshalJSON; comparing the original with the decoded
// value via reflect.DeepEqual would diverge even though time.Equal agrees.
// See lessons.md § "JSON roundtrip strips monotonic-clock state".
func canonicaliseTime(t *testing.T, ts time.Time) time.Time {
	t.Helper()
	b, err := json.Marshal(ts)
	if err != nil {
		t.Fatalf("Marshal time: %v", err)
	}
	var out time.Time
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal time: %v", err)
	}
	return out
}

// TestServer_SessionsList_Success exercises the success path: the fake
// Lister returns a snapshot, the client decodes it, and every entry's
// fields round-trip through the wire intact.
func TestServer_SessionsList_Success(t *testing.T) {
	t.Parallel()

	t1 := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 3, 9, 0, 0, 0, time.UTC)

	snapshot := []sessions.SessionInfo{
		{
			ID:           "11111111-1111-1111-1111-111111111111",
			Label:        "bootstrap",
			LastActiveAt: t1,
			Bootstrap:    true,
		},
		{
			ID:           "22222222-2222-2222-2222-222222222222",
			Label:        "minted",
			LastActiveAt: t2,
		},
	}
	sessioner := &fakeSessioner{listSnapshots: [][]sessions.SessionInfo{snapshot}}

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	got, err := SessionsList(context.Background(), sock)
	if err != nil {
		t.Fatalf("SessionsList: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2; got = %+v", len(got), got)
	}

	// Field-by-field assertions on entry 0 (bootstrap).
	if got[0].ID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("got[0].ID = %q, want bootstrap UUID", got[0].ID)
	}
	if got[0].Label != "bootstrap" {
		t.Errorf("got[0].Label = %q, want %q", got[0].Label, "bootstrap")
	}
	if got[0].State != "active" {
		t.Errorf("got[0].State = %q, want %q", got[0].State, "active")
	}
	if !got[0].LastActive.Equal(t1) {
		t.Errorf("got[0].LastActive = %v, want %v (time.Equal)", got[0].LastActive, t1)
	}
	if !got[0].Bootstrap {
		t.Errorf("got[0].Bootstrap = false, want true")
	}

	// Field-by-field assertions on entry 1 (minted, non-bootstrap).
	if got[1].Label != "minted" {
		t.Errorf("got[1].Label = %q, want %q", got[1].Label, "minted")
	}
	if got[1].State != "active" {
		t.Errorf("got[1].State = %q, want %q", got[1].State, "active")
	}
	if !got[1].LastActive.Equal(t2) {
		t.Errorf("got[1].LastActive = %v, want %v (time.Equal)", got[1].LastActive, t2)
	}
	if got[1].Bootstrap {
		t.Errorf("got[1].Bootstrap = true, want false")
	}

	if got := sessioner.recordedListCalls(); got != 1 {
		t.Errorf("List calls = %d, want exactly 1", got)
	}
}

// TestServer_SessionsList_PreservesPoolOrder verifies the seam does not
// re-sort. The fake's deliberately out-of-order snapshot must reach the
// client unchanged — final user-facing ordering is the CLI renderer's
// responsibility (61-B).
func TestServer_SessionsList_PreservesPoolOrder(t *testing.T) {
	t.Parallel()

	// Deliberately ascending LastActiveAt — opposite of Pool.List's
	// natural descending order.
	t1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)

	snapshot := []sessions.SessionInfo{
		{ID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Label: "a", LastActiveAt: t1},
		{ID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", Label: "b", LastActiveAt: t2},
		{ID: "cccccccc-cccc-cccc-cccc-cccccccccccc", Label: "c", LastActiveAt: t3},
	}
	sessioner := &fakeSessioner{listSnapshots: [][]sessions.SessionInfo{snapshot}}

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	got, err := SessionsList(context.Background(), sock)
	if err != nil {
		t.Fatalf("SessionsList: %v", err)
	}
	wantLabels := []string{"a", "b", "c"}
	if len(got) != len(wantLabels) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(wantLabels))
	}
	for i, want := range wantLabels {
		if got[i].Label != want {
			t.Errorf("got[%d].Label = %q, want %q (seam re-sorted?)", i, got[i].Label, want)
		}
	}
}

// TestServer_SessionsList_NilSessioner covers the nil-Sessioner branch —
// the diagnostic message follows the "sessions.list: " prefix convention.
func TestServer_SessionsList_NilSessioner(t *testing.T) {
	t.Parallel()

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, nil)
	defer stop()

	_, err := SessionsList(context.Background(), sock)
	if err == nil {
		t.Fatal("expected error when sessioner is nil")
	}
	const want = "sessions.list: no sessioner configured"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// TestServer_SessionsList_EmptyState verifies the empty-snapshot path:
// the wire payload is non-nil with a zero-length Sessions slice, and the
// client returns a non-nil empty slice (no error). Defensive even though
// Pool always carries the bootstrap entry.
func TestServer_SessionsList_EmptyState(t *testing.T) {
	t.Parallel()

	sessioner := &fakeSessioner{listSnapshots: [][]sessions.SessionInfo{{}}}

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	got, err := SessionsList(context.Background(), sock)
	if err != nil {
		t.Fatalf("SessionsList: %v", err)
	}
	if got == nil {
		t.Fatal("got = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0; got = %+v", len(got), got)
	}
}

// TestServer_SessionsList_StateEncoding pins the lifecycleState ↔ wire
// string mapping: stateActive → "active", stateEvicted → "evicted". Same
// encoding the on-disk registry uses; the seam reuses
// lifecycleState.String() rather than carrying a parallel translation.
func TestServer_SessionsList_StateEncoding(t *testing.T) {
	t.Parallel()

	active := sessions.SessionInfo{
		ID:    "11111111-1111-1111-1111-111111111111",
		Label: "live",
	}
	evicted := sessions.SessionInfo{
		ID:    "22222222-2222-2222-2222-222222222222",
		Label: "frozen",
	}
	setEvictedState(t, &evicted)

	snapshot := []sessions.SessionInfo{active, evicted}
	sessioner := &fakeSessioner{listSnapshots: [][]sessions.SessionInfo{snapshot}}

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	got, err := SessionsList(context.Background(), sock)
	if err != nil {
		t.Fatalf("SessionsList: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].State != "active" {
		t.Errorf("got[0].State = %q, want %q", got[0].State, "active")
	}
	if got[1].State != "evicted" {
		t.Errorf("got[1].State = %q, want %q", got[1].State, "evicted")
	}
}

// TestServer_SessionsList_LastActiveRoundTrip pins the time.Time ↔ JSON
// invariant: a pre-encode time on the seam side is compared against the
// post-decode time on the wire side via time.Equal (== / DeepEqual would
// diverge on the monotonic-clock component per lessons.md).
func TestServer_SessionsList_LastActiveRoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Now()
	snapshot := []sessions.SessionInfo{
		{ID: "11111111-1111-1111-1111-111111111111", LastActiveAt: now},
	}
	sessioner := &fakeSessioner{listSnapshots: [][]sessions.SessionInfo{snapshot}}

	sock, stop := startServerWithSessioner(t, &fakeResolver{sess: &fakeSession{}}, sessioner)
	defer stop()

	got, err := SessionsList(context.Background(), sock)
	if err != nil {
		t.Fatalf("SessionsList: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if !got[0].LastActive.Equal(now) {
		t.Errorf("LastActive = %v, want time.Equal to %v", got[0].LastActive, now)
	}

	// Sanity-pin the canonical-form helper itself: an explicit JSON
	// round-trip of `now` must compare time.Equal to `got[0].LastActive`.
	canonical := canonicaliseTime(t, now)
	if !got[0].LastActive.Equal(canonical) {
		t.Errorf("post-decode LastActive %v not time.Equal to canonical %v", got[0].LastActive, canonical)
	}
}

// TestProtocol_SessionInfo_BootstrapOmitempty pins the omitempty tag on
// SessionInfo.Bootstrap. The discriminator must elide for non-bootstrap
// entries (the common case) and emit "bootstrap":true for the one entry
// where it matters.
func TestProtocol_SessionInfo_BootstrapOmitempty(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		v        SessionInfo
		contains string
		excludes string
	}{
		{
			name:     "Bootstrap=false elides via omitempty",
			v:        SessionInfo{ID: "x", Label: "y", State: "active"},
			excludes: `"bootstrap"`,
		},
		{
			name:     "Bootstrap=true emits true",
			v:        SessionInfo{ID: "x", Label: "y", State: "active", Bootstrap: true},
			contains: `"bootstrap":true`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if tc.contains != "" && !bytes.Contains(got, []byte(tc.contains)) {
				t.Errorf("Marshal = %s, want it to contain %q", got, tc.contains)
			}
			if tc.excludes != "" && bytes.Contains(got, []byte(tc.excludes)) {
				t.Errorf("Marshal = %s, want it NOT to contain %q", got, tc.excludes)
			}
		})
	}
}

// TestSessionsList_WireRequest pins the wire shape of the client request.
// The verb carries no payload; SessionsPayload must elide entirely
// (omitempty on Request.Sessions). A v0.5.x-style daemon that doesn't
// know the verb would see a single-field "verb" object.
func TestSessionsList_WireRequest(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	gotLine := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 0, 256)
		tmp := make([]byte, 256)
		for {
			n, err := conn.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
				if bytes.IndexByte(buf, '\n') >= 0 {
					break
				}
			}
			if err != nil {
				break
			}
		}
		gotLine <- buf
		_ = json.NewEncoder(conn).Encode(Response{
			SessionsList: &SessionsListPayload{Sessions: []SessionInfo{}},
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := SessionsList(ctx, sock); err != nil {
		t.Fatalf("SessionsList: %v", err)
	}

	select {
	case line := <-gotLine:
		const want = `{"verb":"sessions.list"}` + "\n"
		if string(line) != want {
			t.Errorf("wire bytes = %q, want %q", line, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not receive a request")
	}
}

// TestSessionsList_DecodesEmptyResponseAsError pins the meaningful
// empty-response guard: a Response{} (no SessionsList, no Error) is a
// daemon contract violation and must surface a clean client error rather
// than a silent nil-slice success.
func TestSessionsList_DecodesEmptyResponseAsError(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req Request
		_ = json.NewDecoder(conn).Decode(&req)
		_ = json.NewEncoder(conn).Encode(Response{}) // no Error, no SessionsList
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = SessionsList(ctx, sock)
	if err == nil {
		t.Fatal("expected error from empty server response")
	}
	if !strings.Contains(err.Error(), "empty sessions.list response") {
		t.Errorf("err = %q, want it to mention \"empty sessions.list response\"", err.Error())
	}
}

// TestSessionsList_ServerError verifies that a server Response.Error
// surfaces verbatim through the client (no wrap, no prefix). Pool.List
// does not return errors today, but the nil-sessioner branch and any
// future error paths must round-trip cleanly.
func TestSessionsList_ServerError(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sock := filepath.Join(dir, "p.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req Request
		_ = json.NewDecoder(conn).Decode(&req)
		_ = json.NewEncoder(conn).Encode(Response{Error: "boom"})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = SessionsList(ctx, sock)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "boom" {
		t.Errorf("err = %q, want verbatim %q (no wrap)", err.Error(), "boom")
	}
	// Defensive: verify no stray sentinel match.
	if errors.Is(err, sessions.ErrSessionNotFound) {
		t.Error("errors.Is matched ErrSessionNotFound, want false (no typed sentinels on this verb)")
	}
}
