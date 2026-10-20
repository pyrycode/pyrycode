package e2emcp

import (
	"bytes"
	"strings"
	"testing"
)

func TestMCPStartupCapabilityIsClosedAndModeBound(t *testing.T) {
	for _, c := range []Capability{{Version: 1, Mode: "capture", Socket: "/tmp/controlled.sock", Token: strings.Repeat("a", 64)}, {Version: 1, Mode: "replay"}} {
		var wire bytes.Buffer
		if WriteCapability(&wire, c) != nil {
			t.Fatal("private capability encode")
		}
		got, err := ReadCapability(&wire)
		if err != nil || got != c {
			t.Fatal("private capability changed")
		}
	}
	for _, raw := range []string{
		`{"Version":1,"Mode":"replay","Socket":"/tmp/foreign.sock"}`,
		`{"Version":1,"Mode":"capture","Socket":"/tmp/control.sock"}`,
		`{"Version":1,"Mode":"foreign"}`,
		`{"Version":1,"Mode":"replay","Mode":"capture"}`,
		`{"Version":1,"Mode":"replay","Extra":true}`,
		`{"Version":1,"version":1,"Mode":"replay","Socket":"","Token":""}`,
		`{"version":1,"Mode":"replay","Socket":"","Token":""}`,
		`{"Version":1,"Mode":"replay"}{}`,
		strings.Repeat("x", 1025),
	} {
		if _, err := ReadCapability(strings.NewReader(raw)); err == nil {
			t.Fatal("ambiguous or unbounded startup capability accepted")
		}
	}
}

func TestPrivateMCPHeaderRejectsCaseFoldedFieldAliases(t *testing.T) {
	for _, raw := range []string{`{"Version":1,"Token":"a","Args":[],"Env":[],"Cwd":"/tmp","cwd":"/foreign"}`, `{"Version":1,"Token":"a","Args":[],"Env":[],"cwd":"/tmp"}`} {
		if closedFields([]byte(raw), "Version", "Token", "Args", "Env", "Cwd") {
			t.Fatal("case-folded launch header accepted")
		}
	}
}
