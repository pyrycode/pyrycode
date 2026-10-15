package thread

import "slices"

// agentReports separates unresolved continuation evidence from final projection
// facts. Markers contain only source references, equality and lifecycle facts.
type agentReports struct {
	pending []*agentReport
	markers [4]*agentReport
}

func (h *agentReports) entries() []*agentReport {
	all := slices.Clone(h.pending)
	for _, r := range h.markers {
		if r != nil && !slices.Contains(all, r) {
			all = append(all, r)
		}
	}
	slices.SortStableFunc(all, func(a, b *agentReport) int {
		if a.id < b.id {
			return -1
		}
		if a.id > b.id {
			return 1
		}
		return 0
	})
	return all
}

func (h *agentReports) reduce(shell bool, turn string) {
	// Reconstitute marker candidates before reducing an already compact history.
	if h.markers[0] != nil {
		h.pending, h.markers = h.entries(), [4]*agentReport{}
	}
	var final, result, denial, report, update *agentReport
	for _, r := range h.pending {
		if agentTerminal(r.status) {
			if shell && (r.field == "result" || r.field == "denial") && (!r.mapped || !shellReportMatches(r, turn)) {
				continue
			}
			if shell && r.field == "denial" && denial == nil {
				denial = r
			}
			if r.field == "result" {
				if result == nil {
					result = r
				}
			} else if final == nil {
				final = r
			}
		} else if r.field == "task_report" && r.mapped {
			report = r
			if r.status == "" {
				update = r
			}
		}
	}
	// A foreground result is reversible launch evidence, even on an inactive row.
	if final == nil {
		return
	}
	var pending []*agentReport
	for _, r := range h.pending {
		// Unmatched progress companions can still acquire mapped content/order.
		// Shell result/denial companions can still change foreground eligibility.
		keep := !agentTerminal(r.status) && !r.mapped
		if shell && (r.field == "result" || r.field == "denial") {
			stable := result
			if r.field == "denial" {
				stable = denial
			}
			keep = r == denial || (!r.mapped && (stable == nil || r.id < stable.id))
		}
		if keep {
			pending = append(pending, r)
		}
	}
	h.pending = pending
	h.markers = [4]*agentReport{final, result, report, update}
}
