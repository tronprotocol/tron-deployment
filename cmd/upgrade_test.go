package cmd

import (
	"encoding/json"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

func TestMarkArtifactSwappedPreservesUpgradeErrorEnvelope(t *testing.T) {
	err := output.NewError("UPGRADE_ERROR", output.ExitGeneralError, "hash upgraded artifact: mismatch").
		WithSuggestions("Check logs: trond logs node", "Run diagnostics: trond diagnose node")
	marked := markArtifactSwapped(err)

	se, ok := marked.(*output.StructuredError)
	if !ok {
		t.Fatalf("marked error type = %T, want *output.StructuredError", marked)
	}
	if se.Code != "UPGRADE_ERROR" || se.Message != "hash upgraded artifact: mismatch" ||
		len(se.Suggestions) != 2 || !se.ArtifactSwapped {
		t.Fatalf("marked envelope = %+v, error fields changed or fact missing", se)
	}
	data, marshalErr := json.Marshal(se)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["artifact_swapped"] != true {
		t.Fatalf("JSON envelope = %s, artifact_swapped missing", data)
	}
}
