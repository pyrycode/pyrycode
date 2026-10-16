package thread

import (
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

const (
	checkpointStateName  = "thread-index-state.json"
	checkpointFileSchema = 1
	// Frames contain big-endian uint32 payload length and CRC-32C, then one
	// JSON CheckpointItem. Only bytes within ValidLength are committed.
	checkpointFrameHeader = 8
)

var checkpointCRC = crc32.MakeTable(crc32.Castagnoli)

// checkpointFileState commits a prefix of one append-only generation. Its schema
// versions the framing and envelope independently of the exported fold schema.
// Certificate stays separate from the scalar-only unloaded-metadata leaf.
type checkpointFileState struct {
	Schema        int             `json:"schema"`
	Generation    string          `json:"generation"`
	ValidLength   int64           `json:"valid_length"`
	Certificate   cacheRecord     `json:"certificate"`
	Fold          CheckpointState `json:"fold"`
	fieldsPresent bool
}

func (s *checkpointFileState) UnmarshalJSON(raw []byte) error {
	type state checkpointFileState
	var decoded state
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	// Zero progress and dirty status are valid only when explicitly recorded.
	var evidence struct {
		ValidLength *int64                 `json:"valid_length"`
		Certificate *struct{ Clean *bool } `json:"certificate"`
		Fold        *struct {
			ConversationID *string `json:"conversation_id"`
			Version        *uint64 `json:"version"`
			LastShown      *uint64 `json:"last_shown"`
		} `json:"fold"`
	}
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return err
	}
	*s = checkpointFileState(decoded)
	s.fieldsPresent = evidence.ValidLength != nil && evidence.Certificate != nil && evidence.Certificate.Clean != nil && evidence.Fold != nil && evidence.Fold.ConversationID != nil && evidence.Fold.Version != nil && evidence.Fold.LastShown != nil
	return nil
}

func checkpointIndexLeaf(name string) bool {
	const prefix, suffix = "thread-index-", ".bin"
	return len(name) == len(prefix)+32+len(suffix) && name[:len(prefix)] == prefix && name[len(name)-len(suffix):] == suffix && validToken(name[len(prefix):len(name)-len(suffix)])
}

// readCheckpoint validates persisted consistency without reading history,
// truncating the index or installing a worker. The caller owns serialized
// fold/source access and validates history evidence before using the result.
// Both clean and dirty checkpoints can restore continuation state.
func (s *Store) readCheckpoint(id conversations.ConversationID, source HistorySource) (*Fold, checkpointFileState, error) {
	var d checkpointFileState
	if err := s.readCacheFile(id, checkpointStateName, &d); err != nil {
		return nil, checkpointFileState{}, err
	}
	c := d.Certificate
	if !d.fieldsPresent || d.Schema != checkpointFileSchema || !checkpointIndexLeaf(d.Generation) || d.ValidLength < 0 ||
		c.Schema != cacheSchema || c.Rules != foldingRules || !c.fieldsPresent || !validToken(c.Epoch) || !validEvidence(c.Version, c.LastShownVersion, c.Checksum) ||
		d.Fold.ConversationID != string(id) || c.Version != d.Fold.Version || c.LastShownVersion != d.Fold.LastShown {
		return nil, checkpointFileState{}, errCacheAbsent
	}
	f, err := s.openCacheFile(id, d.Generation)
	if err != nil {
		return nil, checkpointFileState{}, err
	}
	defer f.Close() // read-only close cannot change persisted bytes
	info, err := f.Stat()
	if err != nil {
		return nil, checkpointFileState{}, ErrPersistence
	}
	if d.ValidLength > info.Size() {
		return nil, checkpointFileState{}, errCacheAbsent
	}
	latest := make(map[uint64]CheckpointItem)
	for remaining := d.ValidLength; remaining > 0; {
		var header [checkpointFrameHeader]byte
		if remaining < int64(len(header)) {
			return nil, checkpointFileState{}, errCacheAbsent
		}
		if _, err := io.ReadFull(f, header[:]); err != nil {
			return nil, checkpointFileState{}, errCacheAbsent
		}
		remaining -= int64(len(header))
		size := int64(binary.BigEndian.Uint32(header[:4]))
		if size == 0 || size > remaining || uint64(size) > uint64(^uint(0)>>1) {
			return nil, checkpointFileState{}, errCacheAbsent
		}
		raw := make([]byte, int(size))
		if _, err := io.ReadFull(f, raw); err != nil || crc32.Checksum(raw, checkpointCRC) != binary.BigEndian.Uint32(header[4:]) {
			return nil, checkpointFileState{}, errCacheAbsent
		}
		var row CheckpointItem
		if err := json.Unmarshal(raw, &row); err != nil || row.Metadata.ID == 0 {
			return nil, checkpointFileState{}, errCacheAbsent
		}
		latest[row.Metadata.ID] = row
		remaining -= size
	}
	if len(latest) != len(d.Fold.ItemIDs) {
		return nil, checkpointFileState{}, errCacheAbsent
	}
	rows := make([]CheckpointItem, 0, len(latest))
	for _, itemID := range d.Fold.ItemIDs {
		row, ok := latest[itemID]
		if !ok {
			return nil, checkpointFileState{}, errCacheAbsent
		}
		rows = append(rows, row)
	}
	restored, err := ImportCheckpoint(string(id), source, d.Fold, rows)
	if err != nil {
		return nil, checkpointFileState{}, err
	}
	return restored, d, nil
}
