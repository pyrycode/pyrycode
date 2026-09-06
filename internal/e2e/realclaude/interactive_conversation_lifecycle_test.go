//go:build e2e_realclaude

package realclaude

// TestInteractiveConversationLifecycle is the #1028 deliverable: a real-claude
// liveness gate for the conversation-management verbs Desktop and Mobile drive
// most often — create → rename → archive → unarchive → delete — proven against a
// freshly-spawned daemon running real `claude --model haiku` over the Noise v2
// wire. Before this, no conversation-management verb had ever executed against
// real claude: the fake tier owns the verbs' detailed shape
// (relay_v2_rename_test.go #974, relay_v2_delete_test.go #975,
// relay_v2_archive_test.go #976), but "fake-mock e2e is necessary but not
// sufficient" — this gate proves the real interactive stack executes the verbs
// at all, per the 2026-07-08 real-claude-e2e-in-a-pre-ship-gate policy.
//
// It reuses the #997 per-conversation harness verbatim (startPerConversationHarness,
// createConversationViaPhone, sealEnvelope, drainForReply) and the #854 turn
// drive/drain (sealSendMessage, drainForAssistantReply). The verb spine runs on
// ONE conversation over the SAME encrypted channel — request and reply strictly
// alternate, so the Noise receive nonce stays in lockstep (both drain helpers
// decrypt every noise_msg in arrival order to preserve that invariant).
//
// Each verb's effect is asserted from an operator-observable signal (AC #2): the
// daemon's reply envelope for that verb (renamed label; archived flag set then
// cleared; deleted id) and, for delete, the on-disk conversations registry no
// longer holding the row. Liveness (AC #3) is proven by streaming a non-empty
// assistant_delta for the conversation on the session the create bound to it;
// claude's words are never asserted (substrate-guard safe).
//
// Ordering rationale (do not reorder without cause). The metadata verbs
// (rename/archive/unarchive) run against a QUIESCENT wire, so their drains are
// fast and non-flaky: no assistant_delta stream is in flight, and since #2085 no
// claude is even running yet — the create binds the session but defers the child
// to the first message. The liveness send_message is inserted between unarchive
// and delete deliberately: only the delete drain then has to skip the tail of the
// in-flight liveness turn before it reaches conversation_deleted, isolating the
// "drain past a live stream" complexity to exactly one step. Running it after the
// archive round-trip proves the row those verbs rewrote is still routable — they
// touch only the registry is_archived flag, never CurrentSessionID. It no longer
// proves a RUNNING child survived them, because there is none to survive; see the
// note at the liveness step for why that witness is not bought back.
//
// Like #854/#997 this is a standing liveness gate in preship, not a
// deterministic RED/GREEN oracle for a specific bug — the fake tier owns the
// deterministic shape checks. Placement under the e2e_realclaude build tag wires
// it into `make e2e-realclaude` (and thus `make preship`) with no Makefile
// change; startPerConversationHarness skips cleanly when claude/creds are absent.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// metaVerbReplyBudget is the drain budget for the quiescent metadata verbs
// (rename/archive/unarchive): the registry op is instant, so the margin only
// covers any stray activation-time control frame the drain skips in order.
const metaVerbReplyBudget = 30 * time.Second

