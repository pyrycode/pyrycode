package relay

import "encoding/json"

// queueItemBinding distinguishes legacy omission from an item ID in 1..2^53-1.
// Malformed bindings are never logged, reflected, or treated as legacy requests.
func queueItemBinding(raw json.RawMessage) (uint64, bool) {
	if len(raw) == 0 {
		return 0, true
	}
	if len(raw) > 20 {
		return 0, false
	}
	var id uint64
	if json.Unmarshal(raw, &id) != nil || id == 0 || id > 1<<53-1 {
		return 0, false
	}
	return id, true
}
