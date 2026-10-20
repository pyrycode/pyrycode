package claudetape

import (
	"os"
	"testing"
)

func TestHandoffNativeFixtureParser(t *testing.T) {
	f, err := os.Open("../../testdata/claude-tapes/session-rotation.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	suite, err := DecodeLifecycleSuite(f, []string{"bootstrap", "rotated"})
	if err != nil {
		t.Fatal(err)
	}
	note, err := lifecycleHandoffText(suite.Roles["bootstrap"].Tape)
	if err != nil {
		t.Fatal("real native wrapup could not be derived", err)
	}
	if len(note) == 0 {
		t.Fatal("empty native handoff")
	}
	t.Logf("native complete wrapup derived (%d bytes)", len(note))
}
