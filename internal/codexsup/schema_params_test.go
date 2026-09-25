package codexsup

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// schemaAnnotations are the keywords schemaCheck ignores.
var schemaAnnotations = map[string]bool{
	"title": true, "description": true, "default": true, "format": true, "$schema": true,
}

// schemaCheck validates a decoded JSON value against the JSON Schema subset
// the committed Codex schema uses for request params. It is stricter than JSON
// Schema in one way: an object may carry only the properties its node
// declares unless additionalProperties says otherwise, so a renamed field
// fails instead of passing as an unknown extra. Any keyword outside the subset
// is recorded in unsupported rather than silently skipped.
type schemaCheck struct {
	root        any // the whole bundle, for $ref
	unsupported map[string]bool
}

func newSchemaCheck(root any) *schemaCheck {
	return &schemaCheck{root: root, unsupported: map[string]bool{}}
}

// check returns every mismatch of inst against schema, each prefixed with
// path, the instance's location.
func (v *schemaCheck) check(path string, schema, inst any) []string {
	switch s := schema.(type) {
	case bool:
		if s {
			return nil
		}
		return []string{path + ": schema false allows nothing"}
	case map[string]any:
		return v.checkNode(path, s, inst)
	default:
		v.unsupported[fmt.Sprintf("schema of type %T", schema)] = true
		return nil
	}
}

func (v *schemaCheck) checkNode(path string, s map[string]any, inst any) []string {
	var errs []string
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	obj, isObj := inst.(map[string]any)
	for _, k := range keys {
		val := s[k]
		switch {
		case schemaAnnotations[k]:
		case k == "$ref":
			target, ok := v.resolve(val)
			if !ok {
				errs = append(errs, fmt.Sprintf("%s: unresolved $ref %v", path, val))
				continue
			}
			errs = append(errs, v.check(path, target, inst)...)
		case k == "type":
			if !typeAllows(val, jsonKind(inst)) {
				errs = append(errs, fmt.Sprintf("%s: got %s, want type %v", path, jsonKind(inst), val))
			}
		case k == "enum":
			vals, _ := val.([]any)
			if !slices.ContainsFunc(vals, func(e any) bool { return reflect.DeepEqual(e, inst) }) {
				got, _ := json.Marshal(inst)
				want, _ := json.Marshal(val)
				errs = append(errs, fmt.Sprintf("%s: %s is not in enum %s", path, got, want))
			}
		case k == "properties" || k == "additionalProperties":
			// Checked once, below, together with the strict key rule.
		case k == "required":
			names, _ := val.([]any)
			for _, n := range names {
				name, _ := n.(string)
				if _, ok := obj[name]; isObj && !ok {
					errs = append(errs, fmt.Sprintf("%s: missing required property %q", path, name))
				}
			}
		case k == "items":
			arr, _ := inst.([]any)
			for i, e := range arr {
				errs = append(errs, v.check(fmt.Sprintf("%s[%d]", path, i), val, e)...)
			}
		case k == "anyOf" || k == "oneOf":
			// Every arm is checked, even after a match, so an unsupported
			// keyword in a later arm is still recorded.
			arms, _ := val.([]any)
			var misses []string
			matched := false
			for _, arm := range arms {
				sub := v.check(path, arm, inst)
				if len(sub) == 0 {
					matched = true
				} else {
					misses = append(misses, sub[0])
				}
			}
			if !matched {
				errs = append(errs, fmt.Sprintf("%s: matches no arm of %s (%s)", path, k, strings.Join(misses, "; ")))
			}
		case k == "allOf":
			arms, _ := val.([]any)
			for _, arm := range arms {
				errs = append(errs, v.check(path, arm, inst)...)
			}
		case k == "minimum":
			floor, _ := val.(float64)
			if n, ok := inst.(float64); ok && n < floor {
				errs = append(errs, fmt.Sprintf("%s: %v is below minimum %v", path, n, floor))
			}
		case k == "minLength":
			floor, _ := val.(float64)
			if str, ok := inst.(string); ok && float64(utf8.RuneCountInString(str)) < floor {
				errs = append(errs, fmt.Sprintf("%s: %q is shorter than minLength %v", path, str, floor))
			}
		default:
			v.unsupported[k] = true
		}
	}
	_, hasProps := s["properties"]
	_, hasAdditional := s["additionalProperties"]
	if isObj && (hasProps || hasAdditional || typeAllows(s["type"], "object")) {
		errs = append(errs, v.checkProperties(path, s, obj)...)
	}
	return errs
}

