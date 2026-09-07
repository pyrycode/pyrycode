package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// wsLabelProducer is one conversation-frame producer under test: everything
// needed to drive it end to end and read back the bytes it put on the wire
// (#2210).
//
// The six entries in wsLabelProducers are every production site in this package
// that builds a ConversationUpdatedPayload or a ConversationCreatedPayload. The
// seventh producer, cmd/pyry's channel-new announce, is an unsolicited push in
// another package and carries its own test there — which is why AC #1's "from
// every producer" is only half-covered by this file.
type wsLabelProducer struct {
	name string
	// cwd is the workspace key the emitted frame's own Cwd will hold, and so the
	// key a label must be stored under to reach that frame. Deliberately
	// per-producer rather than one shared constant: change_workspace's is the
	// resolver's realpath, which is a different string from its seeded row's cwd.
	cwd string
	// emit builds a registry, hands it to seed (which may store labels), drives
	// the handler and returns the payload bytes of the single frame it replied
	// with. Raw bytes rather than a decoded struct because the null case below
	// cannot be judged from a decoded *string.
	emit func(t *testing.T, seed func(*conversations.Registry)) []byte
}

// wsLabelCreatedProducer names the one entry below whose frame is a
// conversation_created rather than a conversation_updated, so the two payload
// types are both known to be covered rather than assumed to be.
const wsLabelCreatedProducer = "create_conversation"

// wsLabelCreateCwd is the workspace create_conversation's client supplies and
// the handler stores verbatim.
const wsLabelCreateCwd = "/work/created-workspace"

func wsLabelProducers() []wsLabelProducer {
	return []wsLabelProducer{
		{
			name: "archive_conversation",
			cwd:  archiveConvCwd,
			emit: func(t *testing.T, seed func(*conversations.Registry)) []byte {
				t.Helper()
				reg, regPath := newArchiveConvReg(t, false)
				seed(reg)
				c, recv := newArchiveConvConn(t)
				req := archiveConvRequest(t, protocol.TypeArchiveConversation,
					protocol.ArchiveConversationPayload{ConversationID: archiveConvTargetID})
				h := ArchiveConversation(reg, regPath, testLogger(t), true)
				if err := h(context.Background(), c, req); err != nil {
					t.Fatalf("handler: %v", err)
				}
				return assertArchiveConvEnvelopeShape(t, recv(), protocol.TypeConversationUpdated).Payload
			},
		},
		{
			// The unarchive wiring of the same function: ArchiveConversation is
			// one producer wired twice, and both wirings reach the same literal.
			name: "unarchive_conversation",
			cwd:  archiveConvCwd,
			emit: func(t *testing.T, seed func(*conversations.Registry)) []byte {
				t.Helper()
				reg, regPath := newArchiveConvReg(t, true)
				seed(reg)
				c, recv := newArchiveConvConn(t)
				req := archiveConvRequest(t, protocol.TypeUnarchiveConversation,
					protocol.ArchiveConversationPayload{ConversationID: archiveConvTargetID})
				h := ArchiveConversation(reg, regPath, testLogger(t), false)
				if err := h(context.Background(), c, req); err != nil {
					t.Fatalf("handler: %v", err)
				}
				return assertArchiveConvEnvelopeShape(t, recv(), protocol.TypeConversationUpdated).Payload
			},
		},
		{
			// Keyed on the resolver's realpath, NOT on the seeded row's cwd and
			// NOT on the request path. TestChangeWorkspace_ReportsDestinationLabel
			// is what proves that distinction actually holds.
			name: "change_workspace",
			cwd:  changeWSNewCwd,
			emit: func(t *testing.T, seed func(*conversations.Registry)) []byte {
				t.Helper()
				reg, regPath := newChangeWSReg(t)
				seed(reg)
				c, recv := newChangeWSConn(t)
				req := changeWSRequest(t, protocol.ChangeWorkspacePayload{
					ConversationID: changeWSTargetID,
					Cwd:            changeWSRequestPath,
				})
				h := ChangeWorkspace(reg, acceptResolver, regPath, testLogger(t))
				if err := h(context.Background(), c, req); err != nil {
					t.Fatalf("handler: %v", err)
				}
				return assertChangeWSEnvelopeShape(t, recv(), protocol.TypeConversationUpdated).Payload
			},
		},
		{
			name: "promote_conversation",
			cwd:  promoteConvSeedCwd,
			emit: func(t *testing.T, seed func(*conversations.Registry)) []byte {
				t.Helper()
				reg, regPath := newPromoteConvReg(t)
				seed(reg)
				c, recv := newPromoteConvConn(t)
				req := promoteConvRequest(t, protocol.PromoteConversationPayload{
					ConversationID: promoteConvTargetID,
					Name:           "ws-label-promoted",
					Cwd:            promoteConvSeedCwd,
				})
				h := PromoteConversation(reg, regPath, testLogger(t))
				if err := h(context.Background(), c, req); err != nil {
					t.Fatalf("handler: %v", err)
				}
				return assertPromoteConvEnvelopeShape(t, recv(), protocol.TypeConversationUpdated).Payload
			},
		},
		{
			name: "rename_conversation",
			cwd:  renameConvCwd,
			emit: func(t *testing.T, seed func(*conversations.Registry)) []byte {
				t.Helper()
				reg, regPath := newRenameConvReg(t)
				seed(reg)
				c, recv := newRenameConvConn(t)
				req := renameConvRequest(t, protocol.RenameConversationPayload{
					ConversationID: renameConvTargetID,
					Name:           "ws-label-renamed",
				})
				h := RenameConversation(reg, regPath, testLogger(t))
				if err := h(context.Background(), c, req); err != nil {
					t.Fatalf("handler: %v", err)
				}
				return assertRenameConvEnvelopeShape(t, recv(), protocol.TypeConversationUpdated).Payload
			},
		},
		{
			name: "set_system_prompt",
			cwd:  sspCwd,
			emit: func(t *testing.T, seed func(*conversations.Registry)) []byte {
				t.Helper()
				reg, regPath := newSSPReg(t)
				seed(reg)
				c, recv := newSSPConn(t)
				prompt := sspPrompt
				req := sspRequest(t, protocol.SetSystemPromptPayload{
					ConversationID: sspTargetID,
					SystemPrompt:   &prompt,
				})
				h := SetSystemPrompt(reg, regPath, testLogger(t))
				if err := h(context.Background(), c, req); err != nil {
					t.Fatalf("handler: %v", err)
				}
				return assertSSPEnvelopeShape(t, recv(), protocol.TypeConversationUpdated).Payload
			},
		},
		{
			name: wsLabelCreatedProducer,
			cwd:  wsLabelCreateCwd,
			emit: func(t *testing.T, seed func(*conversations.Registry)) []byte {
				t.Helper()
				reg, regPath := newCreateConvReg(t)
				seed(reg)
				c, recv := newCreateConvConn(t)
				cwd := wsLabelCreateCwd
				req := createConvRequest(t, protocol.CreateConversationPayload{Cwd: &cwd})
				h := CreateConversation(reg, &stubSessionCreator{}, regPath, createConvDefault, testLogger(t))
				if err := h(context.Background(), c, req); err != nil {
					t.Fatalf("handler: %v", err)
				}
				return assertCreateConvEnvelopeShape(t, recv(), protocol.TypeConversationCreated).Payload
			},
		},
	}
}

