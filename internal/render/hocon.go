package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/gurkankaymak/hocon"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/security"
)

// NetworkTemplate maps network names to their base config template file.
var NetworkTemplate = map[string]string{
	"mainnet": "main_net_config.conf",
	"nile":    "test_net_config.conf",
	"private": "private_net_config.conf",
}

// witnessKeyName is the exact java-tron config key that carries the
// super-representative signing key.
const witnessKeyName = "localwitness"

// redactedWitnessAssignment is what every text / JSON / diff surface
// prints in place of a `localwitness` assignment.
const redactedWitnessAssignment = witnessKeyName + ` = ["<REDACTED>"]`

// Rendered carries a rendered java-tron HOCON config in two forms.
//
// Config is the DISPLAY form and is the only one safe to print to
// stdout, serialise into JSON, hand to an MCP client or write into a
// diff: a resolved witness (SR) private key has been replaced by a
// `<REDACTED:ENV_NAME>` placeholder. The placeholder names only the
// environment variable, which is already public in the intent file.
//
// The DEPLOY form — the byte stream java-tron actually needs, with the
// real key inlined — is unexported and reachable only through
// Deployable(). Keeping it unexported means the secret cannot escape
// through an accidental fmt / encoding-json / log call: a caller has
// to spell out Deployable() to obtain it.
type Rendered struct {
	// Config is the redacted display form. Byte-identical to the
	// deploy form whenever no witness private key was resolved.
	Config string

	// WitnessKey holds the resolved witness private key, wrapped so
	// String / MarshalJSON / MarshalText all render "[REDACTED]".
	// Zero value when the node is not a witness, uses a keystore, or
	// the referenced env var is unset.
	WitnessKey security.PrivateKey

	// Redacted reports whether Config had a real witness private key
	// removed from it — i.e. whether Config is a preview that
	// java-tron would reject rather than a deployable artifact.
	Redacted bool

	// deploy is the real config bytes. Unexported on purpose.
	deploy string
}

// Deployable returns the config bytes to hand to runtime.Deploy (and
// to hash for idempotency). This is the ONLY accessor that yields the
// real witness private key; every caller that prints, serialises or
// diffs the result must use Config instead.
func (r Rendered) Deployable() string { return r.deploy }

// String returns the redacted display form so that %v / %s / %+v on a
// Rendered can never spill the key. Without this, fmt would reflect
// over the unexported deploy field and print the secret verbatim.
func (r Rendered) String() string { return r.Config }

// GoString keeps %#v away from the deploy field for the same reason.
func (r Rendered) GoString() string {
	return fmt.Sprintf("render.Rendered{Config: %q, Redacted: %t}", r.Config, r.Redacted)
}

// IsWitnessKeyLine reports whether one line of HOCON is the
// `localwitness = ...` assignment that carries the SR signing key.
//
// The match is a deliberately stateless, exact single-line key match:
// the line is trimmed, must begin with the literal key `localwitness`,
// and the next non-blank character must be `=`. No state is carried
// between lines and no bracket / quote scanning is done, so the
// predicate is safe to apply to any line of any config in any order.
// `localwitnesskeystore` (a file path) and `localWitnessAccountAddress`
// (a public address) both fail the match, which is what we want.
//
// Known limits, accepted deliberately: a key sitting on a CONTINUATION
// line of a hand-written multi-line block
//
//	localwitness = [
//	  <key>
//	]
//
// or inside a commented-out line is not matched. Detecting those needs
// a stateful, delimiter-scanning parser over the whole file, which is
// exactly the fragile machinery this fix is required to avoid. trond
// itself only ever renders the single-line form, so every config trond
// produces is fully covered.
func IsWitnessKeyLine(line string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), witnessKeyName)
	if !ok {
		return false
	}
	separator := strings.TrimLeft(rest, " \t")
	return strings.HasPrefix(separator, "=") || strings.HasPrefix(separator, ":")
}

// RedactWitnessLine returns line unchanged unless it is a witness
// private-key assignment, in which case the entire assignment is
// replaced by a fixed marker that carries no key material.
//
// Every surface that emits raw config lines — the `plan --diff`,
// `config diff`, `verify-config` and MCP verify_config differs — runs
// each line through this immediately before emitting it. Comparison
// still happens on the raw lines, so a rotated or drifted key is still
// reported as a change; the reader just sees the marker on both sides
// instead of the two keys.
//
// Over-inclusive by design: a bare `localwitness = [` opening line
// (the shape the shipped templates use) is also rewritten. That costs
// nothing but a slightly blunter diff line and keeps us on the safe
// side of any unquoted / oddly-spaced value an operator may have
// hand-written into a live conf.
func RedactWitnessLine(line string) string {
	if !IsWitnessKeyLine(line) {
		return line
	}
	return lineIndent(line) + redactedWitnessAssignment
}

