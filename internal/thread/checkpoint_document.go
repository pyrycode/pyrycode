package thread

import (
	"encoding/json"
	"time"
)

// checkpointSchema must change whenever the checkpoint document format changes.
const checkpointSchema = 1

// CheckpointState is the private continuation part of a reference fold checkpoint.
// ItemIDs is the complete creation-ordered manifest for the separate item records.
// Table references are one-based, with zero meaning nil. Item indexes are
// zero-based and retain negative sentinels. Importers must validate both parts.
type CheckpointState struct {
	Schema            int                                `json:"schema"`
	Rules             int                                `json:"rules"`
	ConversationID    string                             `json:"conversation_id"`
	Version           uint64                             `json:"version"`
	LastShown         uint64                             `json:"last_shown"`
	Suppressed        uint64                             `json:"suppressed"`
	UnprovenThrough   uint64                             `json:"unproven_through"`
	ItemIDs           []uint64                           `json:"item_ids"`
	Published         map[uint64]bool                    `json:"published"`
	Touches           map[uint64]uint64                  `json:"touches"`
	PublicVisible     map[uint64]bool                    `json:"public_visible"`
	Successor         checkpointSource                   `json:"successor"`
	LastBoundary      uint64                             `json:"last_boundary"`
	LegacyScope       uint64                             `json:"legacy_scope"`
	PendingBoundaries []checkpointBoundary               `json:"pending_boundaries"`
	CurrentTurns      []checkpointTurnRef                `json:"current_turns"`
	Openings          map[uint64]int                     `json:"openings"`
	OpenTexts         []int                              `json:"open_texts"`
	Turns             []checkpointTurn                   `json:"turns"`
	MainCalls         []checkpointMainCall               `json:"main_calls"`
	Outcomes          []checkpointOutcome                `json:"outcomes"`
	Accepted          map[uint64]int                     `json:"accepted"`
	Messages          map[uint64]int                     `json:"messages"`
	ChildTurns        []checkpointChildTurn              `json:"child_turns"`
	ChildCalls        []checkpointChildCall              `json:"child_calls"`
	ChildReports      []checkpointChildCall              `json:"child_reports"`
	Children          map[int]checkpointChild            `json:"children"`
	ChildEndings      map[int]checkpointEnding           `json:"child_endings"`
	Groups            []checkpointGroup                  `json:"groups"`
	AgentCalls        []checkpointAgentCall              `json:"agent_calls"`
	Reports           []checkpointReport                 `json:"reports"`
	Lifetimes         []checkpointLifetime               `json:"lifetimes"`
	Observations      map[uint64][]checkpointObservation `json:"observations"`
}

// CheckpointItem holds metadata and an unrendered recipe for every internal row,
// including unresolved children and suppressed deliveries. It owns its storage.
type CheckpointItem struct {
	Metadata checkpointMetadata `json:"metadata"`
	Recipe   checkpointRecipe   `json:"recipe"`
}