// TestConversationFrames_CarryWorkspaceLabel covers AC #1 for the six producers
// that live in this package: every conversation_updated and every
// conversation_created carries the label stored for that frame's OWN cwd.
//
// The assertion pairs a decoded read with a raw-bytes read on purpose. The
// decoded half proves the value is reachable through the published struct; the
// raw half proves it is spelled workspace_label on the wire, which no decode
// into a Go field can establish once the tag is written.
func TestConversationFrames_CarryWorkspaceLabel(t *testing.T) {
	t.Parallel()
	for _, p := range wsLabelProducers() {
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			label := "Label for " + p.name
			payload := p.emit(t, func(reg *conversations.Registry) {
				reg.SetWorkspaceLabel(p.cwd, &label)
			})

			wantKey := `"workspace_label":` + string(mustJSON(t, label))
			if !strings.Contains(string(payload), wantKey) {
				t.Errorf("frame does not carry %s:\n%s", wantKey, payload)
			}
			if got := decodeFrameWorkspaceLabel(t, payload); got == nil || *got != label {
				t.Errorf("decoded workspace_label = %v, want pointer to %q", got, label)
			}
		})
	}
}

// TestConversationFrames_UnlabelledWorkspaceCarriesExplicitNull covers AC #2:
// on a registry where no label has ever been set, every frame carries an
// explicit null rather than omitting the key.
//
// Judged on RAW BYTES, which is the only thing that can judge it. A decoded
// *string is nil both when the wire said null and when the key was absent
// altogether, so a struct-level assertion here would pass against a payload
// tagged omitempty — exactly the regression this test exists to catch.
func TestConversationFrames_UnlabelledWorkspaceCarriesExplicitNull(t *testing.T) {
	t.Parallel()
	for _, p := range wsLabelProducers() {
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			payload := p.emit(t, func(*conversations.Registry) {})

			const wantKey = `"workspace_label":null`
			if !strings.Contains(string(payload), wantKey) {
				t.Errorf("frame does not carry %s — an unlabelled workspace must send an "+
					"explicit null, never an omitted key:\n%s", wantKey, payload)
			}
		})
	}
}

