package cmd

import (
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

// An unrecognised --output used to fall through to the text writer, so a
// caller asking for machine-readable output got a human table and exit 0.
// It must be refused as a validation error instead.
func TestValidateFormat(t *testing.T) {
	for _, ok := range []string{"text", "json"} {
		if err := validateFormat("--output", ok); err != nil {
			t.Errorf("%q must be accepted, got %v", ok, err)
		}
	}

	// "yaml" is called out specifically: contracts/cli-contract.md advertised
	// it as a supported value while no writer ever implemented it.
	for _, bad := range []string{"yaml", "xml", "JSON", "", "tex"} {
		err := validateFormat("--output", bad)
		if err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
		se, isStructured := err.(*output.StructuredError)
		if !isStructured {
			t.Fatalf("%q: want *output.StructuredError, got %T", bad, err)
		}
		if se.ExitCode != output.ExitValidationError {
			t.Errorf("%q: exit %d, want %d", bad, se.ExitCode, output.ExitValidationError)
		}
		if !strings.Contains(se.Error(), bad) {
			t.Errorf("%q: message should quote the offending value, got %q", bad, se.Error())
		}
	}
}
