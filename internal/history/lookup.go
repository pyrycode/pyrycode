package history

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// ErrInvalidEntryIDs means requested IDs are not strictly ascending and unique.
var ErrInvalidEntryIDs = errors.New("history: entry IDs must be ascending and unique")

// MissingEntriesError identifies every requested ID absent from the log.
// Lookup returns no entries alongside this error.
type MissingEntriesError struct{ IDs []uint64 }

func (e *MissingEntriesError) Error() string {
	return fmt.Sprintf("history: missing entry IDs %v", e.IDs)
}

type lookupBound struct {
	firstID    uint64
	size       int64
	discovered bool // Prefix discovery certified the first-ID bound.
}

func (c *convLog) rememberLookupBound(segment uint64, size int64, first uint64) {
	if c.lookupBounds == nil {
		c.lookupBounds = make(map[uint64]lookupBound)
	}
	c.lookupBounds[segment] = lookupBound{firstID: first, size: size}
}

// lookupSegmentBound refreshes unknown or resized bounds after leaf admission.
// Callers hold Store.mu and resolve the containing directory on every call.
func (s *Store) lookupSegmentBound(c *convLog, dir string, seg segmentRef) (lookupBound, error) {
	path := filepath.Join(dir, seg.name)
	info, err := os.Lstat(path)
	if err != nil {
		return lookupBound{}, fmt.Errorf("history: stat lookup segment: %w", err)
	}
	if !info.Mode().IsRegular() {
		return lookupBound{}, fmt.Errorf("%w: lookup segment is not regular", ErrCorruptSegment)
	}
	bound, known := c.lookupBounds[seg.num]
	// Recovery can remember zero from an invalid first entry. Only discovery
	// certifies that a larger-than-header zero bound really contains no entry.
	if !known || bound.size != info.Size() || (bound.firstID == 0 && !bound.discovered && info.Size() > int64(len(segmentHeaderLine))) {
		first, err := s.segmentFirstID(path)
		if err != nil {
			return lookupBound{}, err
		}
		c.rememberLookupBound(seg.num, info.Size(), first)
		bound = c.lookupBounds[seg.num]
		bound.discovered = true
		c.lookupBounds[seg.num] = bound
	}
	return bound, nil
}

// Lookup returns exact stored entries in requested order, with detached payloads
// and metadata. IDs must be ascending and unique; zero is missing. Any error
// returns no partial result. An absent log creates nothing. Like Append, convID
// must come from the authenticated session, never from a client assertion.
// Containment and regular-file membership are rechecked even for cached entries.
func (s *Store) Lookup(convID conversations.ConversationID, ids []uint64) ([]Entry, error) {
	if !conversations.ValidID(string(convID)) {
		return nil, fmt.Errorf("%w: conversation id %q", ErrInvalidID, string(convID))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			return nil, ErrInvalidEntryIDs
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.resolveDir(convID, false)
	if errors.Is(err, fs.ErrNotExist) {
		if len(ids) == 0 {
			return []Entry{}, nil
		}
		return nil, &MissingEntriesError{IDs: append([]uint64(nil), ids...)}
	}
	if err != nil {
		return nil, err
	}
	segs, err := listSegments(dir)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []Entry{}, nil
	}
	c := s.convs[convID]
	if c == nil {
		c = &convLog{}
		s.convs[convID] = c
	}
	var ranges []segmentRef
	current := make(map[uint64]bool, len(segs))
	var previous uint64
	for _, seg := range segs {
		bound, err := s.lookupSegmentBound(c, dir, seg)
		if err != nil {
			return nil, err
		}
		current[seg.num] = true
		if bound.firstID == 0 {
			continue
		}
		if bound.firstID <= previous {
			return nil, fmt.Errorf("%w: non-increasing segment first IDs", ErrCorruptSegment)
		}
		previous = bound.firstID
		ranges = append(ranges, seg)
	}
	for num := range c.lookupBounds {
		if !current[num] {
			delete(c.lookupBounds, num)
		}
	}
	var out []Entry
	var missing []uint64
	for pos := 0; pos < len(ids); {
		id := ids[pos]
		i := sort.Search(len(ranges), func(i int) bool { return c.lookupBounds[ranges[i].num].firstID > id }) - 1
		if id == 0 || i < 0 {
			missing = append(missing, id)
			pos++
			continue
		}
		seg := ranges[i]
		end := pos + 1
		for end < len(ids) && (i+1 == len(ranges) || ids[end] < c.lookupBounds[ranges[i+1].num].firstID) {
			end++
		}
		key := segmentCacheKey{convID: convID, segment: seg.num, size: c.lookupBounds[seg.num].size}
		rolled := seg.num != segs[len(segs)-1].num
		var entries []segEntry
		hit := false
		if rolled {
			entries, hit = s.lookupCache.get(key)
		}
		if !hit {
			var size int64
			entries, size, _, err = s.readSegment(filepath.Join(dir, seg.name))
			if err != nil {
				return nil, err
			}
			var previous uint64
			for _, e := range entries {
				if e.entry.ID <= previous || (i+1 < len(ranges) && e.entry.ID >= c.lookupBounds[ranges[i+1].num].firstID) {
					return nil, fmt.Errorf("%w: non-increasing entry IDs", ErrCorruptSegment)
				}
				previous = e.entry.ID
			}
			// The final file remains appendable; it never supplies cached answers.
			if rolled && size == key.size {
				s.lookupCache.put(key, entries)
			}
		}
		for ; pos < end; pos++ {
			j := sort.Search(len(entries), func(j int) bool { return entries[j].entry.ID >= ids[pos] })
			if j == len(entries) || entries[j].entry.ID != ids[pos] {
				missing = append(missing, ids[pos])
				continue
			}
			out = append(out, detachEntry(entries[j].entry))
		}
	}
	if len(missing) > 0 {
		return nil, &MissingEntriesError{IDs: missing}
	}
	return out, nil
}

// segmentFirstID decodes at most two bounded lines.
// Buffered read-ahead counts as actual I/O, even though it is not decoded.
func (s *Store) segmentFirstID(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("history: open lookup prefix: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only
	s.segmentsOpened++
	counted := &lookupPrefixReader{store: s, reader: io.LimitReader(f, segmentCeiling(s.maxSegmentBytes)+1)}
	r := bufio.NewReaderSize(counted, 256)
	var prefix []byte
	for range 2 {
		line, err := r.ReadBytes('\n')
		prefix = append(prefix, line...)
		if int64(len(prefix)) > segmentCeiling(s.maxSegmentBytes) {
			return 0, fmt.Errorf("%w: lookup prefix exceeds segment ceiling", ErrCorruptSegment)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("history: read lookup prefix: %w", err)
		}
		if err != nil {
			break
		}
	}
	entries, _, err := decodeSegment(prefix)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}
	if entries[0].entry.ID == 0 {
		return 0, fmt.Errorf("%w: zero entry ID", ErrCorruptSegment)
	}
	return entries[0].entry.ID, nil
}

type lookupPrefixReader struct {
	store  *Store
	reader io.Reader
}

func (r *lookupPrefixReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.store.readBytes += int64(n)
	return n, err
}

func detachEntry(e Entry) Entry {
	e.Payload = bytes.Clone(e.Payload)
	if e.Session != nil {
		session := *e.Session
		e.Session = &session
	}
	if e.Shown != nil {
		shown := *e.Shown
		e.Shown = &shown
	}
	return e
}
