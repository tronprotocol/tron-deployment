package output

// Exit codes per contracts/cli-contract.md.
// Stable across minor versions.
const (
	ExitSuccess           = 0 // Operation completed successfully or no changes needed
	ExitGeneralError      = 1 // Unclassified error
	ExitValidationError   = 2 // Intent file or config validation failed
	ExitTargetUnreachable = 3 // SSH connection failed or Docker not available
	ExitPreflightFailure  = 4 // Target does not meet requirements
	// No code 5. A multi-node command that partially succeeds returns
	// ExitGeneralError with `error_code: "PARTIAL_SUCCESS"` in the JSON
	// (see cmd/network/destroy.go) — the machine-readable distinction lives
	// in the payload, not the exit status. A `ExitPartialSuccess = 5`
	// constant sat here unreferenced by any command; callers that branched
	// on 5 would have waited forever for a code nothing emitted. 5 is left
	// unassigned rather than reused, so it stays available if the split is
	// ever wanted.
	ExitHumanRequired = 10 // Destructive change in non-interactive mode without --auto-approve
)

// ExitCodeName returns a human-readable name for an exit code.
func ExitCodeName(code int) string {
	switch code {
	case ExitSuccess:
		return "SUCCESS"
	case ExitGeneralError:
		return "GENERAL_ERROR"
	case ExitValidationError:
		return "VALIDATION_ERROR"
	case ExitTargetUnreachable:
		return "TARGET_UNREACHABLE"
	case ExitPreflightFailure:
		return "PREFLIGHT_FAILURE"
	case ExitHumanRequired:
		return "HUMAN_REQUIRED"
	default:
		return "UNKNOWN"
	}
}
