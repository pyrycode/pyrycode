//go:build e2e_realclaude

package realclaude

import (
	"github.com/pyrycode/pyrycode/internal/e2e/internal/scenario"
	"github.com/pyrycode/pyrycode/internal/history"
	"io/fs"
	"testing"
)

func requirePromptAnswerHistory(t *testing.T, h *perConvHarness, convID, id, decision string, knownSession bool) {
	t.Helper()
	session := ""
	if knownSession {
		session = streamModalBootstrapUUID
	}
	scenario.RequirePromptAnswerHistory(t, h.home, convID, id, decision, session)
}

func readPromptAnswerHistory(fsys fs.FS) ([]history.Entry, error) {
	return scenario.ReadPromptAnswerHistory(fsys)
}
