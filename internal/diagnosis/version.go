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

// VersionChecker compares deployed version against latest release.
type VersionChecker struct{}

func (c *VersionChecker) Name() string { return "version_check" }

func (c *VersionChecker) Run(ctx context.Context, tgt target.Target, opts CheckOpts) CheckResult {
	// Get node version from API
	url := apply.ProbeURL(opts.HTTPPort, "/wallet/getnodeinfo")
	out, err := target.Get(ctx, tgt, url, 5*time.Second)
	if err != nil {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Cannot reach node API to check version",
		}
	}

	var nodeInfo struct {
		ConfigNodeInfo struct {
			CodeVersion string `json:"codeVersion"`
		} `json:"configNodeInfo"`
	}

	if err := json.Unmarshal(out, &nodeInfo); err != nil {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Could not parse node info",
		}
	}

	currentVersion := nodeInfo.ConfigNodeInfo.CodeVersion
	if currentVersion == "" {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Could not determine running version",
		}
	}

	// Check latest release from GitHub API
	ghOut, err := tgt.Exec(ctx, "curl", "-s", "--max-time", "5",
		"https://api.github.com/repos/tronprotocol/java-tron/releases/latest")
	if err != nil {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: fmt.Sprintf("Running version: %s (could not check latest)", currentVersion),
		}
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(ghOut, &release); err != nil {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: fmt.Sprintf("Running version: %s", currentVersion),
		}
	}

	latestVersion := strings.TrimPrefix(release.TagName, "GreatVoyage-v")

	if currentVersion == latestVersion || strings.Contains(release.TagName, currentVersion) {
		return CheckResult{
			Name:    c.Name(),
			Status:  StatusPass,
			Message: fmt.Sprintf("Running latest version: %s", currentVersion),
		}
	}

	return CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("Running %s, latest is %s", currentVersion, release.TagName),
		Suggestions: []string{
			fmt.Sprintf("Upgrade with: trond upgrade %s --version %s", opts.NodeName, release.TagName),
		},
	}
}
