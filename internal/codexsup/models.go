package codexsup

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/pyrycode/pyrycode/internal/turnevent"
)

// Bounds on what LatestModels retains from model/list. Every retained string
// is validated against a closed ASCII alphabet rather than truncated, so none
// needs JSON escaping or can carry a control character, and each has a length
// cap: the result reaches the daemon's model_list.json.
const (
	// maxModelListPages caps the pages one read follows, so a cursor that
	// never ends fails the read instead of stalling the spawn.
	maxModelListPages = 16
	// maxModelFamilies caps the families held. A new family past it is left
	// out; a newer version of a held one still replaces it.
	maxModelFamilies = 8
	// maxModelIDLen caps a model id, [a-z0-9.-] only.
	maxModelIDLen = 64
	// maxFamilyLen caps a family name, [a-z][a-z0-9-]*.
	maxFamilyLen = 32
	// maxModelEffortLevels caps the effort levels kept per family, first in
	// order; maxEffortLen caps one level, [a-z0-9_-] only.
	maxModelEffortLevels = 8
	maxEffortLen         = 32
)

// errModelListPages is returned when model/list still names a next page after
// maxModelListPages.
var errModelListPages = errors.New("codexsup: model/list: too many pages")

// listedModel is the part of a model/list entry that is read. Every other
// field, displayName and description among them, is never decoded.
type listedModel struct {
	ID      string         `json:"id"`
	Efforts []listedEffort `json:"supportedReasoningEfforts"`
}

type listedEffort struct {
	Effort string `json:"reasoningEffort"`
}

// LatestModels reads every page of model/list and returns the newest version
// of each model family, in order of the family's first appearance: Value is
// the family, ResolvedModel the newest version's id and EffortLevels that
// version's advertised levels in order. An entry whose id is not shaped
// gpt-<version>-<family> is left out. Hidden models are not asked for.
//
// Each page is folded into a table of at most maxModelFamilies before the next
// is read. A read that fails part-way returns an error and no list, so a
// family seen only on a later page is never dropped silently.
func (c *Client) LatestModels(ctx context.Context) ([]turnevent.ModelOption, error) {
	var tbl familyTable
	var cursor string
	for range maxModelListPages {
		params := struct {
			Cursor string `json:"cursor,omitempty"`
		}{cursor}
		var res struct {
			Data       []listedModel `json:"data"`
			NextCursor *string       `json:"nextCursor"`
		}
		if err := c.call(ctx, methodModelList, params, &res); err != nil {
			return nil, err
		}
		for _, m := range res.Data {
			tbl.fold(m)
		}
		if res.NextCursor == nil || *res.NextCursor == "" {
			return tbl.options(), nil
		}
		cursor = *res.NextCursor
	}
	return nil, errModelListPages
}

// familyTable is the newest version seen per family, in first-seen order.
type familyTable struct {
	families []familyEntry
}

type familyEntry struct {
	family  string
	version []int
	id      string
	efforts []string
}

// fold records m when its id names a family and it is the newest version of
// that family seen so far. A tie keeps the first seen.
func (t *familyTable) fold(m listedModel) {
	version, family, ok := parseFamilyID(m.ID)
	if !ok {
		return
	}
	i := slices.IndexFunc(t.families, func(e familyEntry) bool { return e.family == family })
	if i < 0 {
		if len(t.families) >= maxModelFamilies {
			return
		}
		t.families = append(t.families, familyEntry{family: family})
		i = len(t.families) - 1
	} else if slices.Compare(version, t.families[i].version) <= 0 {
		return
	}
	t.families[i].version = version
	t.families[i].id = m.ID
	t.families[i].efforts = effortLevels(m.Efforts)
}

// options renders the table; nil when it holds no family.
func (t *familyTable) options() []turnevent.ModelOption {
	var out []turnevent.ModelOption
	for _, e := range t.families {
		out = append(out, turnevent.ModelOption{Value: e.family, ResolvedModel: e.id, EffortLevels: e.efforts})
	}
	return out
}

// effortLevels keeps the valid levels in order, at most maxModelEffortLevels;
// nil when none is valid.
func effortLevels(efforts []listedEffort) []string {
	var out []string
	for _, e := range efforts {
		if len(out) == maxModelEffortLevels {
			break
		}
		if len(e.Effort) <= maxEffortLen && onlyBytes(e.Effort, "abcdefghijklmnopqrstuvwxyz0123456789_-") {
			out = append(out, e.Effort)
		}
	}
	return out
}

// parseFamilyID splits an id shaped gpt-<version>-<family>, where version is
// dot-separated decimal components and family starts with a letter. ok is
// false for any other id.
func parseFamilyID(id string) (version []int, family string, ok bool) {
	if len(id) > maxModelIDLen || !onlyBytes(id, "abcdefghijklmnopqrstuvwxyz0123456789.-") {
		return nil, "", false
	}
	rest, found := strings.CutPrefix(id, "gpt-")
	if !found {
		return nil, "", false
	}
	v, family, found := strings.Cut(rest, "-")
	if !found || family == "" || len(family) > maxFamilyLen || family[0] < 'a' || family[0] > 'z' || strings.Contains(family, ".") {
		return nil, "", false
	}
	for _, part := range strings.Split(v, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, "", false
		}
		version = append(version, n)
	}
	return version, family, true
}

// onlyBytes reports whether s is non-empty and every byte of it is in set.
func onlyBytes(s, set string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(set, s[i]) < 0 {
			return false
		}
	}
	return true
}
