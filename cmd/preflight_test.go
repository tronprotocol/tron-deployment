package cmd

import (
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

func TestCheckMemoryRecommended(t *testing.T) {
	tests := []struct {
		name       string
		nodes      []intent.NodeSpec
		warnings   int
		status     string
		messageHas []string
	}{
		{
			name:     "4GB warns",
			nodes:    []intent.NodeSpec{{Type: "fullnode", Resources: intent.Resources{Memory: "4GB"}}},
			warnings: 1, messageHas: []string{"node 'test'", "8192MB"},
		},
		{
			name:   "8GB passes",
			nodes:  []intent.NodeSpec{{Type: "fullnode", Resources: intent.Resources{Memory: "8GB"}}},
			status: "pass",
		},
		{
			name:   "16GB passes",
			nodes:  []intent.NodeSpec{{Type: "fullnode", Resources: intent.Resources{Memory: "16GB"}}},
			status: "pass",
		},
		{
			name: "mixed memory warns once",
			nodes: []intent.NodeSpec{
				{Type: "fullnode", Resources: intent.Resources{Memory: "2GB"}},
				{Type: "fullnode", Resources: intent.Resources{Memory: "16GB"}},
			},
			warnings: 1,
		},
		{
			name:       "empty memory fails validation",
			nodes:      []intent.NodeSpec{{Type: "fullnode"}},
			status:     "fail",
			messageHas: []string{"invalid", "memory"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks := checkMemoryRecommended(&intent.Intent{Name: "test", Nodes: tt.nodes})
			gotWarnings := 0
			for _, check := range checks {
				if check.Status == "warning" {
					gotWarnings++
				}
				for _, part := range tt.messageHas {
					if !strings.Contains(check.Message, part) {
						t.Errorf("message %q missing %q", check.Message, part)
					}
				}
			}
			if gotWarnings != tt.warnings {
				t.Errorf("warning count = %d, want %d", gotWarnings, tt.warnings)
			}
			if tt.status != "" && (len(checks) != 1 || checks[0].Status != tt.status) {
				t.Errorf("checks = %+v, want one %s check", checks, tt.status)
			}
		})
	}
}