// redactedWitnessElement stands in for a key that sits on its own line
// inside a multi-line `localwitness = [ ... ]` array.
const redactedWitnessElement = `"<REDACTED>"`

// redactedValue stands in for a key value replaced in place, where the
// surrounding quoting and punctuation of the original line is kept.
const redactedValue = "<REDACTED>"

// RedactWitnessLines redacts a whole config's worth of lines at once and
// returns a slice of the same length, so callers can keep comparing the
// raw lines positionally while emitting the redacted ones.
//
// It exists because RedactWitnessLine, looking at one line in isolation,
// cannot see the shape the shipped templates use:
//
//	localwitness = [
//	  <the key>
//	]
//
// Only the opening line begins with the `localwitness` key, so a per-line
// pass leaves the element line — the one that carries the key material —
// untouched. Every surface that emits config lines (plan --diff, config
// diff, verify-config and the MCP drift tool, whose output leaves the
// machine) must go through this rather than mapping RedactWitnessLine
// over the slice.
//
// The key values are found by parsing the config rather than by reading
// line shapes, so the formatting does not matter: single-line,
// multi-line, a `]` inside a comment on the opening line, an element
// that closes the array on its own line. A scan over line shapes gets
// each of those wrong, and for redaction the failure is silent — the
// caller cannot tell a config with no key from one whose key slipped
// through. When the text does not parse (a partial read off a node, a
// file mid-write) it falls back to the scan, which is why the scan is
// hardened rather than deleted.
func RedactWitnessLines(lines []string) []string {
	if values := witnessKeyValues(strings.Join(lines, "\n")); len(values) > 0 {
		out := make([]string, len(lines))
		for i, line := range lines {
			out[i] = redactValues(line, values)
		}
		return out
	}
	return redactWitnessLinesByScan(lines)
}

// witnessKeyValues parses raw and returns the literal strings assigned to
// `localwitness`. Nil when the text does not parse or carries no key —
// the caller falls back to the scan, which cannot tell those two apart
// either but at least does not claim to.
func witnessKeyValues(raw string) []string {
	cfg, err := hocon.ParseString(raw)
	if err != nil || cfg == nil {
		return nil
	}

	// A parse error is reported through err, but this library also
	// panics on some malformed input; a redaction path must not take
	// the process down with it.
	var values []string
	func() {
		defer func() { _ = recover() }()
		for _, v := range cfg.GetArray(witnessKeyName) {
			if v == nil {
				continue
			}
			// String() quotes string values; the raw text may or may
			// not have them, so match against both.
			s := strings.TrimSpace(v.String())
			unquoted := strings.Trim(s, `"`)
			if unquoted == "" {
				continue
			}
			values = append(values, unquoted)
		}
	}()
	return values
}

// redactValues replaces every occurrence of a key value in line. Longest
// first, so one value being a prefix of another cannot leave a tail
// behind.
func redactValues(line string, values []string) string {
	out := line
	sorted := slices.Clone(values)
	slices.SortFunc(sorted, func(a, b string) int { return len(b) - len(a) })
	for _, v := range sorted {
		out = strings.ReplaceAll(out, v, redactedValue)
	}
	return out
}

// redactWitnessLinesByScan is the fallback for text that does not parse.
// It walks the lines and stays inside an unterminated localwitness
// array. Comments are stripped before the bracket checks: a `]` inside a
// comment on the opening line would otherwise end the array before it
// began, and let the key through.
func redactWitnessLinesByScan(lines []string) []string {
	out := make([]string, len(lines))
	inArray := false
	for i, line := range lines {
		trimmed := stripComment(strings.TrimSpace(line))
		switch {
		case inArray:
			if strings.HasPrefix(trimmed, "]") {
				inArray = false
				out[i] = line
				continue
			}
			if trimmed == "" {
				out[i] = line
				continue
			}
			out[i] = lineIndent(line) + redactedWitnessElement
			// An element may close the array on its own line.
			if strings.HasSuffix(trimmed, "]") {
				inArray = false
			}
		case IsWitnessKeyLine(line):
			out[i] = lineIndent(line) + redactedWitnessAssignment
			// An assignment that opens an array without closing it on
			// the same line continues on the lines that follow.
			if !strings.Contains(trimmed, "]") {
				inArray = true
			}
		default:
			out[i] = line
		}
	}
	return out
}