// checkProperties checks each of obj's properties against its declaration.
// A property the node does not declare fails unless additionalProperties is
// true or a schema; an absent additionalProperties counts as false.
func (v *schemaCheck) checkProperties(path string, s, obj map[string]any) []string {
	props, _ := s["properties"].(map[string]any)
	names := make([]string, 0, len(obj))
	for n := range obj {
		names = append(names, n)
	}
	sort.Strings(names)
	var errs []string
	for _, n := range names {
		sub := path + "." + n
		if ps, ok := props[n]; ok {
			errs = append(errs, v.check(sub, ps, obj[n])...)
			continue
		}
		switch ap := s["additionalProperties"].(type) {
		case bool:
			if !ap {
				errs = append(errs, fmt.Sprintf("%s: property %q is not declared", path, n))
			}
		case map[string]any:
			errs = append(errs, v.check(sub, ap, obj[n])...)
		default:
			errs = append(errs, fmt.Sprintf("%s: property %q is not declared", path, n))
		}
	}
	return errs
}

// resolve follows a local JSON pointer ref ("#/definitions/v2/X") in root.
func (v *schemaCheck) resolve(ref any) (any, bool) {
	r, _ := ref.(string)
	rest, ok := strings.CutPrefix(r, "#/")
	if !ok {
		return nil, false
	}
	node := v.root
	for _, tok := range strings.Split(rest, "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		m, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		if node, ok = m[tok]; !ok {
			return nil, false
		}
	}
	return node, true
}

// jsonKind is inst's JSON Schema type; a whole number is "integer".
func jsonKind(inst any) string {
	switch x := inst.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case float64:
		if x == math.Trunc(x) {
			return "integer"
		}
		return "number"
	}
	return fmt.Sprintf("%T", inst)
}

// typeAllows reports whether a type keyword's value (a name or a list of
// names) names kind; an integer is also a number.
func typeAllows(typ any, kind string) bool {
	names := []any{typ}
	if list, ok := typ.([]any); ok {
		names = list
	}
	for _, n := range names {
		if n == kind || (n == "number" && kind == "integer") {
			return true
		}
	}
	return false
}

// paramsSchema is method's params definition, reached through its arm of
// ClientRequest.oneOf as schemaMethods reaches method names.
func paramsSchema(t *testing.T, bundle any, method string) any {
	t.Helper()
	arms, _ := dig(bundle, "definitions", "ClientRequest", "oneOf").([]any)
	for _, arm := range arms {
		enum, _ := dig(arm, "properties", "method", "enum").([]any)
		if slices.Contains(enum, any(method)) {
			if params := dig(arm, "properties", "params"); params != nil {
				return params
			}
		}
	}
	t.Fatalf("schema's ClientRequest has no params for %q", method)
	return nil
}

func dig(node any, keys ...string) any {
	for _, k := range keys {
		m, _ := node.(map[string]any)
		node = m[k]
	}
	return node
}

// TestRequestParamsMatchSchema runs the params every client request actually
// writes through schemaCheck against the committed schema: turn/start once per
// combination of the posture constants, which covers every value
// codexTurnOverrides in cmd/pyry can send.
func TestRequestParamsMatchSchema(t *testing.T) {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var bundle any
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatalf("parse schema: %v", err)
	}

	type turn struct{ approval, sandbox, reviewer string }
	var turns []turn
	for _, a := range []string{ApprovalGranular, ApprovalOnRequest, ApprovalNever} {
		for _, s := range []string{SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess} {
			for _, r := range []string{ReviewerUser, ReviewerAutoReview} {
				turns = append(turns, turn{a, s, r})
			}
		}
	}

	// script is every request in the order the test sends it, with the
	// peer's answer; initialize was answered by the handshake.
	script := [][2]string{{methodInitialize, ""}, {methodThreadStart, `{"thread":{"id":"th-1"}}`},
		{methodThreadResume, `{"thread":{"id":"th-1"}}`}}
	for range turns {
		script = append(script, [2]string{methodTurnStart, `{"turn":{"id":"tu-1"}}`})
	}
	script = append(script, [2]string{methodTurnInterrupt, `{}`},
		[2]string{methodAccountRead, `{"account":null,"requiresOpenaiAuth":false}`},
		[2]string{methodModelList, `{"data":[],"nextCursor":"page-2"}`},
		[2]string{methodModelList, `{"data":[],"nextCursor":null}`})

	c, p := startPeer(t, "codex/0.156.1", Config{Dir: "/work"})
	sent := make(chan [2]string, len(script))
	go func() {
		for _, step := range script {
			f := p.next(step[0])
			if step[1] != "" {
				p.send(fmt.Sprintf(`{"id":%s,"result":%s}`, f["id"], step[1]))
			}
			sent <- [2]string{step[0], string(f["params"])}
		}
	}()

	ctx := ctx5(t)
	if _, err := c.StartThread(ctx); err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	if err := c.ResumeThread(ctx, "th-1"); err != nil {
		t.Fatalf("ResumeThread: %v", err)
	}
	for _, tu := range turns {
		in := TurnInput{Text: "go", Model: "gpt-6-luna", Effort: "high",
			ApprovalPolicy: tu.approval, Sandbox: tu.sandbox, ApprovalsReviewer: tu.reviewer}
		if _, err := c.StartTurn(ctx, in); err != nil {
			t.Fatalf("StartTurn(%v): %v", tu, err)
		}
	}
	if err := c.Interrupt(ctx, "tu-1"); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if _, err := c.SignedIn(ctx); err != nil {
		t.Fatalf("SignedIn: %v", err)
	}
	if _, err := c.LatestModels(ctx); err != nil {
		t.Fatalf("LatestModels: %v", err)
	}

	v := newSchemaCheck(bundle)
	checked := map[string]bool{}
	for range script {
		var got [2]string
		select {
		case got = <-sent:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the peer's captured params")
		}
		method := got[0]
		var inst any
		if err := json.Unmarshal([]byte(got[1]), &inst); err != nil {
			t.Errorf("%s: params %q: %v", method, got[1], err)
			continue
		}
		for _, e := range v.check("params", paramsSchema(t, bundle, method), inst) {
			t.Errorf("%s: %s", method, e)
		}
		checked[method] = true
	}
	for _, m := range clientRequests {
		if !checked[m] {
			t.Errorf("%s: no params were validated; add the request to this test", m)
		}
	}
	for k := range v.unsupported {
		t.Errorf("schema keyword %q is not supported by schemaCheck", k)
	}
}

