package streamsup

import "encoding/json"

// UsageWindow is one of claude's account allowance windows as a rate_limit_event
// line reported it: rate_limit_info.unifiedWindows.five_hour or .seven_day.
//
// Both values are claude's own, verbatim and unvalidated: utilization is not
// clamped to 0..1 (every capture shows a fraction, not a percentage) and a
// resetsAt in the past passes through. Both are pointers so a key claude omitted
// stays distinguishable from a reported 0, which for utilization is a fresh
// window rather than an absent reading.
type UsageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *int64   `json:"resetsAt"`
}

// usageWindowsLine is the decode target for the windows alone. It is a SEPARATE
// decode from rateLimitEventLine on purpose: that one is whole-line, so declaring
// unifiedWindows there would let a malformed windows object send the line down
// emitRateLimit's undecodable arm and suppress a rate_limited that fires today.
type usageWindowsLine struct {
	Info struct {
		Windows *struct {
			FiveHour *UsageWindow `json:"five_hour"`
			SevenDay *UsageWindow `json:"seven_day"`
		} `json:"unifiedWindows"`
	} `json:"rate_limit_info"`
}

// usageWindowsDropMsg is readUsageWindows' one log message, carrying a fixed
// daemon-authored reason and never a value from the line.
const (
	usageWindowsDropMsg         = "streamsup: dropping rate_limit_event usage windows"
	usageWindowsDropUndecodable = "undecodable"
)

// SetUsageWindowsHandler installs the destination for every usage reading this
// parser decodes. h is called on the stdout forwarder goroutine, once per
// rate_limit_event line that carries at least one window, benign or not, with a
// nil window for one the line omitted. It must be installed before the parser
// is handed to a runner; nil (the default) discards readings.
func (p *Parser) SetUsageWindowsHandler(h func(fiveHour, sevenDay *UsageWindow)) {
	p.usageWindows = h
}

// readUsageWindows decodes the windows off one rate_limit_event line and hands
// them to the installed handler. A line with no unifiedWindows, a null or empty
// one, or one that fails to decode reaches no handler, so whatever reading the
// consumer already holds stands.
func (p *Parser) readUsageWindows(line []byte) {
	if p.usageWindows == nil {
		return
	}
	var uw usageWindowsLine
	if err := json.Unmarshal(line, &uw); err != nil {
		p.log.Debug(usageWindowsDropMsg, "reason", usageWindowsDropUndecodable)
		return
	}
	w := uw.Info.Windows
	if w == nil || (w.FiveHour == nil && w.SevenDay == nil) {
		return
	}
	p.usageWindows(w.FiveHour, w.SevenDay)
}
