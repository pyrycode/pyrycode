package main

import (
	"slices"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/protocol"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// offeredModel identifies the retained menu row representing model's family and
// variant. The vocabulary contains one row per family/variant: an alias when
// offered, otherwise the newest pinned row. Truncated values cannot establish
// identity. A miss retains the canonical model for membership classification and
// effort fallback; an empty override remains inherited. Codex values match exactly.
func offeredModel(harness string, list turnevent.ModelList, model string) string {
	if model == "" {
		return ""
	}
	family := followFamily(harness, model)
	for _, option := range list.Models {
		if !slices.Contains(option.TruncatedFields, "value") && followFamily(harness, option.Value) == family {
			return option.Value
		}
	}
	return family
}

// runSettingsFor projects canonical stored settings onto the published menu
// identity for the bound session, live or dormant. It preserves the resolver's
// session isolation, confirmed posture and live marker. Vocabulary reads use the
// same bound/bootstrap/saved priority as publication and settings validation.
func runSettingsFor(reg *conversations.Registry, pool *sessions.Pool, saved savedModelVocabulary, attachments ...*daemonLiveBindings) func(string) (boundRunSettings, bool) {
	return func(convID string) (boundRunSettings, bool) {
		bound, ok, op := liveResolve(liveAttachment(attachments), convID, protocol.TypeSessionSettings, func() (boundRunSettings, bool) {
			return resolveBoundRunSettings(reg, runSettingsPool{Pool: pool}, convID)
		})
		bound.liveOp = op
		if !ok || bound.model == "" {
			return bound, ok
		}
		harness, err := pool.HarnessFor(sessions.SessionID(bound.sessionID))
		if err != nil {
			return bound, true
		}
		list, have := agentModelVocabulary(pool, saved, harness, bound.sessionID)
		if have {
			bound.model = offeredModel(harness, list, bound.model)
		}
		return bound, true
	}
}
