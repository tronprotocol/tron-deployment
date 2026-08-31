package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// StructuredError represents a CLI error with code, message, and fix suggestions.
//
// Wire format matches schemas/output/error.schema.json and the
// agent contract in AGENTS.md: { error_code, exit_code, message,
// suggestions }. The historical short `code` field was a CLI-only
// pre-contract leak and is no longer emitted — agents that ever
// parsed it must now read `error_code`. The MCP envelope path
// already used the schema names.
type StructuredError struct {
	Code        string   `json:"error_code"`
	Message     string   `json:"message"`
	Suggestions []string `json:"suggestions,omitempty"`
	ExitCode    int      `json:"exit_code"`
	// ArtifactSwapped reports that an upgrade changed the runnable artifact
	// before a later error. It is omitted for all other errors.
	ArtifactSwapped bool `json:"artifact_swapped,omitempty"`
}

func (e *StructuredError) Error() string {
	return e.Message
}

// NewError creates a StructuredError with the given code, exit code, and message.
func NewError(code string, exitCode int, message string) *StructuredError {
	return &StructuredError{
		Code:     code,
		Message:  message,
		ExitCode: exitCode,
	}
}

// NewErrorf creates a StructuredError with a formatted message.
func NewErrorf(code string, exitCode int, format string, args ...any) *StructuredError {
	return &StructuredError{
		Code:     code,
		Message:  fmt.Sprintf(format, args...),
		ExitCode: exitCode,
	}
}

// WithSuggestions adds fix suggestions to the error.
func (e *StructuredError) WithSuggestions(suggestions ...string) *StructuredError {
	e.Suggestions = suggestions
	return e
}

// WriteError writes a StructuredError to the given writer in the specified format.
func WriteError(w io.Writer, err *StructuredError, format string) {
	switch format {
	case "json":
		data, _ := json.Marshal(err)
		fmt.Fprintln(w, string(data))
	default:
		fmt.Fprintf(w, "Error [%s]: %s\n", err.Code, err.Message)
		if len(err.Suggestions) > 0 {
			fmt.Fprintln(w, "Suggestions:")
			for _, s := range err.Suggestions {
				fmt.Fprintf(w, "  - %s\n", s)
			}
		}
	}
}

// Errorf is a convenience for creating common error types.
func Errorf(code string, format string, args ...any) *StructuredError {
	return &StructuredError{
		Code:     strings.ToUpper(code),
		Message:  fmt.Sprintf(format, args...),
		ExitCode: ExitGeneralError,
	}
}
