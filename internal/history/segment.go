package history

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// The segment header, written as the first line of every segment file. It is a
// JSON object rather than a magic string so a future format revision is a field
// change instead of a format change, and it is a hand-written literal rather
// than a marshalled value so its bytes and its LENGTH are both stable —
// a cursor's offset is a byte position in this file, so a header that changed
// width would silently move every entry boundary.
//
// TestSegmentHeaderLiteralMatchesItsStruct is what keeps the literal and
// segmentHeader in agreement.
const (
	segmentFormat     = "pyrycode.history"
	segmentVersion    = 1
	segmentHeaderLine = `{"format":"pyrycode.history","version":1}` + "\n"
)

// segmentHeader is the decoded form of the line above. A first line that fails
// to decode into it, or that decodes with any other format or version, is
// ErrUnknownVersion — this build then refuses the segment whole rather than
// reading it as the version it happens to know.
type segmentHeader struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
}

const (
	segmentPrefix = "segment-"
	segmentSuffix = ".jsonl"
	// Twenty digits hold any uint64, so zero-padding to that width makes
	// lexicographic order equal numeric order. That is what lets the backward
	// walk take os.ReadDir's already-sorted answer without re-sorting it.
	segmentDigits = 20
)

func segmentName(n uint64) string {
	return fmt.Sprintf("%s%0*d%s", segmentPrefix, segmentDigits, n, segmentSuffix)
}

// parseSegmentName is segmentName's inverse, and the filter that decides what
// counts as a segment. It accepts only the exact width segmentName produces, so
// a leftover temp file, a hand-dropped note or a differently padded name is not
// mistaken for part of the log.
func parseSegmentName(name string) (uint64, bool) {
	digits, ok := strings.CutPrefix(name, segmentPrefix)
	if !ok {
		return 0, false
	}
	digits, ok = strings.CutSuffix(digits, segmentSuffix)
	if !ok || len(digits) != segmentDigits {
		return 0, false
	}
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// segmentCeiling is the largest a well-formed segment can be: the header, plus
// the bound the appender rolls at, plus the one entry that may cross it (the
// roll is checked BEFORE the write, and Append separately refuses an entry line
// longer than the bound). A file past this ceiling was not written by this
// package, so the reader refuses it rather than allocating it.
func segmentCeiling(maxSegmentBytes int64) int64 {
	return int64(len(segmentHeaderLine)) + 2*maxSegmentBytes
}

// segEntry is one decoded entry together with the byte offset its line begins
// at. The offset is the only thing a cursor names inside a segment, and it is
// always read back off the bytes rather than computed from an entry count —
// nothing here does arithmetic on a position an untrusted cursor supplied.
type segEntry struct {
	entry  Entry
	offset int64
}

// encodeEntry renders one entry as a single JSON line, newline included.
//
// The encoder does the terminating newline, so the JSON-lines invariant comes
// from encoding/json rather than from string concatenation, and HTML escaping
// is off so a payload carrying '<', '>' or '&' round-trips as the producer
// wrote it. A raw newline cannot appear inside the line because Append has
// already established the payload is valid JSON, which escapes them.
func encodeEntry(e Entry) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		// Unreachable for a payload json.Valid has accepted. The message
		// deliberately carries nothing from the entry: payload bytes are
		// conversation content and this is the one place they may be written.
		return nil, fmt.Errorf("%w: entry could not be encoded", ErrInvalidPayload)
	}
	return buf.Bytes(), nil
}

// decodeSegment decodes a whole segment: the version header first, then one
// entry per line. It returns no entries at all on an unrecognised header, which
// is what "decodes no entries from that segment, rather than parsing it as the
// version it does know" means.
//
// A final line with no terminating newline is ErrCorruptSegment rather than a
// silent skip. It is unreachable while a process lives — writes are serialised
// under the store's mutex and each is one write call — so it can only be the
// tail of a machine crash, which is out of scope for this package's durability
// promise. Surfacing it beats dropping data or appending after it.
//
// No error names a line's content. The payload is conversation content, so
// refusals carry the entry's ORDINAL and nothing else.
func decodeSegment(data []byte) ([]segEntry, error) {
	// A zero-length file makes no version claim at all, so it is not a version
	// this build fails to recognise — it is a segment created and never written,
	// which writeSegment produces when its first write loses and its cleanup
	// cannot run. It holds no entry, and saying so lets a reader walk past it to
	// the segments that do rather than refusing the whole conversation. A file
	// with bytes but no newline is a different thing and stays a version
	// refusal: something wrote it, and this build cannot say what.
	if len(data) == 0 {
		return nil, nil
	}
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		return nil, fmt.Errorf("%w: segment carries no header line", ErrUnknownVersion)
	}
	var h segmentHeader
	if err := json.Unmarshal(data[:nl], &h); err != nil || h.Format != segmentFormat || h.Version != segmentVersion {
		return nil, fmt.Errorf("%w: segment is not %s v%d", ErrUnknownVersion, segmentFormat, segmentVersion)
	}

	var out []segEntry
	offset := int64(nl + 1)
	for rest := data[nl+1:]; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			return nil, fmt.Errorf("%w: entry %d is unterminated", ErrCorruptSegment, len(out)+1)
		}
		var e Entry
		if err := json.Unmarshal(rest[:i], &e); err != nil {
			return nil, fmt.Errorf("%w: entry %d does not decode", ErrCorruptSegment, len(out)+1)
		}
		out = append(out, segEntry{entry: e, offset: offset})
		offset += int64(i + 1)
		rest = rest[i+1:]
	}
	return out, nil
}

// segmentRef is one segment of one conversation, in ascending number order.
type segmentRef struct {
	num  uint64
	name string
}

// listSegments returns the conversation's segments in ascending order. It is
// the index: segment boundaries ARE the index, so no sidecar file exists and
// nothing has to be kept in step with the directory.
//
// A listing is used rather than probing segment-1, segment-2 … upward
// deliberately. Retention is left open by design, and the moment a policy
// deletes old segments the numbering stops being contiguous — a probe would
// then stop at the first hole and silently report a shorter log. A listing
// tolerates gaps by construction.
//
// Only regular files are eligible, which is ResolvePath's leaf discipline: a
// symlink parked in the history directory is stepped over rather than followed.
// Containment of the DIRECTORY is not containment of what it holds.
func listSegments(dir string) ([]segmentRef, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("history: read log directory %q: %w", dir, err)
	}
	// os.ReadDir answers sorted by filename, and segmentName's fixed width
	// makes that numeric order, so the result needs no further sort.
	var out []segmentRef
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		n, ok := parseSegmentName(e.Name())
		if !ok {
			continue
		}
		out = append(out, segmentRef{num: n, name: e.Name()})
	}
	return out, nil
}
