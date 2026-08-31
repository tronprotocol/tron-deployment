package cmd

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
)

func TestChaosPairRejectsCorruptState(t *testing.T) {
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	if err := os.WriteFile(paths.State(), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	err := chaosPair(context.Background(), "disconnect", "a", "b")
	var structured *output.StructuredError
	if err == nil || !errors.As(err, &structured) || structured.Code != "STATE_ERROR" {
		t.Fatalf("error=%v", err)
	}
}