// stripComment removes a trailing HOCON comment so a `]` or `[` inside
// it cannot steer the bracket tracking. HOCON accepts both # and //.
func stripComment(s string) string {
	if i := strings.Index(s, "#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "//"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// RenderHOCON loads the base template for the network and applies intent-driven overrides.
// Returns the final HOCON config as a string. templateDir may be empty, in
// which case the embedded template is used.
//
// Override layering (last write wins, per HOCON spec):
//
//  1. Per-key line-level rewrites for ports + features. These keep the
//     surrounding template comments and structure intact so the output is
//     still legible to a human.
//  2. An appended "trond overrides" block carrying everything from
//     network_overrides + witness_key + config_overrides. HOCON merges by
//     dotted-key, so anything written here trumps earlier values.
//
// Two layers exist because some keys (ports) need to stay in their original
// place to keep the file diff-friendly, while bulk overrides are simpler
// and safer to express as a final-section append.
//
// RenderHOCON returns the DISPLAY form (see Rendered): any resolved
// witness private key is replaced by a `<REDACTED:ENV_NAME>`
// placeholder, because this string ends up on stdout, in JSON payloads
// and in MCP tool results. Callers that need the deployable bytes must
// use RenderHOCONWithSecrets and ask for Deployable().
func RenderHOCON(templateDir string, i *intent.Intent, node *intent.NodeSpec) (string, error) {
	r, err := RenderHOCONWithSecrets(templateDir, i, node)
	if err != nil {
		return "", err
	}
	return r.Config, nil
}

// RenderHOCONWithSecrets renders the same config as RenderHOCON but
// returns both forms: Rendered.Config for anything that is printed,
// serialised or diffed, and Rendered.Deployable() for the bytes handed
// to runtime.Deploy and hashed into config_hash.
func RenderHOCONWithSecrets(templateDir string, i *intent.Intent, node *intent.NodeSpec) (Rendered, error) {
	data, err := LoadTemplate(templateDir, i.Network)
	if err != nil {
		return Rendered{}, err
	}

	config := string(data)

	// 1. Targeted line-level rewrites.
	if node.Ports.SolidityGRPC != 0 && !hasHOCONBlock(config, "rpc") {
		return Rendered{}, fmt.Errorf("cannot render ports.solidity_grpc: rpc block not found")
	}
	config = applyPortOverrides(config, node)
	config = applyFeatureOverrides(config, node)
	if err := checkHTTPPortConflicts(config, node); err != nil {
		return Rendered{}, err
	}

	// Monitoring: auto-enable prometheus metrics in HOCON.
	if i.Monitoring != nil && i.Monitoring.Enabled != nil && *i.Monitoring.Enabled {
		config = ensureMetricsForMonitoring(config)
	}

	// 2. Trailing override block (network_overrides + witness_key + config_overrides).
	ap := renderHOCONAppendix(node)
	if ap.deploy == "" {
		// Nothing appended: both forms are the same bytes.
		return Rendered{Config: config, deploy: config}, nil
	}
	if !strings.HasSuffix(config, "\n") {
		config += "\n"
	}
	return Rendered{
		Config:     config + "\n" + ap.display,
		WitnessKey: ap.key,
		Redacted:   ap.redacted,
		deploy:     config + "\n" + ap.deploy,
	}, nil
}

// ValidateIntentHTTPPortConflicts performs the same check early, but only
// when the intent explicitly sets HTTP port. Defaults and auto_ports are
// left to the authoritative render-time check.
func ValidateIntentHTTPPortConflicts(templateDir string, parsed, raw *intent.Intent) error {
	if raw.Target.AutoPorts {
		return nil
	}
	for idx := range parsed.Nodes {
		node := &parsed.Nodes[idx]
		if _, err := RenderHOCONWithSecrets(templateDir, parsed, node); err != nil {
			return err
		}
	}
	return nil
}

// hoconAppendix is the "trond overrides" block in its two forms. The
// two differ in at most one line — the `localwitness` assignment.
type hoconAppendix struct {
	deploy   string
	display  string
	key      security.PrivateKey
	redacted bool
}

// renderHOCONAppendix produces the "trond overrides" block. Returns the
// zero hoconAppendix (empty forms) when nothing is configured so the
// rendered HOCON stays identical to today's output for users who don't
// use the new fields.
//
// Witness-key handling: java-tron parses HOCON via typesafe-config but
// does NOT enable environment-variable substitution — `${VAR}` is treated
// as an internal config-reference and fails when the referenced key
// doesn't exist (the witness silently shuts down with "private key must
// be 64 hex string, actual: 9", that 9 being the literal length of
// `${SR_KEY}`). So we inline the env value at render time.
//
// The inlined secret goes ONLY into the deploy form. The display form
// gets a `<REDACTED:ENV_NAME>` placeholder, so the string that trond
// prints, JSON-encodes and returns over MCP never carries the key —
// only the env var's name, which the intent file already states in
// cleartext.
func renderHOCONAppendix(node *intent.NodeSpec) hoconAppendix {
	var lines []string

	// Index of the `localwitness` line inside lines, and the redacted
	// replacement for it. -1 means "no secret was inlined", in which
	// case both rendered forms are the same bytes.
	witnessIdx := -1
	witnessDisplayLine := ""
	var witnessKey security.PrivateKey

	// --- network_overrides ---
	no := &node.NetworkOverrides
	if no.Seeds != nil {
		lines = append(lines, "seed.node.ip.list = "+hoconStringList(*no.Seeds))
	}
	if no.ActivePeers != nil {
		lines = append(lines, "node.active = "+hoconStringList(*no.ActivePeers))
	}
	if no.PassivePeers != nil {
		lines = append(lines, "node.passive = "+hoconStringList(*no.PassivePeers))
	}
	if no.P2PVersion != nil {
		lines = append(lines, fmt.Sprintf("node.p2p.version = %d", *no.P2PVersion))
	}
	if no.Discovery != nil {
		lines = append(lines, fmt.Sprintf("node.discovery.enable = %t", *no.Discovery))
	}
	if no.MaxConnections != nil {
		lines = append(lines, fmt.Sprintf("node.maxConnections = %d", *no.MaxConnections))
	}
	if no.MaxActiveSameIP != nil {
		lines = append(lines, fmt.Sprintf("node.maxActiveNodesWithSameIp = %d", *no.MaxActiveSameIP))
	}
	if no.NeedSyncCheck != nil {
		lines = append(lines, fmt.Sprintf("block.needSyncCheck = %t", *no.NeedSyncCheck))
	}

	// --- witness_key ---
	if node.Type == "witness" {
		// Resolve from either the structured block or the legacy field.
		envName := ""
		keystore := ""
		var accountAddress string
		if node.WitnessKey != nil {
			envName = node.WitnessKey.PrivateKeyEnv
			keystore = node.WitnessKey.KeystorePath
			accountAddress = node.WitnessKey.AccountAddress
		}
		if envName == "" {
			envName = node.WitnessKeyEnv
		}

		switch {
		case envName != "":
			// Inline the resolved value at render time. typesafe-config
			// won't substitute ${ENV} for us, so we have to do it here.
			// If the env is unset at render time we emit a single-quoted
			// placeholder that java-tron will reject loudly — better than
			// silently rendering an empty key.
			val := os.Getenv(envName)
			if val == "" {
				// No secret to protect: the loud placeholder is
				// identical in both forms, exactly as before.
				lines = append(lines, fmt.Sprintf(`%s = [%q]`, witnessKeyName, "<UNSET:"+envName+">"))
				break
			}
			witnessKey = security.NewPrivateKey(val)
			witnessIdx = len(lines)
			witnessDisplayLine = fmt.Sprintf(`%s = [%q]`, witnessKeyName, "<REDACTED:"+envName+">")
			lines = append(lines, fmt.Sprintf(`%s = [%q]`, witnessKeyName, val))
		case keystore != "":
			lines = append(lines, fmt.Sprintf("localwitnesskeystore = [%q]", keystore))
		}
		if accountAddress != "" {
			lines = append(lines, fmt.Sprintf("localWitnessAccountAddress = %q", accountAddress))
		}
	}

	// --- config_overrides (sorted for determinism) ---
	if len(node.ConfigOverrides) > 0 {
		keys := make([]string, 0, len(node.ConfigOverrides))
		for k := range node.ConfigOverrides {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			lines = append(lines, fmt.Sprintf("%s = %s", k, hoconValue(node.ConfigOverrides[k])))
		}
	}

	if len(lines) == 0 {
		return hoconAppendix{}
	}

	deploy := joinAppendixLines(lines)
	if witnessIdx < 0 {
		return hoconAppendix{deploy: deploy, display: deploy}
	}
	displayLines := slices.Clone(lines)
	displayLines[witnessIdx] = witnessDisplayLine
	return hoconAppendix{
		deploy:   deploy,
		display:  joinAppendixLines(displayLines),
		key:      witnessKey,
		redacted: true,
	}
}

// joinAppendixLines wraps the override lines in the block header. Split
// out so the deploy and display forms are assembled by identical code —
// they must not drift in anything but the witness line.
func joinAppendixLines(lines []string) string {
	var sb strings.Builder
	sb.WriteString("# === trond overrides (last-write-wins) ===\n")
	for _, l := range lines {
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	return sb.String()
}

// hoconStringList serialises a Go []string as a HOCON list of quoted strings.
func hoconStringList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// hoconValue renders an arbitrary intent value (from config_overrides) as
// the closest HOCON literal. Strings are double-quoted; numbers and bools
// pass through; lists / maps are JSON-serialised, which HOCON accepts.
// hoconValue renders one config_overrides value as a HOCON literal.
//
// HOCON is a superset of JSON, so a JSON encoder is the correct renderer for
// everything except numbers. fmt is not: it produces Go syntax, and Go syntax
// is only accidentally JSON. Two real defects this replaces:
//
//   - %v on a slice or map emitted Go's own container syntax —
//     `[map[address:T… voteCount:5000]]` — which no HOCON parser accepts. That
//     made every list-valued override unusable, including
//     `genesis.block.witnesses`, i.e. multi-witness private networks could not
//     be expressed in an intent at all.
//   - %q on a string holding a control byte emitted `\x01`. That is a Go
//     escape, not a JSON/HOCON one, so the rendered config failed to parse.
//
// Numbers stay on fmt deliberately: %v already yields JSON-compatible output
// for every numeric type YAML can produce (including the 1e+06 form, which the
// JSON number grammar allows), and routing them through the encoder would
// change existing rendered configs for no correctness gain.
//
// HTML escaping is disabled so URLs, & and friends survive verbatim rather
// than turning into &.
func hoconValue(v any) string {
	switch x := v.(type) {
	case bool:
		return fmt.Sprintf("%t", x)
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return fmt.Sprintf("%v", x)
	default:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(x); err != nil {
			// Unreachable for anything YAML can unmarshal into `any`;
			// kept so an exotic value degrades instead of panicking.
			return fmt.Sprintf("%q", fmt.Sprintf("%v", x))
		}
		return strings.TrimRight(buf.String(), "\n")
	}
}

// sortStrings is a thin wrapper around sort.Strings so the appendix
// renderer reads cleanly. (Earlier this file hand-rolled an insertion
// sort to "avoid importing sort twice"; Go imports are per-file so the
// rationale was wrong.)
func sortStrings(s []string) {
	sort.Strings(s)
}

// applyPortOverrides patches port settings in the HOCON config.
func applyPortOverrides(config string, node *intent.NodeSpec) string {
	ports := node.Ports

	if ports.HTTP != 0 {
		config = replaceHOCONValue(config, "fullNodePort", fmt.Sprintf("%d", ports.HTTP))
	}
	if ports.GRPC != 0 {
		config = replaceRPCPort(config, ports.GRPC)
	}
	if ports.SolidityGRPC != 0 {
		config = replaceSolidityGRPCPort(config, ports.SolidityGRPC)
	}
	if ports.SolidityHTTP != 0 {
		config = replaceHOCONValue(config, "solidityPort", fmt.Sprintf("%d", ports.SolidityHTTP))
	}
	if ports.P2P != 0 {
		config = replaceListenPort(config, ports.P2P)
	}
	if ports.JSONRPC != 0 {
		// Task #165: features.jsonrpc=true used to enable the service but
		// leave httpFullNodePort commented out, so java-tron fell back to
		// its internal default 8545 while docker bound the intent's port.
		// Wiring this here means features.jsonrpc + ports.jsonrpc compose
		// correctly. ensureJSONRPCEnabled (called from applyFeatureOverrides)
		// still handles the enable bit; this one handles the port.
		config = replaceJSONRPCPort(config, ports.JSONRPC)
	}
	if ports.Metrics != 0 {
		// node.metrics.prometheus.port — the metrics endpoint follows the
		// same shape as jsonrpc: the template ships with a default of 9527
		// and trond needs to plumb the intent value through. Untested in
		// production but shipped alongside the JSONRPC fix because the
		// failure mode would be symmetric.
		config = replaceMetricsPort(config, ports.Metrics)
	}

	return config
}

// checkHTTPPortConflicts rejects collisions among the HTTP services that are
// rendered by the template. Explicit config_overrides are applied in the
// appendix and therefore remain authoritative; an operator who explicitly
// sets a colliding key has taken responsibility for that configuration.
func checkHTTPPortConflicts(config string, node *intent.NodeSpec) error {
	full, fullOK := hoconPortValue(config, "fullNodePort")
	solidity, solidityOK := hoconPortValue(config, "solidityPort")
	pbft, pbftOK := hoconPortValue(config, "PBFTPort")
	if !fullOK {
		return nil
	}
	if solidityOK && full == solidity && !hasHTTPOverride(node, "solidityPort") {
		return fmt.Errorf("rendered HTTP port conflict: fullNodePort=%d collides with solidityPort (in-container). Set ports.solidity_http in the intent (or config_overrides \"node.http.solidityPort\") to a different port", full)
	}
	if pbftOK && full == pbft && !hasHTTPOverride(node, "PBFTPort") {
		return fmt.Errorf("rendered HTTP port conflict: fullNodePort=%d collides with PBFTPort (in-container). Set config_overrides \"node.http.PBFTPort\" to a different port", full)
	}
	if solidityOK && pbftOK && solidity == pbft && !hasHTTPOverride(node, "solidityPort") && !hasHTTPOverride(node, "PBFTPort") {
		return fmt.Errorf("rendered HTTP port conflict: solidityPort=%d collides with PBFTPort; set a different ports.solidity_http or config_overrides value", solidity)
	}
	return nil
}

func hasHTTPOverride(node *intent.NodeSpec, key string) bool {
	_, ok := node.ConfigOverrides["node.http."+key]
	return ok
}

// hoconPortValue returns the first active integer assignment for key. Port
// names occur in both HTTP and RPC sections; the first occurrence is the HTTP
// template value, matching replaceHOCONValue's existing behavior.
func hoconPortValue(config, key string) (int, bool) {
	for _, line := range strings.Split(config, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") ||
			(!strings.HasPrefix(trimmed, key+" =") && !strings.HasPrefix(trimmed, key+"=")) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(trimmed, key+" ="), key+"="))
		var port int
		if _, err := fmt.Sscanf(value, "%d", &port); err == nil {
			return port, true
		}
	}
	return 0, false
}

// applyFeatureOverrides enables/disables features in the HOCON config.
func applyFeatureOverrides(config string, node *intent.NodeSpec) string {
	features := node.Features

	if features.JSONRPC != nil && *features.JSONRPC {
		// Ensure jsonrpc block has httpFullNodeEnable = true
		config = ensureJSONRPCEnabled(config)
	}

	if features.Metrics != nil && *features.Metrics {
		// Ensure node.metrics.prometheus.enable = true so the bound
		// metrics port actually serves data (symmetric to JSONRPC).
		// No-op on templates without a prometheus block (Nile/private).
		config = ensureMetricsEnabled(config)
	}

	return config
}

// replaceHOCONValue replaces a simple key = value pattern in HOCON.
func replaceHOCONValue(config, key, newValue string) string {
	lines := strings.Split(config, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+" =") || strings.HasPrefix(trimmed, key+"=") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = fmt.Sprintf("%s%s = %s", indent, key, newValue)
			break
		}
	}
	return strings.Join(lines, "\n")
}

