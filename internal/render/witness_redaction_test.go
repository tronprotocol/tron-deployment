package render

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

// A stand-in SR signing key. Distinctive enough that a substring
// search over any output surface is conclusive.
const probeWitnessKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// The env var name every case in this file renders through.
const probeWitnessKeyEnv = "PROBE_SR_KEY"

func witnessIntent(t *testing.T) (*intent.Intent, *intent.NodeSpec) {
	t.Helper()
	seeds := []string{"1.2.3.4:18888"}
	discovery := false
	i := &intent.Intent{
		Name:    "probe-witness",
		Network: "mainnet",
		Nodes: []intent.NodeSpec{{
			Type:       "witness",
			WitnessKey: &intent.WitnessKey{PrivateKeyEnv: probeWitnessKeyEnv},
			NetworkOverrides: intent.NetworkOverrides{
				Seeds:     &seeds,
				Discovery: &discovery,
			},
			ConfigOverrides: map[string]any{"vm.supportConstant": true},
		}},
	}
	return i, &i.Nodes[0]
}

// TestRendered_DeployCarriesKey_DisplayDoesNot is the core of F3: the
// bytes java-tron gets still hold the real key, while the string every
// print / JSON / MCP / diff surface handles does not.
func TestRendered_DeployCarriesKey_DisplayDoesNot(t *testing.T) {
	t.Setenv("PROBE_SR_KEY", probeWitnessKey)
	i, node := witnessIntent(t)

	r, err := RenderHOCONWithSecrets("", i, node)
	if err != nil {
		t.Fatalf("RenderHOCONWithSecrets: %v", err)
	}

	wantLine := fmt.Sprintf(`localwitness = [%q]`, probeWitnessKey)
	if !strings.Contains(r.Deployable(), wantLine) {
		t.Fatalf("deploy form must inline the real key; missing %q", wantLine)
	}
	if strings.Contains(r.Config, probeWitnessKey) {
		t.Fatal("display form leaked the raw witness key")
	}
	if !strings.Contains(r.Config, `localwitness = ["<REDACTED:PROBE_SR_KEY>"]`) {
		t.Fatalf("display form missing the redaction placeholder:\n%s", tailOf(r.Config))
	}
	if !r.Redacted {
		t.Error("Redacted must be true when a key was removed from the display form")
	}
	if r.WitnessKey.Value() != probeWitnessKey {
		t.Error("WitnessKey should carry the resolved key for callers that genuinely need it")
	}
	if strings.Contains(r.WitnessKey.String(), probeWitnessKey) {
		t.Error("security.PrivateKey.String() leaked the key")
	}
}

// TestRenderHOCON_ReturnsDisplayForm pins that the long-standing
// exported entry point — the one every preview caller uses — is the
// redacted one.
func TestRenderHOCON_ReturnsDisplayForm(t *testing.T) {
	t.Setenv("PROBE_SR_KEY", probeWitnessKey)
	i, node := witnessIntent(t)

	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("RenderHOCON: %v", err)
	}
	if strings.Contains(out, probeWitnessKey) {
		t.Fatal("RenderHOCON leaked the raw witness key")
	}
}

// TestRendered_DeployAndDisplayDifferOnlyInWitnessLine is the
// byte-identity guard: the deploy form must stay exactly what it was
// before the redaction split, so runtime.Deploy writes the same file
// and config_hash never moves. Reconstructing the deploy text from the
// display text by swapping back a single line proves nothing else in
// the render changed.
func TestRendered_DeployAndDisplayDifferOnlyInWitnessLine(t *testing.T) {
	t.Setenv("PROBE_SR_KEY", probeWitnessKey)
	i, node := witnessIntent(t)

	r, err := RenderHOCONWithSecrets("", i, node)
	if err != nil {
		t.Fatalf("RenderHOCONWithSecrets: %v", err)
	}

	display := strings.Split(r.Config, "\n")
	deploy := strings.Split(r.Deployable(), "\n")
	if len(display) != len(deploy) {
		t.Fatalf("line count drifted: display %d, deploy %d", len(display), len(deploy))
	}
	changed := 0
	for n := range display {
		if display[n] == deploy[n] {
			continue
		}
		changed++
		if display[n] != `localwitness = ["<REDACTED:PROBE_SR_KEY>"]` {
			t.Errorf("line %d differs but is not the witness placeholder: %q", n, display[n])
		}
		if deploy[n] != fmt.Sprintf(`localwitness = [%q]`, probeWitnessKey) {
			t.Errorf("line %d deploy side is not the inlined key: %q", n, deploy[n])
		}
	}
	if changed != 1 {
		t.Fatalf("expected exactly 1 differing line, got %d", changed)
	}
}

