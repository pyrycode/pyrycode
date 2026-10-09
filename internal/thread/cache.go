package thread

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

const (
	cacheName    = "thread-cache.json"
	recoveryName = "thread-recovery.json"
	cacheSchema  = 1
	// foldingRules must change whenever folded item semantics change.
	foldingRules = 1
)

// ErrPersistence reports a recoverable checkpoint or recovery-marker I/O failure.
var ErrPersistence = errors.New("thread: cache persistence unavailable")
var errCacheAbsent = errors.New("thread: cache requires rebuilding")

type recoveryRecord struct {
	Run         string
	Coordinator conversations.ConversationID
}
type runRecord struct {
	Run      string
	Complete bool
}
type cacheRecord struct {
	Schema, Rules int
	Epoch         string
	Version       uint64
	Items         []Item
	Complete      bool
	Recovery      recoveryRecord
}
type progressRecord struct {
	Epoch   string
	Version uint64
	Err     error
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
func runName(token string) string { return "thread-run-" + token + ".json" }

// cacheLeaf allows only package-owned names, never a path from cache contents.
func cacheLeaf(name string) bool {
	if name == cacheName || name == recoveryName {
		return true
	}
	const prefix = "thread-run-"
	return len(name) == len(prefix)+32+len(".json") && name[:len(prefix)] == prefix && name[len(name)-5:] == ".json" && validToken(name[len(prefix):len(name)-5])
}

func (s *Store) readCacheFile(id conversations.ConversationID, name string, dst any) error {
	if !conversations.ValidID(string(id)) || !cacheLeaf(name) {
		return ErrPersistence
	}
	dir, err := s.history.LogDir(id)
	if errors.Is(err, fs.ErrNotExist) {
		return errCacheAbsent
	}
	if err != nil {
		return ErrPersistence
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return errCacheAbsent
	}
	if err != nil || !info.Mode().IsRegular() {
		return ErrPersistence
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ErrPersistence
	}
	defer f.Close() // read-only close cannot change persisted bytes
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ErrPersistence
	}
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
	return nil
}

func (s *Store) beginRun(id conversations.ConversationID) (recoveryRecord, error) {
	s.initMu.Lock()
	defer s.initMu.Unlock()
	if s.runID == "" {
		token, err := randomToken()
		if err != nil {
			return recoveryRecord{}, err
		}
		if err := s.writeCacheFile(id, runName(token), runRecord{Run: token}); err != nil {
			return recoveryRecord{}, err
		}
		s.runID, s.coordinator = token, id
	}
	return recoveryRecord{Run: s.runID, Coordinator: s.coordinator}, nil
}

func (s *Store) recoveryCandidate(id conversations.ConversationID, bound uint64) (cacheRecord, error) {
	var c cacheRecord
	if err := s.readCacheFile(id, cacheName, &c); err != nil {
		if errors.Is(err, errCacheAbsent) {
			return cacheRecord{}, nil
		}
		return c, err
	}
	if c.Schema != cacheSchema || c.Rules != foldingRules || !c.Complete || !validToken(c.Epoch) || !validToken(c.Recovery.Run) || !conversations.ValidID(string(c.Recovery.Coordinator)) || c.Version > bound {
		return cacheRecord{}, nil
	}
	var marker recoveryRecord
	if err := s.readCacheFile(id, recoveryName, &marker); err != nil {
		if errors.Is(err, errCacheAbsent) {
			return cacheRecord{}, nil
		}
		return c, err
	}
	if marker != c.Recovery {
		return cacheRecord{}, nil
	}
	s.initMu.Lock()
	sameRun := c.Recovery.Run == s.runID && c.Recovery.Coordinator == s.coordinator
	s.initMu.Unlock()
	if !sameRun {
		var run runRecord
		if err := s.readCacheFile(c.Recovery.Coordinator, runName(c.Recovery.Run), &run); err != nil {
			if errors.Is(err, errCacheAbsent) {
				return cacheRecord{}, nil
			}
			return c, err
		}
		if !run.Complete || run.Run != c.Recovery.Run {
			return cacheRecord{}, nil
		}
	}
	return c, nil
}

func sameItems(a, b []Item) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}

// finishRun cannot certify any participant until all final checkpoint writes
// succeed. An interrupted final replacement leaves the run incomplete.
func (s *Store) finishRun(records map[conversations.ConversationID]progressRecord) error {
	for id, p := range records {
		if p.Err != nil {
			return ErrPersistence
		}
		var c cacheRecord
		var marker recoveryRecord
		if s.readCacheFile(id, cacheName, &c) != nil || s.readCacheFile(id, recoveryName, &marker) != nil || !c.Complete || c.Schema != cacheSchema || c.Rules != foldingRules || c.Epoch != p.Epoch || c.Version != p.Version || c.Recovery != marker || marker.Run != s.runID || marker.Coordinator != s.coordinator {
			return ErrPersistence
		}
		if err := s.writeCacheFile(id, cacheName, c); err != nil {
			return err
		}
	}
	if s.runID == "" {
		return nil
	}
	return s.writeCacheFile(s.coordinator, runName(s.runID), runRecord{Run: s.runID, Complete: true})
}
