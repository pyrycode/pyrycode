package history

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// cursorVersion tags the encoded form so a future shape can be told apart from
// this one rather than misread as it.
const cursorVersion = "1"

// cursorPos is a decoded cursor: the position of the OLDEST entry a page
// returned. The next page is everything strictly before it.
//
// It is deliberately neither an offset into one growing file nor a page number.
// Both of those move under a concurrent append; a (segment, byte offset) pair
// names a position in an append-only file that nothing ever rewrites, so it
// stays valid however much lands after it.
type cursorPos struct {
	segment uint64
	offset  int64
}

// mintCursor renders a position as the opaque string the client echoes back.
//
// docs/protocol-mobile.md's consumer (#2116) passes it through and never parses
// it, and #2113 declares it opaque on the wire, so the base64 wrapper is an
// opacity convention rather than a security primitive: it exists so no client is
// tempted to read the parts. '.' is a safe separator because conversations.ValidID
// admits nothing outside lowercase hex and '-'.
func mintCursor(convID conversations.ConversationID, segment uint64, offset int64) string {
	raw := fmt.Sprintf("%s.%s.%d.%d", cursorVersion, string(convID), segment, offset)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// parseCursor decodes and validates a cursor against the conversation being
// read. It is the ONLY place a cursor is ever validated: the wire declares it
// opaque and the handler above passes it through, so a forged one reaches here
// unexamined.
//
// The structural checks live here; whether the offset is a real entry boundary
// is checked in Page against the bytes actually on disk, because only there are
// the boundaries known. Nothing anywhere computes a position FROM the untrusted
// number — the eventring #2022 lesson, where a boundary inferred from an
// assumed id sequence wrapped at math.MaxUint64 and reached the wrong branch.
// Here math.MaxUint64 is simply not an entry boundary.
//
// Every refusal is one sentinel, on attachments.ErrNotFound's reasoning: the
// distinctions — well-formed but for another conversation, well-formed but
// naming a segment that does not exist — are exactly what a probe would want,
// so a consumer that cannot branch on them cannot leak them. And no refusal
// echoes the cursor: it is attacker-chosen text, so a message carrying it would
// put those bytes into whatever the consumer logs.
func parseCursor(cursor string, convID conversations.ConversationID) (cursorPos, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return cursorPos{}, cursorRefusal(convID, "not opaque-encoded")
	}
	fields := strings.Split(string(raw), ".")
	if len(fields) != 4 {
		return cursorPos{}, cursorRefusal(convID, "wrong field count")
	}
	if fields[0] != cursorVersion {
		return cursorPos{}, cursorRefusal(convID, "unknown cursor version")
	}
	// Shape first, then identity. The equality alone would be enough — an id
	// equal to one the caller already validated is itself valid — but checking
	// the shape keeps the refusal self-contained rather than depending on what
	// the caller happened to do first.
	if !conversations.ValidID(fields[1]) || fields[1] != string(convID) {
		return cursorPos{}, cursorRefusal(convID, "minted for a different conversation")
	}
	// ParseUint, explicitly unsigned: the segment number is formatted into a
	// fixed-width filename, and no sign may reach that.
	segment, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return cursorPos{}, cursorRefusal(convID, "segment is not a segment number")
	}
	// 63 bits, so the offset lands in an int64 as a non-negative value and the
	// comparison against a real entry boundary needs no range check of its own.
	offset, err := strconv.ParseUint(fields[3], 10, 63)
	if err != nil {
		return cursorPos{}, cursorRefusal(convID, "offset is not a byte offset")
	}
	return cursorPos{segment: segment, offset: int64(offset)}, nil
}

// cursorRefusal builds every cursor refusal. reason is for the operator and is
// always a fixed string chosen here; the conversation id is daemon-authored and
// canonical-shape-checked, so it carries no client text.
func cursorRefusal(convID conversations.ConversationID, reason string) error {
	return fmt.Errorf("%w: conversation %q: %s", ErrInvalidCursor, string(convID), reason)
}
