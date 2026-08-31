package render

import (
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

// TestRenderCompose_ImageOverride is the Phase 3 wire-up guard:
// when the build pipeline produces a locally-tagged image and
// threads its tag through to RenderCompose, the generated compose
// MUST:
//  1. Use that tag in the `image:` field (NOT node.Image:version).
//  2. Emit `pull_policy: never` so `docker compose up` doesn't
//     reach for a remote registry on a tag that only exists
//     locally.
func TestRenderCompose_ImageOverride(t *testing.T) {
	in := &intent.Intent{
		Name:    "dev-node",
		Network: "nile",
		Target:  intent.Target{Type: "local", Runtime: "docker"},
		Nodes: []intent.NodeSpec{{
			Type:    "fullnode",
			Image:   "tronprotocol/java-tron",
			Version: "GreatVoyage-v4.7.7",
		}},
	}
	got := RenderCompose("dev-node", in, &in.Nodes[0], "", "-Xmx4g", "trond-build:dev")

	if !strings.Contains(got, "image: trond-build:dev\n") {
		t.Errorf("compose should reference imageOverride; got:\n%s", got)
	}
	if !strings.Contains(got, "pull_policy: never\n") {
		t.Error("compose with imageOverride MUST set pull_policy: never")
	}
	// Negative: the regular tronprotocol/java-tron:version path must
	// NOT leak into the output when overridden.
	if strings.Contains(got, "tronprotocol/java-tron:GreatVoyage-v4.7.7") {
		t.Errorf("compose leaked node.Image:Version despite override:\n%s", got)
	}
}

// TestRenderCompose_NoOverrideKeepsLegacyBehavior asserts the
// pre-Phase-3 default path still works when imageOverride is empty:
// node.Image (+ optional :Version) flows through, no pull_policy
// line emitted.
func TestRenderCompose_NoOverrideKeepsLegacyBehavior(t *testing.T) {
	in := &intent.Intent{
		Name:    "prod-node",
		Network: "mainnet",
		Target:  intent.Target{Type: "local", Runtime: "docker"},
		Nodes: []intent.NodeSpec{{
			Type:    "fullnode",
			Image:   "tronprotocol/java-tron",
			Version: "GreatVoyage-v4.7.7",
		}},
	}
	got := RenderCompose("prod-node", in, &in.Nodes[0], "", "-Xmx4g", "")

	if !strings.Contains(got, "image: tronprotocol/java-tron:GreatVoyage-v4.7.7\n") {
		t.Errorf("compose should use node.Image:Version; got:\n%s", got)
	}
	if strings.Contains(got, "pull_policy:") {
		t.Error("compose without imageOverride MUST NOT inject pull_policy")
	}
}

func TestRenderComposeEscapesExtraEnvDollar(t *testing.T) {
	i := &intent.Intent{Name: "env-node", Nodes: []intent.NodeSpec{{Type: "fullnode", ExtraEnv: map[string]string{"A": "$VAR", "B": "${VALUE}", "C": "literal$"}}}}
	got := RenderCompose("env-node", i, &i.Nodes[0], "", "", "")
	for _, want := range []string{"A=$$VAR", "B=$${VALUE}", "C=literal$$"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing escaped env %q in %s", want, got)
		}
	}
}
