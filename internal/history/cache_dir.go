package history

import (
	"errors"

	"github.com/pyrycode/pyrycode/internal/conversations"
)

// EnsureLogDir creates the contained log directory without creating history
// entries. It shares Append's caller-authorization precondition.
func (s *Store) EnsureLogDir(id conversations.ConversationID) (string, error) {
	if !conversations.ValidID(string(id)) {
		return "", ErrInvalidID
	}
	if s == nil {
		return "", errors.New("history: store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolveDir(id, true)
}
