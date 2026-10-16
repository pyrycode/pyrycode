package thread

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

const (
	cacheName       = "thread-certificate.json"
	legacyCacheName = "thread-cache.json"
	recoveryName    = "thread-recovery.json"
	cacheSchema     = 1
	// foldingRules must change whenever folded item semantics change.
	foldingRules = 4
)

// ErrPersistence reports a recoverable cache I/O failure.
var ErrPersistence = errors.New("thread: cache persistence unavailable")
var errCacheAbsent = errors.New("thread: cache requires rebuilding")

type cacheRecord struct {
	Schema, Rules    int
	Epoch            string
	Version          uint64
	LastShownVersion uint64
	Checksum         string
	Clean            bool
	fieldsPresent    bool
}

// UnmarshalJSON distinguishes absent/null evidence from valid empty progress.
func (c *cacheRecord) UnmarshalJSON(raw []byte) error {
	type record cacheRecord
	var decoded struct {
		record
		Version          *uint64
		LastShownVersion *uint64
		Checksum         *string
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*c = cacheRecord(decoded.record)
	c.fieldsPresent = decoded.Version != nil && decoded.LastShownVersion != nil && decoded.Checksum != nil
	if c.fieldsPresent {
		c.Version, c.LastShownVersion, c.Checksum = *decoded.Version, *decoded.LastShownVersion, *decoded.Checksum
	}
	return nil
}

type progressRecord struct {
	Epoch            string
	Version          uint64
	LastShownVersion uint64
	Checksum         string
	Err              error
}

// entryChecksum frames ID, length-prefixed type, timestamp seconds/nanoseconds
// and length-prefixed payload using big-endian uint64s. Only the timestamp's
// instant matters; segment formatting and in-memory monotonic time are excluded.
func entryChecksum(entry history.Entry) string {
	h := sha256.New()
	var word [8]byte
	writeWord := func(value uint64) {
		binary.BigEndian.PutUint64(word[:], value)
		_, _ = h.Write(word[:]) // hash.Hash.Write never returns an error
	}
	writeWord(entry.ID)
	writeWord(uint64(len(entry.Type)))
	_, _ = h.Write([]byte(entry.Type))
	writeWord(uint64(entry.TS.Unix()))
	writeWord(uint64(entry.TS.Nanosecond()))
	writeWord(uint64(len(entry.Payload)))
	_, _ = h.Write(entry.Payload)
	return hex.EncodeToString(h.Sum(nil))
}

func validEvidence(version, lastShown uint64, checksum string) bool {
	if lastShown > version {
		return false
	}
	if version == 0 {
		return checksum == ""
	}
	decoded, err := hex.DecodeString(checksum)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == checksum
}

func randomToken() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", ErrPersistence
	}
	return hex.EncodeToString(nonce[:]), nil
}
func validToken(token string) bool {
	decoded, err := hex.DecodeString(token)
	return err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == token
}

// cacheLeaf admits only package-owned leaves, never directory components.
func cacheLeaf(name string) bool {
	return name == cacheName || name == checkpointStateName || checkpointIndexLeaf(name) || legacyLeaf(name)
}

func legacyLeaf(name string) bool {
	if name == legacyCacheName || name == recoveryName {
		return true
	}
	const prefix = "thread-run-"
	return len(name) == len(prefix)+32+len(".json") && name[:len(prefix)] == prefix && name[len(name)-5:] == ".json" && validToken(name[len(prefix):len(name)-5])
}

func (s *Store) openCacheFile(id conversations.ConversationID, name string) (*os.File, error) {
	if !conversations.ValidID(string(id)) || !cacheLeaf(name) {
		return nil, ErrPersistence
	}
	dir, err := s.history.LogDir(id)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errCacheAbsent
	}
	if err != nil {
		return nil, ErrPersistence
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errCacheAbsent
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrPersistence
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrPersistence
	}
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close() // read-only close cannot change persisted bytes
		return nil, ErrPersistence
	}
	return f, nil
}

