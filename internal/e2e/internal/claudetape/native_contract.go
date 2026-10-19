package claudetape

import (
	"errors"
	"reflect"
)

// SameNativeSuggestionContract permits only different lengths of the declared
// coding prefix. Every complete input and unknown field still participates in
// comparison; playback continues to use Player.Input's exact ordered contract.
func SameNativeSuggestionContract(a, b *Tape, prompts []string, clear string) (int, int, error) {
	fail := errors.New("native coding prefix contract changed")
	if len(prompts) != 6 || clear == "" || a.Version != b.Version || a.Scenario != "native-suggestions" || b.Scenario != a.Scenario || !reflect.DeepEqual(a.Args, b.Args) || a.CheckFiles(b.Files) != nil {
		return 0, 0, fail
	}
	type trace struct {
		init, query, user map[string]any
		iterations        int
	}
	parse := func(t *Tape) (trace, error) {
		var out trace
		if err := t.Validate(); err != nil {
			return out, err
		}
		if err := t.RequireNativeSuggestion(); err != nil {
			return out, err
		}
		var inputs []map[string]any
		ids := map[string]bool{}
		for _, f := range t.Frames {
			v, err := object(f.Data)
			if err != nil {
				return out, err
			}
			if v["type"] == "control_request" {
				id, ok := v["request_id"].(string)
				if !ok || id == "" || ids[id] {
					return out, fail
				}
				ids[id] = true
				// Only the top-level control identity is variable, never nested payloads.
				v["request_id"] = "${control}"
			}
			if f.Direction == "stdin" {
				inputs = append(inputs, v)
			}
		}
		if len(inputs) < 4 || len(inputs) > 14 || len(inputs)%2 != 0 {
			return out, fail
		}
		out.iterations = (len(inputs) - 2) / 2
		control := func(v map[string]any, subtype string) bool {
			req, ok := v["request"].(map[string]any)
			return ok && v["type"] == "control_request" && req["subtype"] == subtype
		}
		user := func(v map[string]any, text string) bool {
			msg, ok := v["message"].(map[string]any)
			if !ok || v["type"] != "user" || msg["role"] != "user" {
				return false
			}
			blocks, ok := msg["content"].([]any)
			if !ok || len(blocks) != 1 {
				return false
			}
			block, ok := blocks[0].(map[string]any)
			if !ok || block["type"] != "text" || block["text"] != text {
				return false
			}
			block["text"] = "${checked-user-text}"
			return true
		}
		if !control(inputs[0], "initialize") {
			return out, fail
		}
		out.init = inputs[0]
		for i := 0; i < out.iterations; i++ {
			u, q := inputs[1+2*i], inputs[2+2*i]
			if !user(u, prompts[i]) || !control(q, "get_context_usage") {
				return out, fail
			}
			if i == 0 {
				out.user = u
				out.query = q
			} else if !reflect.DeepEqual(out.user, u) || !reflect.DeepEqual(out.query, q) {
				return out, fail
			}
		}
		last := inputs[len(inputs)-1]
		if !user(last, clear) || !reflect.DeepEqual(last, out.user) {
			return out, fail
		}
		return out, nil
	}
	left, err := parse(a)
	if err != nil {
		return 0, 0, err
	}
	right, err := parse(b)
	if err != nil {
		return 0, 0, err
	}
	if !reflect.DeepEqual(left.init, right.init) || !reflect.DeepEqual(left.query, right.query) || !reflect.DeepEqual(left.user, right.user) {
		return 0, 0, fail
	}
	return left.iterations, right.iterations, nil
}
