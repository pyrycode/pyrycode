package turnevent

// The four enums below carry exactly the ACP taxonomy values. They are
// string-backed so the values ARE the ACP strings, which keeps the model
// faithful and lets adapters marshal them directly. Layout mirrors
// internal/protocol/codes.go: grouped, doc-commented const blocks named
// <Type><Value>.

// ToolKind is the ACP tool-call kind taxonomy.
type ToolKind string

const (
	ToolKindRead    ToolKind = "read"
	ToolKindEdit    ToolKind = "edit"
	ToolKindDelete  ToolKind = "delete"
	ToolKindMove    ToolKind = "move"
	ToolKindSearch  ToolKind = "search"
	ToolKindExecute ToolKind = "execute"
	ToolKindThink   ToolKind = "think"
	ToolKindFetch   ToolKind = "fetch"
	ToolKindOther   ToolKind = "other"
)

// ToolStatus is the ACP tool-call status taxonomy.
type ToolStatus string

const (
	ToolStatusPending    ToolStatus = "pending"
	ToolStatusInProgress ToolStatus = "in_progress"
	ToolStatusCompleted  ToolStatus = "completed"
	ToolStatusFailed     ToolStatus = "failed"
)

// TurnEndReason is the ACP turn stop-reason taxonomy.
type TurnEndReason string

const (
	TurnEndReasonEndTurn         TurnEndReason = "end_turn"
	TurnEndReasonMaxTokens       TurnEndReason = "max_tokens"
	TurnEndReasonMaxTurnRequests TurnEndReason = "max_turn_requests"
	TurnEndReasonRefusal         TurnEndReason = "refusal"
	TurnEndReasonCancelled       TurnEndReason = "cancelled"
)

// PermissionOptionKind is the ACP session/request_permission option-kind
// taxonomy.
type PermissionOptionKind string

const (
	PermissionOptionKindAllowOnce    PermissionOptionKind = "allow_once"
	PermissionOptionKindAllowAlways  PermissionOptionKind = "allow_always"
	PermissionOptionKindRejectOnce   PermissionOptionKind = "reject_once"
	PermissionOptionKindRejectAlways PermissionOptionKind = "reject_always"
)

// Canonical value slices are the single source of truth that each Valid()
// scans and each exactness test asserts against — so slice/predicate/const
// drift is structurally impossible. They stay unexported (no consumer needs to
// enumerate yet; #607's wire mapping may add an exported accessor when it does).
var (
	toolKinds = []ToolKind{
		ToolKindRead, ToolKindEdit, ToolKindDelete, ToolKindMove,
		ToolKindSearch, ToolKindExecute, ToolKindThink, ToolKindFetch,
		ToolKindOther,
	}
	toolStatuses = []ToolStatus{
		ToolStatusPending, ToolStatusInProgress, ToolStatusCompleted,
		ToolStatusFailed,
	}
	turnEndReasons = []TurnEndReason{
		TurnEndReasonEndTurn, TurnEndReasonMaxTokens, TurnEndReasonMaxTurnRequests,
		TurnEndReasonRefusal, TurnEndReasonCancelled,
	}
	permissionOptionKinds = []PermissionOptionKind{
		PermissionOptionKindAllowOnce, PermissionOptionKindAllowAlways,
		PermissionOptionKindRejectOnce, PermissionOptionKindRejectAlways,
	}
)

// Valid reports whether k is one of the ACP tool kinds. The seam consumer
// (#608/#607) calls this to reject an out-of-taxonomy value.
func (k ToolKind) Valid() bool {
	for _, v := range toolKinds {
		if k == v {
			return true
		}
	}
	return false
}

// Valid reports whether s is one of the ACP tool statuses.
func (s ToolStatus) Valid() bool {
	for _, v := range toolStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// Valid reports whether r is one of the ACP turn-end reasons.
func (r TurnEndReason) Valid() bool {
	for _, v := range turnEndReasons {
		if r == v {
			return true
		}
	}
	return false
}

// Valid reports whether k is one of the ACP permission-option kinds.
func (k PermissionOptionKind) Valid() bool {
	for _, v := range permissionOptionKinds {
		if k == v {
			return true
		}
	}
	return false
}
