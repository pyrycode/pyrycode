package codexsup

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestDefaultDeclinesMatchSchema validates every default result declineFor
// produces against the committed schema's response definition for that
// method, so a hand-written literal cannot drift from the contract. The
// response definition is found from the ServerRequest arm's params ref,
// XParams → XResponse.
func TestDefaultDeclinesMatchSchema(t *testing.T) {
	s := loadSchema(t)
	arms := s.serverRequestParams(t)
	results := 0
	for _, m := range serverRequests {
		result, rpcErr := declineFor(m)
		if rpcErr != nil {
			continue
		}
		results++
		params, ok := arms[m]
		if !ok || !strings.HasSuffix(params, "Params") {
			t.Fatalf("%s: schema ServerRequest arm has params ref %q", m, params)
		}
		respRef := strings.TrimSuffix(params, "Params") + "Response"
		resp, err := s.resolve(respRef)
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		if err := s.validate(resp, wireValue(t, result)); err != nil {
			t.Errorf("%s: default %s does not match %s: %v", m, mustJSON(t, result), respRef, err)
		}
	}
	if results != 5 {
		t.Errorf("declineFor produced %d results, want one per approval method (5)", results)
	}
}

// TestSchemaValidatorRejectsBareDenied proves the validator bites: the bare
// "denied" string is not a ReviewDecision arm at 0.156.1.
func TestSchemaValidatorRejectsBareDenied(t *testing.T) {
	s := loadSchema(t)
	resp, err := s.resolve("#/definitions/ExecCommandApprovalResponse")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.validate(resp, wireValue(t, map[string]string{"decision": "denied"})); err == nil {
		t.Error(`{"decision":"denied"} validated against ExecCommandApprovalResponse`)
	}
}

// jsonSchema is a validator for the subset of draft-07 the committed schema
// uses on response shapes: $ref, allOf/anyOf/oneOf, enum, type, properties,
// required, additionalProperties:false and items. Other keywords are ignored.
type jsonSchema struct{ root any }

func loadSchema(t *testing.T) jsonSchema {
	t.Helper()
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return jsonSchema{root: root}
}

// serverRequestParams maps each ServerRequest method to its params $ref.
func (s jsonSchema) serverRequestParams(t *testing.T) map[string]string {
	t.Helper()
	def, err := s.resolve("#/definitions/ServerRequest")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, a := range def["oneOf"].([]any) {
		props := a.(map[string]any)["properties"].(map[string]any)
		ref, _ := props["params"].(map[string]any)["$ref"].(string)
		for _, m := range props["method"].(map[string]any)["enum"].([]any) {
			out[m.(string)] = ref
		}
	}
	return out
}

func (s jsonSchema) resolve(ref string) (map[string]any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("unsupported $ref %q", ref)
	}
	node := s.root
	for _, part := range strings.Split(ref[2:], "/") {
		obj, ok := node.(map[string]any)
		if !ok || obj[part] == nil {
			return nil, fmt.Errorf("$ref %q does not resolve", ref)
		}
		node = obj[part]
	}
	def, ok := node.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("$ref %q is not a schema object", ref)
	}
	return def, nil
}

func (s jsonSchema) validate(sch map[string]any, v any) error {
	if ref, ok := sch["$ref"].(string); ok {
		def, err := s.resolve(ref)
		if err != nil {
			return err
		}
		return s.validate(def, v)
	}
	if all, ok := sch["allOf"].([]any); ok {
		for _, sub := range all {
			if err := s.validate(sub.(map[string]any), v); err != nil {
				return err
			}
		}
	}
	for _, kw := range []string{"anyOf", "oneOf"} {
		arms, ok := sch[kw].([]any)
		if !ok {
			continue
		}
		matched := 0
		for _, sub := range arms {
			if s.validate(sub.(map[string]any), v) == nil {
				matched++
			}
		}
		if matched == 0 || (kw == "oneOf" && matched > 1) {
			return fmt.Errorf("%s: %d of %d arms match %v", kw, matched, len(arms), v)
		}
	}
	if enum, ok := sch["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			found = found || reflect.DeepEqual(e, v)
		}
		if !found {
			return fmt.Errorf("%v is not in enum %v", v, enum)
		}
	}
	if typ, ok := sch["type"]; ok && !typeMatches(typ, v) {
		return fmt.Errorf("%v is not of type %v", v, typ)
	}
	switch v := v.(type) {
	case map[string]any:
		props, _ := sch["properties"].(map[string]any)
		for _, r := range asSlice(sch["required"]) {
			if _, ok := v[r.(string)]; !ok {
				return fmt.Errorf("missing required %q", r)
			}
		}
		for k, val := range v {
			sub, ok := props[k].(map[string]any)
			if !ok {
				if sch["additionalProperties"] == false {
					return fmt.Errorf("property %q not allowed", k)
				}
				continue
			}
			if err := s.validate(sub, val); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	case []any:
		if items, ok := sch["items"].(map[string]any); ok {
			for i, el := range v {
				if err := s.validate(items, el); err != nil {
					return fmt.Errorf("[%d]: %w", i, err)
				}
			}
		}
	}
	return nil
}

func typeMatches(typ, v any) bool {
	for _, t := range asSlice(typ) {
		switch t {
		case "null":
			if v == nil {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "number":
			if _, ok := v.(float64); ok {
				return true
			}
		case "integer":
			if f, ok := v.(float64); ok && f == math.Trunc(f) {
				return true
			}
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		}
	}
	return false
}

func asSlice(x any) []any {
	if s, ok := x.([]any); ok {
		return s
	}
	if x == nil {
		return nil
	}
	return []any{x}
}

// wireValue round-trips v through JSON, so validation sees the bytes sent.
func wireValue(t *testing.T, v any) any {
	t.Helper()
	var out any
	if err := json.Unmarshal([]byte(mustJSON(t, v)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
