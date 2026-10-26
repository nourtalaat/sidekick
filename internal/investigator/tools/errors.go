package tools

import "fmt"

// ErrorKind says why a tool call failed.
type ErrorKind string

// Error kinds.
const (
	// KindInvalidInput: the model's input does not match the tool's input
	// schema. The source was not called.
	KindInvalidInput ErrorKind = "invalid_input"
	// KindUnknownTool: the run exposes no tool with that name. The source
	// was not called.
	KindUnknownTool ErrorKind = "unknown_tool"
	// KindUnavailable: the source is not connected, or its circuit breaker
	// is open after repeated failures.
	KindUnavailable ErrorKind = "unavailable"
	// KindTimeout: the source did not answer within its timeout.
	KindTimeout ErrorKind = "timeout"
	// KindToolError: the tool ran and reported an error of its own, such
	// as a query it could not parse.
	KindToolError ErrorKind = "tool_error"
	// KindFailed: any other failure, including a panic in the source and a
	// call cancelled because the investigation ended.
	KindFailed ErrorKind = "failed"
)

// ToolError is a failed tool call. The model sees it as a tool result with
// IsError set, so it can try another tool or source.
type ToolError struct {
	Kind ErrorKind
	// Source and Tool name the source and its own tool name; both are empty
	// for an unknown tool.
	Source string
	Tool   string
	// Message is redacted. For KindToolError and KindFailed it can hold
	// text from the source, which is untrusted.
	Message string
}

// Error returns the kind and the message.
func (e *ToolError) Error() string {
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

// fromSource reports whether the message holds text from the source, which
// must be wrapped as untrusted data before the model sees it.
func (e *ToolError) fromSource() bool {
	return e.Kind == KindToolError || e.Kind == KindFailed
}
