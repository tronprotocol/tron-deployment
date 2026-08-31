package config

import (
	"fmt"
	"io"
	"strings"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/render"
)

// printExplain emits a per-field breakdown of the parsed intent: explicit
// user value, default fill-in, or "not set" with a downstream-impact note
// where helpful. The goal is to give a new user (or a reviewer) a single
// view that answers "what will trond actually do with this intent?"
//
// The layout intentionally mirrors the README "Intent Reference" so a
// reader can cross-check fields one-to-one. Lines start with one of:
//
//	✓ value-as-set
//	· default-applied
//	⚠ value-missing-but-might-matter
//	→  derived-side-effect (e.g. JVM heap size from resources.memory)
func printExplain(w io.Writer, raw, finalIntent *intent.Intent) {
	fmt.Fprintf(w, "Intent: %s\n", finalIntent.Name)
	fmt.Fprintln(w)

	explainTopLevel(w, raw, finalIntent)
	for i := range finalIntent.Nodes {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Node %d:\n", i)
		var rawNode *intent.NodeSpec
		if i < len(raw.Nodes) {
			rawNode = &raw.Nodes[i]
		}
		explainNode(w, rawNode, &finalIntent.Nodes[i])
	}
}

func explainTopLevel(w io.Writer, raw, final *intent.Intent) {
	row(w, "✓", "name", final.Name, false)
	row(w, "✓", "network", final.Network, false)

	t := final.Target
	rt := raw.Target
	row(w, "✓", "target.type", t.Type, false)
	row(w, mark(rt.Runtime), "target.runtime", t.Runtime, rt.Runtime == "")
	if t.Type == "ssh" {
		row(w, "✓", "target.host", t.Host, false)
		row(w, "✓", "target.user", t.User, false)
		row(w, mark(intToStr(rt.Port)), "target.port", intToStr(t.Port), rt.Port == 0)
		row(w, mark(rt.IdentityFile), "target.identity_file", t.IdentityFile, rt.IdentityFile == "")
	}
	if t.AutoPorts {
		row(w, "✓", "target.auto_ports", "true (every port replaced with OS-allocated)", false)
	} else {
		row(w, "·", "target.auto_ports", "false (use intent's explicit ports)", false)
	}
}

func explainNode(w io.Writer, raw, final *intent.NodeSpec) {
	r := raw
	if r == nil {
		r = &intent.NodeSpec{}
	}

	row(w, "✓", "type", final.Type, false)
	row(w, mark(r.Version), "version", final.Version, r.Version == "")
	row(w, mark(r.Image), "image", final.Image, r.Image == "")

	if final.Type == "witness" {
		explainWitnessKey(w, final)
	}

	// Resources + JVM
	mem := final.Resources.Memory
	rawMem := r.Resources.Memory
	row(w, mark(rawMem), "resources.memory", mem, rawMem == "")
	memGB, memErr := render.ParseMemoryGB(mem)
	if memErr != nil {
		fmt.Fprintf(w, "    !  JVM heap not derived: invalid resources.memory %q (%v)\n", mem, memErr)
		return
	}
	heap := render.JVMArgsString(memGB, 17, final.JVM)
	xmx := firstFlag(heap, "-Xmx")
	if xmx != "" {
		fmt.Fprintf(w, "    →  JVM heap derived: %s\n", xmx)
	}
	if final.Resources.CPU != "" {
		row(w, "✓", "resources.cpu", final.Resources.CPU, false)
	}

	// Restart
	if final.Restart != "" {
		row(w, "✓", "restart", final.Restart, false)
	} else {
		row(w, "·", "restart", "unless-stopped (default)", false)
	}

	// JVM tuning
	if final.JVM != nil {
		gc := final.JVM.GC
		if gc == "auto" || gc == "" {
			row(w, "·", "jvm.gc", "auto (no GC flags emitted, lets the image decide)", false)
		} else {
			row(w, "✓", "jvm.gc", gc+" (explicit)", false)
		}
		if final.JVM.GCLog != nil {
			row(w, "✓", "jvm.gc_log", boolStr(*final.JVM.GCLog), false)
		} else {
			row(w, "·", "jvm.gc_log", "off (default)", false)
		}
	}

	// Ports
	explainPorts(w, r, final)

	// Storage
	explainStorage(w, r, final)

	// Features (tri-state)
	explainFeatures(w, r, final)

	// Network overrides
	explainNetworkOverrides(w, r, final)

	// Compose-only fields, if set
	if len(final.Networks) > 0 {
		row(w, "✓", "networks", strings.Join(final.Networks, ","), false)
	}
	if len(final.DependsOn) > 0 {
		row(w, "✓", "depends_on", strings.Join(final.DependsOn, ","), false)
	}
	if final.Healthcheck != nil {
		row(w, "✓", "healthcheck", "configured", false)
	}
	if final.Ulimits != nil && final.Ulimits.NOFile > 0 {
		row(w, "✓", "ulimits.nofile", intToStr(final.Ulimits.NOFile), false)
	}
	if len(final.Labels) > 0 {
		row(w, "✓", "labels", fmt.Sprintf("%d label(s)", len(final.Labels)), false)
	}
	if len(final.ExtraEnv) > 0 {
		row(w, "✓", "extra_env", fmt.Sprintf("%d var(s)", len(final.ExtraEnv)), false)
	}
	if len(final.ExtraArgs) > 0 {
		row(w, "✓", "extra_args", strings.Join(final.ExtraArgs, " "), false)
	}
	if len(final.ConfigOverrides) > 0 {
		row(w, "✓", "config_overrides", fmt.Sprintf("%d HOCON key(s)", len(final.ConfigOverrides)), false)
	}
}

