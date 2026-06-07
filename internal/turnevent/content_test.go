package turnevent

import "testing"

// A ToolUpdate can carry any one of the three ACP content shapes, and a
// consumer recovers which shape a value is via a type switch. Each case below
// constructs a ToolUpdate carrying one shape, recovers the concrete type, and
// asserts every field — a wrong-branch match fails via the default arm.
func TestToolContent_ShapeRecovery(t *testing.T) {
	t.Parallel()

	t.Run("text", func(t *testing.T) {
		t.Parallel()
		upd := ToolUpdate{ToolCallID: "tc", Status: ToolStatusInProgress, Content: TextContent{Text: "hello"}}
		switch c := upd.Content.(type) {
		case TextContent:
			if c.Text != "hello" {
				t.Errorf("TextContent.Text: got %q, want %q", c.Text, "hello")
			}
		default:
			t.Fatalf("recovered %T, want TextContent", upd.Content)
		}
	})

	t.Run("diff", func(t *testing.T) {
		t.Parallel()
		upd := ToolUpdate{ToolCallID: "tc", Status: ToolStatusInProgress, Content: DiffContent{Path: "a.go", OldText: "old", NewText: "new"}}
		switch c := upd.Content.(type) {
		case DiffContent:
			if c.Path != "a.go" || c.OldText != "old" || c.NewText != "new" {
				t.Errorf("DiffContent: got %+v", c)
			}
		default:
			t.Fatalf("recovered %T, want DiffContent", upd.Content)
		}
	})

	t.Run("terminal", func(t *testing.T) {
		t.Parallel()
		upd := ToolUpdate{ToolCallID: "tc", Status: ToolStatusInProgress, Content: TerminalContent{TerminalID: "term-1"}}
		switch c := upd.Content.(type) {
		case TerminalContent:
			if c.TerminalID != "term-1" {
				t.Errorf("TerminalContent.TerminalID: got %q, want %q", c.TerminalID, "term-1")
			}
		default:
			t.Fatalf("recovered %T, want TerminalContent", upd.Content)
		}
	})
}

// A nil ToolContent (status-only ToolUpdate) is distinguishable from a present
// shape — the zero-value ToolUpdate carries no content change.
func TestToolUpdate_NilContentIsStatusOnly(t *testing.T) {
	t.Parallel()
	upd := ToolUpdate{ToolCallID: "tc", Status: ToolStatusCompleted}
	if upd.Content != nil {
		t.Fatalf("status-only ToolUpdate.Content: got %v (%T), want nil", upd.Content, upd.Content)
	}
}