// replaceRPCPort replaces the gRPC port in the rpc block.
func replaceRPCPort(config string, port int) string {
	lines := strings.Split(config, "\n")
	inRPC := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "rpc {" || strings.HasPrefix(trimmed, "rpc {") {
			inRPC = true
			continue
		}
		if inRPC && strings.HasPrefix(trimmed, "port") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = fmt.Sprintf("%sport = %d", indent, port)
			break
		}
		if inRPC && trimmed == "}" {
			inRPC = false
		}
	}
	return strings.Join(lines, "\n")
}

func replaceSolidityGRPCPort(config string, port int) string {
	lines := strings.Split(config, "\n")
	inRPC := false
	depth := 0
	rpcDepth := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inRPC && strings.HasPrefix(trimmed, "rpc") && strings.Contains(trimmed, "{") {
			inRPC = true
			rpcDepth = depth + strings.Count(line, "{") - strings.Count(line, "}")
			continue
		}
		if inRPC {
			key := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
			if strings.HasPrefix(key, "solidityPort") {
				lines[i] = fmt.Sprintf("%ssolidityPort = %d", lineIndent(line), port)
				return strings.Join(lines, "\n")
			}
			if trimmed == "}" && depth+strings.Count(line, "{")-strings.Count(line, "}") < rpcDepth {
				lines = append(lines[:i], append([]string{"  solidityPort = " + fmt.Sprintf("%d", port)}, lines[i:]...)...)
				return strings.Join(lines, "\n")
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
	}
	return strings.Join(lines, "\n")
}

func hasHOCONBlock(config, name string) bool {
	for _, line := range strings.Split(config, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, name) && strings.Contains(trimmed, "{") {
			return true
		}
	}
	return false
}

