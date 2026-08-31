package diagnosis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// SyncChecker verifies the node's block sync progress.
type SyncChecker struct{}

func (c *SyncChecker) Name() string { return "sync_progress" }

func (c *SyncChecker) Run(ctx context.Context, tgt target.Target, opts CheckOpts) CheckResult {
	url := apply.ProbeURL(opts.HTTPPort, "/wallet/getnowblock")
	out, err := target.Get(ctx, tgt, url, 5*time.Second)
	if err != nil {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusFail,
			Message: "Cannot reach node HTTP API",
			Suggestions: []string{
				fmt.Sprintf("Check node is running: trond status %s", opts.NodeName),
				fmt.Sprintf("Verify HTTP port %d is accessible", opts.HTTPPort),
			},
		}
	}

	var block struct {
		BlockHeader struct {
			RawData struct {
				Number    int64 `json:"number"`
				Timestamp int64 `json:"timestamp"`
			} `json:"raw_data"`
		} `json:"block_header"`
	}

	if err := json.Unmarshal(out, &block); err != nil {
		// Cap the snippet against the trimmed length, not the raw length —
		// "   " is 3 bytes raw but trims to 0 and would panic on a [:3]
		// slice of the empty trimmed string.
		trimmed := strings.TrimSpace(string(out))
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Could not parse block response: " + trimmed[:min(100, len(trimmed))],
		}
	}

	height := block.BlockHeader.RawData.Number
	if height == 0 {
		return CheckResult{
			Name:        c.Name(),
			Status:      StatusWarning,
			Message:     "Block height is 0 — node may still be starting",
			Suggestions: []string{"Wait a few minutes for initial sync"},
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusPass,
		Message: fmt.Sprintf("Block height: %d", height),
	}
}
