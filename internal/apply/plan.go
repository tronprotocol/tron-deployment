package apply

import (
	"os"

	"gopkg.in/yaml.v3"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

type PlanChange struct {
	Type            string `json:"type"`
	Field           string `json:"field"`
	From            any    `json:"from,omitempty"`
	To              any    `json:"to,omitempty"`
	RestartRequired bool   `json:"restart_required"`
}

type PlanResult struct {
	CurrentState string
	Changes      []PlanChange
	Downtime     int
	Runtime      string
	IntentHash   string
	ConfigHash   string
	Config       []byte
	LegacyMatch  bool
}

// Plan evaluates the same state transition used by both CLI and MCP plans.
func Plan(parsed *intent.Intent, existing *state.ManagedNode, raw []byte, templateDir string) (*PlanResult, error) {
	canonical, err := yaml.Marshal(parsed)
	if err != nil {
		return nil, output.NewError("VALIDATION_ERROR", output.ExitValidationError, "marshal effective intent: "+err.Error())
	}
	rendered, err := render.RenderHOCONWithSecrets(templateDir, parsed, &parsed.Nodes[0])
	if err != nil {
		return nil, output.NewError("RENDER_ERROR", output.ExitGeneralError, err.Error())
	}
	config := []byte(rendered.Deployable())
	result := &PlanResult{CurrentState: "not_deployed", Runtime: parsed.Target.Runtime, IntentHash: EffectiveIntentHash(canonical), ConfigHash: IntentHashFromBytes(config), Config: config}
	if result.Runtime == "" {
		result.Runtime = "docker"
	}
	if existing == nil {
		result.Changes = []PlanChange{{Type: "create", Field: "node", To: parsed.Name}}
		return result, nil
	}
	result.CurrentState = existing.Status
	legacy := LegacyIntentHashMatches(raw, canonical, existing)
	result.LegacyMatch = legacy
	if existing.IntentHash != result.IntentHash && !legacy {
		result.Changes = append(result.Changes, PlanChange{Type: "update", Field: "intent", From: shortHash(existing.IntentHash), To: shortHash(result.IntentHash), RestartRequired: true})
	}
	if existing.ConfigHash != result.ConfigHash {
		result.Changes = append(result.Changes, PlanChange{Type: "update", Field: "config", From: shortHash(existing.ConfigHash), To: shortHash(result.ConfigHash), RestartRequired: true})
		result.Downtime = 30
	}
	if existing.Version != parsed.Nodes[0].Version && parsed.Nodes[0].Version != "latest" {
		result.Changes = append(result.Changes, PlanChange{Type: "update", Field: "version", From: existing.Version, To: parsed.Nodes[0].Version, RestartRequired: true})
		result.Downtime = 60
	}
	return result, nil
}

func shortHash(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12] + "..."
}

// FindTemplatesDir returns the optional on-disk template directory.
func FindTemplatesDir() string {
	if d := os.Getenv("TROND_TEMPLATES_DIR"); d != "" {
		return d
	}
	for _, c := range []string{"templates", "./templates"} {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			if _, err := os.Stat(c + "/main_net_config.conf"); err == nil {
				return c
			}
		}
	}
	return ""
}