// TestConversationFrames_LabelIsPerWorkspaceNotPerConversation pins that the
// lookup keys off the frame's own cwd: a label stored for some OTHER workspace
// never reaches a frame whose conversation lives elsewhere. Without this, a
// projection that ignored the key and returned any stored label would pass the
// two tests above.
func TestConversationFrames_LabelIsPerWorkspaceNotPerConversation(t *testing.T) {
	t.Parallel()
	for _, p := range wsLabelProducers() {
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			elsewhere := "Label of a workspace this conversation is not in"
			payload := p.emit(t, func(reg *conversations.Registry) {
				reg.SetWorkspaceLabel(p.cwd+"-somewhere-else", &elsewhere)
			})

			const wantKey = `"workspace_label":null`
			if !strings.Contains(string(payload), wantKey) {
				t.Errorf("frame does not carry %s — a label stored for another workspace "+
					"must not reach this frame:\n%s", wantKey, payload)
			}
		})
	}
}

// TestConversationFrames_StoredEmptyLabelIsNotNull pins that presence comes from
// the accessor's second return and never from label != "". The registry keeps a
// stored empty label ("", true) distinct from an absent one ("", false); the
// comparison collapses them and would send null for a label that is genuinely
// stored. No wire path can store an empty label today — rename_workspace refuses
// a blank — but the registry API can, and list_conversations already pins the
// same rule on the read side.
func TestConversationFrames_StoredEmptyLabelIsNotNull(t *testing.T) {
	t.Parallel()
	for _, p := range wsLabelProducers() {
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			empty := ""
			payload := p.emit(t, func(reg *conversations.Registry) {
				reg.SetWorkspaceLabel(p.cwd, &empty)
			})

			const wantKey = `"workspace_label":""`
			if !strings.Contains(string(payload), wantKey) {
				t.Errorf("frame does not carry %s — a stored empty label must stay distinct "+
					"from an absent one:\n%s", wantKey, payload)
			}
		})
	}
}

// TestChangeWorkspace_ReportsDestinationLabel covers AC #3: a conversation moved
// by change_workspace reports the DESTINATION workspace's label, not the one it
// left, when both carry distinct stored labels.
//
// Three labels are stored, and the two decoys are what make the case
// discriminating. The origin label catches a projection that read the label
// before the move. The request-path label catches a projection keyed off the
// client's raw p.Cwd instead of the resolver's stored realpath — the bug that an
// identity-resolver test cannot see, and acceptResolver is deliberately not one:
// it maps changeWSRequestPath to the different string changeWSNewCwd.
func TestChangeWorkspace_ReportsDestinationLabel(t *testing.T) {
	t.Parallel()
	reg, regPath := newChangeWSReg(t)

	origin := "Origin workspace — the one it left"
	requested := "Keyed off the raw request path — never correct"
	destination := "Destination workspace — the only right answer"
	reg.SetWorkspaceLabel(changeWSOldCwd, &origin)
	reg.SetWorkspaceLabel(changeWSRequestPath, &requested)
	reg.SetWorkspaceLabel(changeWSNewCwd, &destination)

	c, recv := newChangeWSConn(t)
	req := changeWSRequest(t, protocol.ChangeWorkspacePayload{
		ConversationID: changeWSTargetID,
		Cwd:            changeWSRequestPath,
	})

	h := ChangeWorkspace(reg, acceptResolver, regPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertChangeWSEnvelopeShape(t, recv(), protocol.TypeConversationUpdated)
	var payload protocol.ConversationUpdatedPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal conversation_updated payload: %v", err)
	}

	// The frame's Cwd and its label must name the same workspace: the label is
	// only meaningful as the name OF the cwd on this very frame.
	if payload.Cwd != changeWSNewCwd {
		t.Fatalf("reply Cwd = %q, want the resolver's realpath %q", payload.Cwd, changeWSNewCwd)
	}
	if payload.WorkspaceLabel == nil {
		t.Fatalf("reply workspace_label = null, want pointer to %q", destination)
	}
	switch *payload.WorkspaceLabel {
	case destination:
		// correct
	case origin:
		t.Errorf("reply workspace_label = %q — the label was read before the move, so the "+
			"frame names the workspace the conversation LEFT", origin)
	case requested:
		t.Errorf("reply workspace_label = %q — the lookup keyed off the client's raw request "+
			"path instead of the resolver's stored realpath %q", requested, changeWSNewCwd)
	default:
		t.Errorf("reply workspace_label = %q, want %q", *payload.WorkspaceLabel, destination)
	}
}

// decodeFrameWorkspaceLabel reads the workspace_label key out of a frame's
// payload without committing to either payload struct, so one helper serves both
// conversation_updated and conversation_created.
func decodeFrameWorkspaceLabel(t *testing.T, payload []byte) *string {
	t.Helper()
	var decoded struct {
		WorkspaceLabel *string `json:"workspace_label"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal frame payload: %v", err)
	}
	return decoded.WorkspaceLabel
}

// mustJSON encodes v the way the handler will, so a label containing characters
// encoding/json escapes is compared against the escaped form rather than against
// the Go literal.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %v: %v", v, err)
	}
	return b
}