func TestSchemaCheck(t *testing.T) {
	root := map[string]any{"definitions": map[string]any{"v2": map[string]any{"S": map[string]any{"type": "string"}}}}
	tests := []struct {
		name, schema, inst string
		want               string // substring of the only mismatch; empty for none
		unsupported        string
	}{
		{"renamed property", `{"properties":{"a":{"type":"string"}}}`, `{"b":"x"}`, `property "b" is not declared`, ""},
		{"typed object declares nothing", `{"type":"object"}`, `{"a":1}`, `property "a" is not declared`, ""},
		{"additionalProperties false", `{"properties":{},"additionalProperties":false}`, `{"b":1}`, `property "b" is not declared`, ""},
		{"additionalProperties true", `{"properties":{},"additionalProperties":true}`, `{"b":1}`, "", ""},
		{"additionalProperties schema", `{"additionalProperties":{"type":"string"}}`, `{"b":1}`, "params.b: got integer", ""},
		{"retyped property", `{"properties":{"a":{"type":"string"}}}`, `{"a":1}`, "params.a: got integer, want type string", ""},
		{"nullable null", `{"type":["string","null"]}`, `null`, "", ""},
		{"nullable wrong", `{"type":["string","null"]}`, `true`, "got boolean", ""},
		{"integer is a number", `{"type":"number"}`, `2`, "", ""},
		{"fraction is no integer", `{"type":"integer"}`, `1.5`, "got number", ""},
		{"enum miss", `{"enum":["a","b"]}`, `"c"`, `"c" is not in enum`, ""},
		{"required missing", `{"properties":{"a":{}},"required":["a"]}`, `{}`, `missing required property "a"`, ""},
		{"items", `{"items":{"type":"string"}}`, `["x",1]`, "params[1]: got integer", ""},
		{"anyOf no arm", `{"anyOf":[{"type":"string"},{"type":"null"}]}`, `1`, "matches no arm of anyOf", ""},
		{"oneOf second arm", `{"oneOf":[{"type":"string"},{"type":"integer"}]}`, `1`, "", ""},
		{"allOf every arm", `{"allOf":[{"type":"integer"},{"minimum":3}]}`, `2`, "2 is below minimum 3", ""},
		{"ref followed", `{"$ref":"#/definitions/v2/S"}`, `1`, "want type string", ""},
		{"ref dangling", `{"$ref":"#/definitions/v2/Nope"}`, `1`, "unresolved $ref", ""},
		{"minLength", `{"minLength":1}`, `""`, "shorter than minLength 1", ""},
		{"annotations ignored", `{"title":"t","description":"d","default":1,"format":"uint32","$schema":"x"}`, `1`, "", ""},
		{"unsupported keyword", `{"pattern":"^a"}`, `"a"`, "", "pattern"},
		{"unsupported in a later arm", `{"anyOf":[{"type":"string"},{"maxLength":1}]}`, `"a"`, "", "maxLength"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var schema, inst any
			if err := json.Unmarshal([]byte(tc.schema), &schema); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.inst), &inst); err != nil {
				t.Fatal(err)
			}
			v := newSchemaCheck(root)
			errs := v.check("params", schema, inst)
			switch {
			case tc.want == "" && len(errs) != 0:
				t.Errorf("mismatches %q, want none", errs)
			case tc.want != "" && (len(errs) != 1 || !strings.Contains(errs[0], tc.want)):
				t.Errorf("mismatches %q, want one containing %q", errs, tc.want)
			}
			var unsupported []string
			for k := range v.unsupported {
				unsupported = append(unsupported, k)
			}
			if got := strings.Join(unsupported, ","); got != tc.unsupported {
				t.Errorf("unsupported %q, want %q", got, tc.unsupported)
			}
		})
	}
}