// replaceListenPort replaces the P2P listen port.
func replaceListenPort(config string, port int) string {
	lines := strings.Split(config, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "listen.port") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = fmt.Sprintf("%slisten.port = %d", indent, port)
			break
		}
	}
	return strings.Join(lines, "\n")
}

// replaceJSONRPCPort sets node.jsonrpc.httpFullNodePort to port.
// The template ships with the line commented out (`# httpFullNodePort
// = 8545`); we uncomment + set when an intent provides a value. If the
// line is missing entirely (operator-edited config), we insert one
// before the closing brace, indented to match the surrounding block
// so the synthesised line aligns with sibling keys.
func replaceJSONRPCPort(config string, port int) string {
	lines := strings.Split(config, "\n")
	inJSONRPC := false
	jsonRPCIndent := "" // indent of a sibling key inside the block, for synthesis
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "jsonrpc") && strings.Contains(trimmed, "{") {
			inJSONRPC = true
			continue
		}
		if !inJSONRPC {
			continue
		}
		// Match both commented and uncommented forms — the default
		// template has it commented out.
		uncommented := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		if strings.HasPrefix(uncommented, "httpFullNodePort") {
			indent := lineIndent(line)
			lines[i] = fmt.Sprintf("%shttpFullNodePort = %d", indent, port)
			return strings.Join(lines, "\n")
		}
		// Capture the indent of the first sibling key seen so the
		// synthesis path lays the new line down at the same column —
		// templates with 2-space or 4-space indentation both work.
		if jsonRPCIndent == "" && trimmed != "" && !strings.HasPrefix(trimmed, "#") && trimmed != "}" {
			jsonRPCIndent = lineIndent(line)
		}
		if trimmed == "}" {
			// jsonrpc block ended without the key — synthesise it.
			if jsonRPCIndent == "" {
				jsonRPCIndent = "    " // fallback when the block was empty
			}
			lines[i] = fmt.Sprintf("%shttpFullNodePort = %d\n%s", jsonRPCIndent, port, line)
			return strings.Join(lines, "\n")
		}
	}
	return config
}