type checkpointMetadata struct {
	ID         uint64 `json:"id"`
	Order      uint64 `json:"order"`
	Rev        uint64 `json:"rev"`
	EndedOrder uint64 `json:"ended_order"`
	Kind       string `json:"kind"`
	Session    string `json:"session"`
	Agent      string `json:"agent"`
	NoChild    bool   `json:"no_child"`
	Turn       string `json:"turn"`
	Parent     uint64 `json:"parent"`
	Status     string `json:"status"`
	Active     bool   `json:"active"`
	Shown      bool   `json:"shown"`
	Summary    string `json:"summary"`
	Subtype    string `json:"subtype"`
}
type checkpointRecipe struct {
	Creating uint64                     `json:"creating"`
	Agent    bool                       `json:"agent"`
	Delivery uint64                     `json:"delivery"`
	Text     []uint64                   `json:"text"`
	Fields   map[string]checkpointField `json:"fields"`
}
type checkpointField struct {
	Entry    uint64          `json:"entry"`
	Item     uint64          `json:"item"`
	Revision uint64          `json:"revision"`
	Value    json.RawMessage `json:"value,omitempty"`
	Digest   string          `json:"digest"`
}
type checkpointSource struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
}
type checkpointKey struct {
	Source   checkpointSource `json:"source"`
	Tagged   bool             `json:"tagged"`
	Scope    uint64           `json:"scope"`
	Turn     string           `json:"turn"`
	Lifetime string           `json:"lifetime"`
}
type checkpointBoundary struct {
	At       time.Time `json:"at"`
	Previous string    `json:"previous"`
	Next     string    `json:"next"`
	Reason   string    `json:"reason"`
	IDs      []uint64  `json:"ids"`
}
type checkpointTurnRef struct {
	Key checkpointKey `json:"key"`
	Ref int           `json:"ref"`
}
type checkpointTurn struct {
	Key      checkpointKey               `json:"key"`
	Opening  uint64                      `json:"opening"`
	Text     int                         `json:"text"`
	Calls    map[string]int              `json:"calls"`
	Finished map[string]checkpointMarker `json:"finished"`
	End      int                         `json:"end"`
}
type checkpointMainCall struct {
	Shell    int           `json:"shell"`
	Group    checkpointKey `json:"group"`
	Item     int           `json:"item"`
	Launcher bool          `json:"launcher"`
	Terminal int           `json:"terminal"`
}
type checkpointMarker struct {
	Item     int    `json:"item"`
	Scope    uint64 `json:"scope"`
	Lifetime string `json:"lifetime"`
	Terminal int    `json:"terminal"`
}
type checkpointOutcome struct {
	ID      uint64 `json:"id"`
	Content uint64 `json:"content"`
	Status  string `json:"status"`
	Field   string `json:"field"`
}
type checkpointChildTurn struct {
	Group  checkpointKey `json:"group"`
	Parent string        `json:"parent"`
	Turn   string        `json:"turn"`
	Ref    int           `json:"ref"`
}
type checkpointChildCall struct {
	Group  checkpointKey `json:"group"`
	Turn   string        `json:"turn"`
	Call   string        `json:"call"`
	Parent string        `json:"parent"`
	Ref    int           `json:"ref"`
}
type checkpointChild struct {
	Group    checkpointKey       `json:"group"`
	Parent   string              `json:"parent"`
	Final    bool                `json:"final"`
	Snapshot *checkpointSnapshot `json:"snapshot"`
}
type checkpointSnapshot struct {
	Base     CheckpointItem `json:"base"`
	Previous CheckpointItem `json:"previous"`
	Restored bool           `json:"restored"`
}
type checkpointEnding struct {
	Item     uint64 `json:"item"`
	Revision uint64 `json:"revision"`
}
type checkpointGroup struct {
	Key   checkpointKey             `json:"key"`
	Calls map[string]int            `json:"calls"`
	Tasks map[string]checkpointTask `json:"tasks"`
}
type checkpointAgentCall struct {
	Ordinary     int               `json:"ordinary"`
	Linked       bool              `json:"linked"`
	Parent       string            `json:"parent"`
	Observed     uint64            `json:"observed"`
	Item         int               `json:"item"`
	Launch       uint64            `json:"launch"`
	LaunchResult bool              `json:"launch_result"`
	Reports      checkpointReports `json:"reports"`
}
type checkpointTask struct {
	Observed   uint64            `json:"observed"`
	LinkMapped bool              `json:"link_mapped"`
	Call       string            `json:"call"`
	Link       uint64            `json:"link"`
	LinkDigest string            `json:"link_digest"`
	Reports    checkpointReports `json:"reports"`
}
type checkpointReports struct {
	Pending []int  `json:"pending"`
	Markers [4]int `json:"markers"`
}
type checkpointReport struct {
	ID         uint64 `json:"id"`
	Content    uint64 `json:"content"`
	Turn       string `json:"turn"`
	Foreground bool   `json:"foreground"`
	Status     string `json:"status"`
	Field      string `json:"field"`
	Digest     string `json:"digest"`
	Durable    bool   `json:"durable"`
	Mapped     bool   `json:"mapped"`
}
type checkpointLifetime struct {
	Key      checkpointKey `json:"key"`
	Lifetime string        `json:"lifetime"`
}
type checkpointObservation struct {
	Group int    `json:"group"`
	Call  string `json:"call"`
	Task  string `json:"task"`
}