// TestRendered_NoSecret_FormsAreIdentical covers every non-witness
// path: nothing about those renders may change, in either form.
func TestRendered_NoSecret_FormsAreIdentical(t *testing.T) {
	discovery := false
	cases := map[string]intent.NodeSpec{
		"fullnode-bare": {Type: "fullnode"},
		"fullnode-overrides": {
			Type:             "fullnode",
			NetworkOverrides: intent.NetworkOverrides{Discovery: &discovery},
			ConfigOverrides:  map[string]any{"vm.supportConstant": true},
		},
		"witness-keystore": {
			Type: "witness",
			WitnessKey: &intent.WitnessKey{
				KeystorePath:   "/opt/tron/keystore.json",
				AccountAddress: "TPL66VK2gCXNCD7EJg9pgJRfqcRazjhUZY",
			},
		},
		"witness-no-key": {Type: "witness"},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			i := &intent.Intent{Name: "probe", Network: "mainnet", Nodes: []intent.NodeSpec{spec}}
			r, err := RenderHOCONWithSecrets("", i, &i.Nodes[0])
			if err != nil {
				t.Fatalf("RenderHOCONWithSecrets: %v", err)
			}
			if r.Config != r.Deployable() {
				t.Error("no secret was resolved, so both forms must be byte-identical")
			}
			if r.Redacted {
				t.Error("Redacted must stay false when nothing was removed")
			}
		})
	}
}

// TestRendered_UnsetEnvIsNotRedacted keeps the pre-existing loud
// failure mode intact: `<UNSET:NAME>` is not a secret, so it must
// still appear verbatim in both forms and must not raise the
// "this is a preview" flag.
func TestRendered_UnsetEnvIsNotRedacted(t *testing.T) {
	t.Setenv("PROBE_SR_KEY", "")
	i, node := witnessIntent(t)

	r, err := RenderHOCONWithSecrets("", i, node)
	if err != nil {
		t.Fatalf("RenderHOCONWithSecrets: %v", err)
	}
	if r.Config != r.Deployable() {
		t.Error("unset env: both forms must be identical")
	}
	if r.Redacted {
		t.Error("unset env: nothing secret was removed, so Redacted must be false")
	}
	if !strings.Contains(r.Config, `localwitness = ["<UNSET:PROBE_SR_KEY>"]`) {
		t.Errorf("unset env placeholder changed shape:\n%s", tailOf(r.Config))
	}
}

// TestRendered_FormattingCannotSpillTheKey covers the accidental-print
// paths: fmt reflects over unexported fields for %+v / %#v, so
// Rendered has to implement Stringer / GoStringer.
func TestRendered_FormattingCannotSpillTheKey(t *testing.T) {
	t.Setenv("PROBE_SR_KEY", probeWitnessKey)
	i, node := witnessIntent(t)

	r, err := RenderHOCONWithSecrets("", i, node)
	if err != nil {
		t.Fatalf("RenderHOCONWithSecrets: %v", err)
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if out := fmt.Sprintf(verb, r); strings.Contains(out, probeWitnessKey) {
			t.Errorf("fmt %s spilled the witness key", verb)
		}
	}
	blob, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(blob), probeWitnessKey) {
		t.Error("json.Marshal spilled the witness key")
	}
}

// TestIsWitnessKeyLine_ExactKeyMatch pins the stateless single-line
// predicate: it must catch every shape trond or an operator writes for
// the key itself, and must not swallow the sibling keys that carry no
// secret.
func TestIsWitnessKeyLine_ExactKeyMatch(t *testing.T) {
	match := []string{
		`localwitness = ["deadbeef"]`,
		`localwitness=["deadbeef"]`,
		`  localwitness  =  [ deadbeef ]`,
		"\tlocalwitness\t=\t[\"deadbeef\"]",
		`localwitness = [`,
		`localwitness = ["deadbeef"] # trailing comment`,
	}
	for _, line := range match {
		if !IsWitnessKeyLine(line) {
			t.Errorf("expected witness-key line: %q", line)
		}
	}
	noMatch := []string{
		`localwitnesskeystore = ["/opt/tron/keystore.json"]`,
		`#localwitnesskeystore = [`,
		`localWitnessAccountAddress = "TPL66VK2gCXNCD7EJg9pgJRfqcRazjhUZY"`,
		`# and the localwitness is configured with the private key`,
		`// When it is empty,the localwitness is configured with the private key`,
		`# localWitnessAccountAddress =`,
		`node.discovery.enable = true`,
		``,
		`]`,
	}
	for _, line := range noMatch {
		if IsWitnessKeyLine(line) {
			t.Errorf("must not match: %q", line)
		}
	}
}

// TestRedactWitnessLine covers the substitution every diff surface
// applies at emission time.
func TestRedactWitnessLine(t *testing.T) {
	got := RedactWitnessLine(fmt.Sprintf(`localwitness = [%q]`, probeWitnessKey))
	if strings.Contains(got, probeWitnessKey) {
		t.Fatalf("RedactWitnessLine left the key in place: %q", got)
	}
	if got != `localwitness = ["<REDACTED>"]` {
		t.Errorf("unexpected marker: %q", got)
	}
	// Indent is preserved so redacted diff lines stay aligned.
	if got := RedactWitnessLine(`    localwitness = ["x"]`); got != `    localwitness = ["<REDACTED>"]` {
		t.Errorf("indent not preserved: %q", got)
	}
	// Unrelated lines pass through untouched.
	for _, line := range []string{"", "node.discovery.enable = true", `  localwitnesskeystore = ["/k.json"]`} {
		if got := RedactWitnessLine(line); got != line {
			t.Errorf("RedactWitnessLine(%q) = %q, want unchanged", line, got)
		}
	}
}

func tailOf(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return strings.Join(lines, "\n")
}
