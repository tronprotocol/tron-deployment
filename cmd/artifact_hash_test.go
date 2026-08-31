package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

type artifactHashTarget struct {
	*target.LocalTarget
	err error
}

func (t artifactHashTarget) Sha256IfExists(context.Context, string) (string, error) {
	if t.err != nil {
		return "", t.err
	}
	return "new-digest", nil
}

func TestUpgradeArtifactHashFailureDoesNotRetainOldValue(t *testing.T) {
	nc := &nodeContext{Target: artifactHashTarget{LocalTarget: target.NewLocalTarget(), err: errors.New("probe failed")}, Node: &state.ManagedNode{Runtime: "jar", InstallPath: "/tmp", ArtifactSHA256: "old"}}
	if err := refreshArtifactSHA256(context.Background(), nc); err == nil {
		t.Fatal("expected hash error")
	}
	if nc.Node.ArtifactSHA256 != "old" {
		t.Fatalf("digest changed on error: %q", nc.Node.ArtifactSHA256)
	}
}

func TestRollbackArtifactHashFailureDoesNotRetainOldValue(t *testing.T) {
	nc := &nodeContext{Target: artifactHashTarget{LocalTarget: target.NewLocalTarget(), err: errors.New("probe failed")}, Node: &state.ManagedNode{Runtime: "jar", InstallPath: "/tmp", ArtifactSHA256: "old"}}
	if err := refreshArtifactSHA256(context.Background(), nc); err == nil {
		t.Fatal("expected hash error")
	}
	if nc.Node.ArtifactSHA256 != "old" {
		t.Fatalf("digest changed on error: %q", nc.Node.ArtifactSHA256)
	}
}
