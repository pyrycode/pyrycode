package turnevent

// ToolContent is the sealed sum type a ToolUpdate carries: text, diff, or
// terminal. A consumer recovers the concrete shape with a type switch. A nil
// ToolContent is a valid "no content change" (status-only ToolUpdate), not an
// error. The unexported marker keeps the variant set closed to this package.
type ToolContent interface{ isToolContent() }

// TextContent is plain text tool output.
type TextContent struct {
	Text string
}

// DiffContent is a file edit a tool produced.
type DiffContent struct {
	Path    string
	OldText string
	NewText string
}

// TerminalContent references, by id, a terminal a tool is driving.
type TerminalContent struct {
	TerminalID string
}

func (TextContent) isToolContent()     {}
func (DiffContent) isToolContent()     {}
func (TerminalContent) isToolContent() {}

var (
	_ ToolContent = TextContent{}
	_ ToolContent = DiffContent{}
	_ ToolContent = TerminalContent{}
)
