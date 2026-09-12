//go:build e2e_realclaude

package permbridge

import "encoding/json"

// DiagnoseAlwaysAllow returns the content-free validation status used by the
// authenticated live permission-offer diagnostic. It exists only in tagged
// test builds; production callers continue to receive the all-or-nothing value
// from ParseAlwaysAllow without a content-bearing error path.
func DiagnoseAlwaysAllow(raw json.RawMessage, suppressed bool) string {
	_, status := parseAlwaysAllow(raw, suppressed)
	return status
}
