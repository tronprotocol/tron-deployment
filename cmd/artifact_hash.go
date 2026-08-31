package cmd

import (
	"context"
	"fmt"
	"path/filepath"
)

func refreshArtifactSHA256(ctx context.Context, nc *nodeContext) error {
	digest, err := nc.Target.Sha256IfExists(ctx, filepath.Join(nc.Node.InstallPath, "FullNode.jar"))
	if err != nil {
		return fmt.Errorf("sha256 probe: %w", err)
	}
	nc.Node.ArtifactSHA256 = digest
	return nil
}
