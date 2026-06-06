package agentrun

// IsNewLogicalTurn reports whether an assistant entry with message id
// currentID begins a new logical turn, given lastID (the previous assistant
// entry's id; "" before any assistant entry has been seen).
//
// claude serialises one logical reply as multiple consecutive assistant JSONL
// entries sharing a message.id (2.1.158 emits a thinking line, a tool_use line,
// and a text line for a single reply). A new turn begins only when the
// message.id changes, so a logical-turn count matches claude's native
// num_turns regardless of how a reply is chunked. An empty currentID is
// ungroupable (synthetic/malformed entries) → its own turn (the empty-id
// floor), preserving the pre-fix per-entry behaviour for id-less entries.
//
// This is the single source of truth shared by streamjson's num_turns
// reporting and budget's --max-turns enforcement, so the reported and enforced
// turn counts cannot drift on what a turn is. Transition-counting (comparing
// against the immediately-previous id) equals distinct-id-counting because
// claude completes one assistant message before starting the next — no A,B,A
// interleaving has been observed; if it ever is, this is the one place a
// seen-set would land.
func IsNewLogicalTurn(currentID, lastID string) bool {
	return currentID == "" || currentID != lastID
}
