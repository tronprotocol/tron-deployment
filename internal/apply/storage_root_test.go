package apply

import (
	"path/filepath"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

func TestStorageRootForNode(t *testing.T) {
	deployments := "/deployments"
	tests := []struct {
		name    string
		storage intent.Storage
		want    string
	}{
		{"named data wins", intent.Storage{Data: "volume", StoragePath: "/ignored"}, ""},
		{"relative path anchored", intent.Storage{StoragePath: "./data"}, filepath.Join(deployments, "node0", "data", "data")},
		{"relative data anchored", intent.Storage{Data: "./x"}, filepath.Join(deployments, "node0", "x")},
		{"absolute data unchanged", intent.Storage{Data: "/srv/data", StoragePath: "/ignored"}, "/srv/data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &intent.NodeSpec{Storage: tt.storage}
			if got := StorageRootForNode(node, "docker", deployments, "node0"); got != tt.want {
				t.Fatalf("StorageRootForNode = %q, want %q", got, tt.want)
			}
			if got := StorageRootForNode(node, "jar", deployments, "node0"); got != "" {
				t.Fatalf("jar StorageRoot = %q, want empty", got)
			}
		})
	}
}