func (s *Store) readCacheFile(id conversations.ConversationID, name string, dst any) error {
	f, err := s.openCacheFile(id, name)
	if err != nil {
		return err
	}
	defer f.Close() // read-only close cannot change persisted bytes
	decoder := json.NewDecoder(f)
	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errCacheAbsent
		}
		var syntax *json.SyntaxError
		var kind *json.UnmarshalTypeError
		if errors.As(err, &syntax) || errors.As(err, &kind) || errors.Is(err, io.ErrUnexpectedEOF) {
			return errCacheAbsent
		}
		return ErrPersistence
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errCacheAbsent
		}
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) || errors.Is(err, io.ErrUnexpectedEOF) {
			return errCacheAbsent
		}
		return ErrPersistence
	}
	return nil
}

func (s *Store) writeCacheFile(id conversations.ConversationID, name string, value any) error {
	if !conversations.ValidID(string(id)) || !cacheLeaf(name) {
		return ErrPersistence
	}
	dir, err := s.history.LogDir(id)
	if err != nil {
		return ErrPersistence
	}
	path := filepath.Join(dir, name)
	check := func() error {
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || !info.Mode().IsRegular() {
			return ErrPersistence
		}
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".thread-*")
	if err != nil {
		return ErrPersistence
	}
	defer os.Remove(f.Name()) // best-effort cleanup of unpublished temporary bytes
	defer f.Close()           // also closes on an encode/sync failure
	if f.Chmod(0o600) != nil || json.NewEncoder(f).Encode(value) != nil || f.Sync() != nil || f.Close() != nil {
		return ErrPersistence
	}
	// Recheck containment and the destination immediately before replacement.
	again, err := s.history.LogDir(id)
	if err != nil || again != dir {
		return ErrPersistence
	}
	if err := check(); err != nil {
		return err
	}
	if s.replace(f.Name(), path) != nil {
		return ErrPersistence
	}
	if s.syncDir(dir) != nil {
		// A post-rename failure must not leave a reusable clean claim. Removing
		// the leaf never follows redirects; containment is checked again first.
		again, err := s.history.LogDir(id)
		if err == nil && again == dir && check() == nil {
			_ = os.Remove(path) // best-effort invalidation after failed durability
			_ = s.syncDir(dir)  // best-effort durability of that invalidation
		}
		return ErrPersistence
	}
	return nil
}

func syncCacheDir(dir string) error {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return ErrPersistence
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrPersistence
	}
	return nil
}

func (s *Store) recoveryCandidate(id conversations.ConversationID, bound uint64) (cacheRecord, error) {
	var c cacheRecord
	if err := s.readCacheFile(id, cacheName, &c); err != nil {
		if errors.Is(err, errCacheAbsent) {
			return cacheRecord{}, nil
		}
		return c, err
	}
	if c.Schema != cacheSchema || c.Rules != foldingRules || !c.Clean || !validToken(c.Epoch) || c.Version > bound || !c.fieldsPresent || !validEvidence(c.Version, c.LastShownVersion, c.Checksum) {
		return cacheRecord{}, nil
	}
	return c, nil
}

// UnloadedLastShownVersion reads clean certificate metadata for an unloaded,
// caller-authorized conversation only at exactly known current raw progress.
// It opens at most one certificate and never reads segments, starts a worker or
// proves replay equality or retained-range continuity. Zero can be present.
func (s *Store) UnloadedLastShownVersion(id conversations.ConversationID) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.workers[id] != nil {
		return 0, false
	}
	h, known, err := s.history.CachedLatestEntryID(id)
	if err != nil || !known {
		return 0, false
	}
	c, err := s.recoveryCandidate(id, h)
	if err != nil || c.Epoch == "" || c.Version != h {
		return 0, false
	}
	return c.LastShownVersion, true
}

func sameItems(a, b []Item) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}

// removeLegacy runs only after durable dirty marking. Only regular legacy leaves
// can be removed; checkpoints, redirects and unrelated entries are preserved.
func (s *Store) removeLegacy(id conversations.ConversationID) error {
	dir, err := s.history.LogDir(id)
	if err != nil {
		return ErrPersistence
	}
	leaves, err := os.ReadDir(dir)
	if err != nil {
		return ErrPersistence
	}
	for _, leaf := range leaves {
		name := leaf.Name()
		if !legacyLeaf(name) {
			continue
		}
		again, err := s.history.LogDir(id)
		if err != nil || again != dir {
			return ErrPersistence
		}
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || os.Remove(path) != nil {
			return ErrPersistence
		}
	}
	if s.syncDir(dir) != nil {
		return ErrPersistence
	}
	return nil
}
