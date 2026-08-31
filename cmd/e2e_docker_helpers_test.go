//go:build e2e

package cmd

import (
	"os/exec"
	"testing"
)

func skipUnlessDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("Docker not available: %v", err)
	}
}