// replaceMetricsPort sets node.metrics.prometheus.port to port. The key
// lives at depth 2 inside node.metrics (node.metrics { prometheus {
// port = N } }), so we count brace depth rather than tracking a pair
// of boolean flags — the boolean approach broke if prometheus wasn't
// the first sub-block of node.metrics.
func replaceMetricsPort(config string, port int) string {
	lines := strings.Split(config, "\n")
	depth := 0 // brace depth relative to node.metrics's open brace
	prometheus := false
	prometheusDepth := -1
	inMetrics := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inMetrics {
			if strings.HasPrefix(trimmed, "node.metrics") && strings.Contains(trimmed, "{") {
				inMetrics = true
				depth = 1
			}
			continue
		}
		// Track brace depth so we know exactly where prometheus opens
		// and closes regardless of sibling order.
		opens := strings.Count(line, "{")
		closes := strings.Count(line, "}")
		preDepth := depth
		depth += opens - closes
		if !prometheus && strings.HasPrefix(trimmed, "prometheus") && opens > 0 {
			prometheus = true
			prometheusDepth = preDepth + 1 // depth INSIDE the prometheus block
			continue
		}
		if prometheus && depth >= prometheusDepth && strings.HasPrefix(trimmed, "port") {
			lines[i] = fmt.Sprintf("%sport = %d", lineIndent(line), port)
			return strings.Join(lines, "\n")
		}
		if prometheus && depth < prometheusDepth {
			// Walked past the prometheus block without finding `port`.
			prometheus = false
			prometheusDepth = -1
		}
		if depth == 0 {
			break // exited node.metrics entirely
		}
	}
	return strings.Join(lines, "\n")
}