func explainWitnessKey(w io.Writer, n *intent.NodeSpec) {
	switch {
	case n.WitnessKey != nil && n.WitnessKey.PrivateKeyEnv != "":
		row(w, "✓", "witness_key.private_key_env", n.WitnessKey.PrivateKeyEnv+
			" (value will be inlined into HOCON at apply time)", false)
	case n.WitnessKey != nil && n.WitnessKey.KeystorePath != "":
		row(w, "✓", "witness_key.keystore_path", n.WitnessKey.KeystorePath, false)
	case n.WitnessKeyEnv != "":
		row(w, "✓", "witness_key_env", n.WitnessKeyEnv+" (legacy form, still supported)", false)
	default:
		row(w, "⚠", "witness_key", "missing — witness node will fail validation", false)
	}
}

func explainPorts(w io.Writer, raw, final *intent.NodeSpec) {
	// We can't see auto_ports here directly (it's on Target), so the
	// caller who passed auto-allocated ports through ApplyDefaults gives us
	// values in the >=49152 ephemeral range while raw is still zero. We
	// detect that and label them differently.
	type portField struct {
		name string
		raw  int
		val  int
	}
	fields := []portField{
		{"http", raw.Ports.HTTP, final.Ports.HTTP},
		{"grpc", raw.Ports.GRPC, final.Ports.GRPC},
		{"p2p", raw.Ports.P2P, final.Ports.P2P},
		{"jsonrpc", raw.Ports.JSONRPC, final.Ports.JSONRPC},
		{"metrics", raw.Ports.Metrics, final.Ports.Metrics},
	}
	for _, f := range fields {
		switch {
		case f.raw != 0:
			row(w, "✓", "ports."+f.name, intToStr(f.val), false)
		case f.val >= 32768:
			// Ephemeral / auto-allocated by target.auto_ports.
			fmt.Fprintf(w, "  · %-40s %d (auto-allocated)\n", "ports."+f.name, f.val)
		default:
			fmt.Fprintf(w, "  · %-40s %d (default)\n", "ports."+f.name, f.val)
		}
	}
}

func explainStorage(w io.Writer, _, final *intent.NodeSpec) {
	s := final.Storage
	switch {
	case s.StoragePath != "":
		row(w, "✓", "storage.path", s.StoragePath+" (data → "+s.StoragePath+"/data, logs → "+s.StoragePath+"/logs)", false)
	case s.Data != "" || s.Logs != "":
		if s.Data != "" {
			row(w, "✓", "storage.data", s.Data, false)
		}
		if s.Logs != "" {
			row(w, "✓", "storage.logs", s.Logs, false)
		}
	default:
		row(w, "·", "storage", "default (named volumes <name>-data / <name>-logs)", false)
	}
}

func explainFeatures(w io.Writer, _, final *intent.NodeSpec) {
	pairs := []struct {
		name  string
		value *bool
	}{
		{"features.metrics", final.Features.Metrics},
		{"features.jsonrpc", final.Features.JSONRPC},
		{"features.rate_limit", final.Features.RateLimit},
		{"features.event_subscribe", final.Features.EventSubscribe},
	}
	for _, p := range pairs {
		if p.value == nil {
			row(w, "·", p.name, "use template default", false)
		} else {
			row(w, "✓", p.name, boolStr(*p.value), false)
		}
	}
}

func explainNetworkOverrides(w io.Writer, _, final *intent.NodeSpec) {
	o := final.NetworkOverrides
	if o.Seeds != nil {
		row(w, "✓", "network_overrides.seeds", fmt.Sprintf("%d seed(s) → seed.node.ip.list", len(*o.Seeds)), false)
	}
	if o.ActivePeers != nil {
		row(w, "✓", "network_overrides.active_peers", fmt.Sprintf("%d peer(s) → node.active", len(*o.ActivePeers)), false)
	} else if final.Type == "fullnode" || final.Type == "witness" {
		row(w, "·", "network_overrides.active_peers", "(network create will auto-wire to siblings)", false)
	}
	if o.PassivePeers != nil {
		row(w, "✓", "network_overrides.passive_peers", fmt.Sprintf("%d peer(s) → node.passive", len(*o.PassivePeers)), false)
	}
	if o.P2PVersion != nil {
		row(w, "✓", "network_overrides.p2p_version", intToStr(*o.P2PVersion)+" → node.p2p.version", false)
	}
	if o.Discovery != nil {
		row(w, "✓", "network_overrides.discovery", boolStr(*o.Discovery)+" → node.discovery.enable", false)
	}
	if o.NeedSyncCheck != nil {
		row(w, "✓", "network_overrides.need_sync_check", boolStr(*o.NeedSyncCheck)+" → block.needSyncCheck", false)
	}
	if o.MaxConnections != nil {
		row(w, "✓", "network_overrides.max_connections", intToStr(*o.MaxConnections), false)
	}
	if o.MaxActiveSameIP != nil {
		row(w, "✓", "network_overrides.max_active_same_ip", intToStr(*o.MaxActiveSameIP), false)
	}
}

func row(w io.Writer, icon, name, value string, isDefault bool) {
	if isDefault {
		fmt.Fprintf(w, "  · %-40s %s (default)\n", name, value)
		return
	}
	fmt.Fprintf(w, "  %s %-40s %s\n", icon, name, value)
}

func mark(rawValue string) string {
	if rawValue == "" {
		return "·"
	}
	return "✓"
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	return fmt.Sprintf("%d", n)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// firstFlag extracts the first JVM flag matching a prefix (e.g. "-Xmx").
// Returns "" when none.
func firstFlag(args, prefix string) string {
	for _, tok := range strings.Fields(args) {
		if strings.HasPrefix(tok, prefix) {
			return tok
		}
	}
	return ""
}
