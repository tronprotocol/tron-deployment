package mcp

import (
	"fmt"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

// notFound builds the standard NODE_NOT_FOUND structured error for a
// missing managed node. Tools call this when an MCP-supplied name
// doesn't resolve in state.
func notFound(operation, name string) *output.StructuredError {
	return output.NewError("NODE_NOT_FOUND", output.ExitGeneralError,
		fmt.Sprintf("%s: no managed node named %q", operation, name)).
		WithSuggestions(
			"Call the 'list' tool to see currently-managed nodes",
			"If this is a fresh deployment, call 'apply' first to create the node",
		)
}

// notFoundWithSuggestions is a generic NOT_FOUND envelope for resources
// other than managed nodes (e.g. knowledge topics, snapshot sources).
// Agents handle the same NOT_FOUND code so this gives them a uniform
// recovery hook regardless of resource type.
func notFoundWithSuggestions(resource, name string, suggestions ...string) *output.StructuredError {
	return output.NewError("NOT_FOUND", output.ExitGeneralError,
		fmt.Sprintf("%s %q not found", resource, name)).
		WithSuggestions(suggestions...)
}