// ensureMetricsEnabled sets node.metrics.prometheus.enable = true so a
// rendered `features.metrics: true` actually serves metrics. Without
// this, applyFeatureOverrides wired only JSONRPC and left the template's
// `enable = false` intact — so compose.go bound the metrics port while
// java-tron published nothing on it. That's the exact symmetric bug
// #165 fixed for jsonrpc.
//
// Same brace-depth walk as replaceMetricsPort. SAFE NO-OP when there is
// no prometheus block (the Nile/private templates lack one, tracked as
// #167) — returns the config unchanged rather than synthesising a block,
// so it never corrupts a template that doesn't support metrics.
func ensureMetricsEnabled(config string) string {
	lines := strings.Split(config, "\n")
	depth := 0
	prometheus := false
	prometheusDepth := -1
	inMetrics := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inMetrics {
			if strings.HasPrefix(trimmed, "node.metrics") && strings.Contains(trimmed, "{") {
				inMetrics = true
				depth = 1
			}
			continue
		}
		opens := strings.Count(line, "{")
		closes := strings.Count(line, "}")
		preDepth := depth
		depth += opens - closes
		if !prometheus && strings.HasPrefix(trimmed, "prometheus") && opens > 0 {
			prometheus = true
			prometheusDepth = preDepth + 1
			continue
		}
		if prometheus && depth >= prometheusDepth && strings.HasPrefix(trimmed, "enable") {
			lines[i] = fmt.Sprintf("%senable = true", lineIndent(line))
			return strings.Join(lines, "\n")
		}
		if prometheus && depth < prometheusDepth {
			prometheus = false
			prometheusDepth = -1
		}
		if depth == 0 {
			break
		}
	}
	return strings.Join(lines, "\n")
}