func TestInteractiveConversationLifecycle(t *testing.T) {
	h := startPerConversationHarness(t)
	// A per-run nonce seeds the rename label and the liveness message text so
	// reruns differ — enough to defeat any accidental caching — without asserting
	// on the nonce's echo.
	nonce := time.Now().UnixNano()

	// create — the handler mints + binds + persists the dedicated session before
	// replying, so convID is bound by the time the helper returns (helper asserts
	// the id is non-empty). Since #2085 that session has NO claude yet; the
	// liveness turn below is this conversation's first message and is what brings
	// the child up.
	convID := createConversationViaPhone(t, h.phone, h.initSend, h.initRecv, 2, nil)

	// rename — the reply's ConversationUpdatedPayload.Name reflects the new label.
	newName := fmt.Sprintf("renamed-%d", nonce)
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:      3,
		Type:    protocol.TypeRenameConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.RenameConversationPayload{ConversationID: convID, Name: newName}),
	})
	renamed := drainForReply(t, h.phone, h.initRecv, protocol.TypeConversationUpdated, 3, metaVerbReplyBudget)
	assertConversationUpdated(t, "rename", renamed, convID, func(p protocol.ConversationUpdatedPayload) {
		if p.Name == nil || *p.Name != newName {
			t.Errorf("rename reply Name = %v, want pointer to %q", p.Name, newName)
		}
	})

	// archive — the reply's IsArchived flips to true.
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:      4,
		Type:    protocol.TypeArchiveConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.ArchiveConversationPayload{ConversationID: convID}),
	})
	archived := drainForReply(t, h.phone, h.initRecv, protocol.TypeConversationUpdated, 4, metaVerbReplyBudget)
	assertConversationUpdated(t, "archive", archived, convID, func(p protocol.ConversationUpdatedPayload) {
		if !p.IsArchived {
			t.Errorf("archive reply IsArchived = false, want true")
		}
	})

	// unarchive — the reply's IsArchived clears back to false (same id-only payload).
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:      5,
		Type:    protocol.TypeUnarchiveConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.ArchiveConversationPayload{ConversationID: convID}),
	})
	unarchived := drainForReply(t, h.phone, h.initRecv, protocol.TypeConversationUpdated, 5, metaVerbReplyBudget)
	assertConversationUpdated(t, "unarchive", unarchived, convID, func(p protocol.ConversationUpdatedPayload) {
		if p.IsArchived {
			t.Errorf("unarchive reply IsArchived = true, want false")
		}
	})

	// liveness — a send_message turn on the conversation must stream a non-empty
	// assistant_delta (AC #3). Placed AFTER the archive round-trip, it shows the
	// row those three verbs rewrote is still routable end-to-end: the binding they
	// preserved resolves, and a turn on it reaches claude and streams back.
	//
	// Read the claim as exactly that, and no more. Until #2085 the session was
	// already running when archive landed, so this also witnessed a LIVE child
	// surviving the round-trip; now the child comes up on this very turn, and that
	// half is no longer proven here. It is not proven anywhere else either —
	// deliberately: archive/unarchive is a registry-metadata flip that never
	// reaches the pool (see TestRelayV2_Archive, which drives it against a seeded
	// row with no session at all), so there is no path by which it could tear a
	// child down, and buying the witness back would cost a second live claude turn
	// in preship. What this turn gains in exchange is a real-claude witness of the
	// deferred first-message spawn itself. perTurnReplyBudget matches #997's first
	// cold-turn budget and already covers that spawn.
	sealSendMessage(t, h.phone, h.initSend, 6, convID, "m-1",
		fmt.Sprintf("Reply with a single short word. run=%d", nonce))
	drainForAssistantReply(t, h.phone, h.initRecv, convID, 1, perTurnReplyBudget)

	// delete — the reply carries the deleted id and the on-disk registry no longer
	// holds the row. This drain must outlast the in-flight liveness turn's streamed
	// tail before conversation_deleted is reached, so it reuses perTurnReplyBudget;
	// the reply itself is instant.
	sealEnvelope(t, h.phone, h.initSend, protocol.Envelope{
		ID:      7,
		Type:    protocol.TypeDeleteConversation,
		TS:      time.Now().UTC(),
		Payload: mustJSON(t, protocol.DeleteConversationPayload{ConversationID: convID}),
	})
	deletedEnv := drainForReply(t, h.phone, h.initRecv, protocol.TypeConversationDeleted, 7, perTurnReplyBudget)
	var deleted protocol.ConversationDeletedPayload
	if err := json.Unmarshal(deletedEnv.Payload, &deleted); err != nil {
		t.Fatalf("decode conversation_deleted payload: %v", err)
	}
	if deleted.ID != convID {
		t.Errorf("delete reply ID = %q, want %q", deleted.ID, convID)
	}
	// The daemon eager-Saves before replying, so the row is gone by the time the
	// reply lands. This test creates exactly one conversation, so the registry is
	// now empty.
	if ids := readConversationIDsOnDisk(t, h.home); slices.Contains(ids, convID) {
		t.Errorf("on-disk conversations still contain %q after delete: %v", convID, ids)
	}
}

// assertConversationUpdated decodes a conversation_updated reply, asserts its
// InReplyTo/ID correlate to the expected request/conversation, and runs the
// verb-specific field check. Keeps the three metadata-verb steps to their single
// observable signal (Name / IsArchived); the fake tier owns the full
// field-preservation matrix.
func assertConversationUpdated(t *testing.T, verb string, env protocol.Envelope, convID string, check func(protocol.ConversationUpdatedPayload)) {
	t.Helper()
	var p protocol.ConversationUpdatedPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("decode %s conversation_updated payload: %v", verb, err)
	}
	if p.ID != convID {
		t.Errorf("%s reply ID = %q, want %q", verb, p.ID, convID)
	}
	check(p)
}

// readConversationIDsOnDisk reads the "test" instance registry
// (<home>/.pyry/test/conversations.json — the -pyry-name=test daemon) and
// returns the ids of every row. Returns nil if the file is absent or holds no
// rows. Mirrors the read-back shape in internal/e2e/relay_v2_delete_test.go.
func readConversationIDsOnDisk(t *testing.T, home string) []string {
	t.Helper()
	path := filepath.Join(home, ".pyry", "test", "conversations.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("read conversations.json: %v", err)
	}
	var onDisk struct {
		Conversations []struct {
			ID string `json:"id"`
		} `json:"conversations"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("decode on-disk registry: %v", err)
	}
	ids := make([]string, 0, len(onDisk.Conversations))
	for _, c := range onDisk.Conversations {
		ids = append(ids, c.ID)
	}
	return ids
}
