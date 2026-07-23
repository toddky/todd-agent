package agent

type EventType int

const (
	EventTextDelta EventType = iota
	EventToolCallStarted
	EventToolResult
	EventTurnComplete
	EventError
)

// Event is one engine occurrence for a frontend to render.
// Which fields are set depends on Type: TextDelta uses Text; ToolCallStarted uses ToolName and ToolInput;
// ToolResult uses ToolName, Result, IsError; Error uses Err.
type Event struct {
	Type      EventType
	Text      string
	ToolName  string
	ToolInput string
	Result    string
	IsError   bool
	Err       error
}