// lineIndent returns the leading whitespace (spaces or tabs) of a line.
// Centralised so each replacer doesn't redo the same slice arithmetic.
func lineIndent(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// ensureJSONRPCEnabled ensures the jsonrpc block has httpFullNodeEnable = true.
func ensureJSONRPCEnabled(config string) string {
	lines := strings.Split(config, "\n")
	inJSONRPC := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "jsonrpc") && strings.Contains(trimmed, "{") {
			inJSONRPC = true
			continue
		}
		if inJSONRPC {
			if strings.Contains(trimmed, "httpFullNodeEnable") {
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				lines[i] = fmt.Sprintf("%shttpFullNodeEnable = true", indent)
				return strings.Join(lines, "\n")
			}
			if trimmed == "}" {
				// Insert before closing brace
				indent := "    "
				lines[i] = indent + "httpFullNodeEnable = true\n" + line
				return strings.Join(lines, "\n")
			}
		}
	}
	return config
}

// ensureMetricsForMonitoring sets node.metrics.prometheus.enable = true in
// the HOCON config, synthesising the surrounding blocks when missing. This
// is the active form used when monitoring.enabled=true — the user has
// explicitly opted in to monitoring, so we *do* want to inject a prometheus
// block into templates (nile/private) that lack one. Compare the simpler
// SAFE NO-OP variant `ensureMetricsEnabled` used by features.metrics, which
// only flips an existing flag and never synthesises blocks.
//
// Three cases:
//  1. node.metrics { prometheus { enable = false } } → set to true
//  2. node.metrics { prometheus { ... } } (no enable) → insert enable = true
//  3. node.metrics block missing entirely → append a new block
func ensureMetricsForMonitoring(config string) string {
	lines := strings.Split(config, "\n")
	inMetrics := false
	inPrometheus := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "node.metrics") && strings.Contains(trimmed, "{") {
			inMetrics = true
			continue
		}
		if inMetrics {
			if strings.HasPrefix(trimmed, "prometheus") && strings.Contains(trimmed, "{") {
				inPrometheus = true
				continue
			}
			if inPrometheus {
				if strings.Contains(trimmed, "enable") {
					indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
					lines[i] = fmt.Sprintf("%senable = true", indent)
					return strings.Join(lines, "\n")
				}
				if trimmed == "}" {
					indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
					lines[i] = indent + "enable = true\n" + line
					return strings.Join(lines, "\n")
				}
			}
			if trimmed == "}" && !inPrometheus {
				// node.metrics block exists but no prometheus sub-block
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				lines[i] = indent + "prometheus {\n" + indent + "  enable = true\n" + indent + "}\n" + line
				return strings.Join(lines, "\n")
			}
		}
	}
	// node.metrics block missing entirely — append to end
	if !strings.HasSuffix(config, "\n") {
		config += "\n"
	}
	config += "\nnode.metrics {\n  prometheus {\n    enable = true\n  }\n}\n"
	return config
}
