package apply

import (
	"crypto/sha256"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// EffectiveIntentHash hashes the fully-resolved intent used for deployment.
func EffectiveIntentHash(effective []byte) string {
	h := sha256.New()
	h.Write([]byte("intent-hash-v2\x00"))
	h.Write(effective)
	return fmt.Sprintf("v2:%x", h.Sum(nil))
}

// LegacyIntentHashMatches accepts hashes written before the versioned hash
// format, while ensuring monitoring state did not change underneath them.
func LegacyIntentHashMatches(raw, canonical []byte, existing *state.ManagedNode) bool {
	if existing == nil {
		return false
	}
	stored := existing.IntentHash
	if stored == "" || len(stored) > 3 && stored[:3] == "v2:" {
		return false
	}
	var rawIntent, canonicalIntent intent.Intent
	if err := yaml.Unmarshal(raw, &rawIntent); err != nil {
		return false
	}
	if err := yaml.Unmarshal(canonical, &canonicalIntent); err != nil {
		return false
	}
	if IntentHashFromBytes(raw) == stored {
		return monitoringEnabled(rawIntent.Monitoring) == monitoringEnabled(canonicalIntent.Monitoring) &&
			monitoringEnabled(canonicalIntent.Monitoring) == recordedMonitoringEnabled(existing)
	}
	return IntentHashFromBytes(canonical) == stored &&
		monitoringEnabled(canonicalIntent.Monitoring) == recordedMonitoringEnabled(existing)
}

func monitoringEnabled(m *intent.Monitoring) bool {
	return m != nil && m.Enabled != nil && *m.Enabled
}

func recordedMonitoringEnabled(existing *state.ManagedNode) bool {
	return existing != nil && existing.Monitoring != nil && existing.Monitoring.Enabled
}

// RestoreAutoPorts restores persisted ports into an intent. The optional
// explicit is optionally the raw, pre-defaults intent node. Its non-zero
// ports win over persisted values; zero is the documented unset convention.
// The optional form preserves compatibility with callers without raw intent.
func RestoreAutoPorts(node *intent.NodeSpec, existing *state.ManagedNode, explicit ...*intent.NodeSpec) {
	var userPorts *intent.PortMapping
	if len(explicit) > 0 && explicit[0] != nil {
		userPorts = &explicit[0].Ports
	}
	if existing == nil {
		return
	}
	restore := func(current, persisted, user int) int {
		if userPorts != nil && user != 0 {
			return current
		}
		if persisted != 0 {
			return persisted
		}
		return current
	}
	node.Ports.HTTP = restore(node.Ports.HTTP, existing.HTTPPort, portValue(userPorts, func(p intent.PortMapping) int { return p.HTTP }))
	node.Ports.GRPC = restore(node.Ports.GRPC, existing.GRPCPort, portValue(userPorts, func(p intent.PortMapping) int { return p.GRPC }))
	node.Ports.P2P = restore(node.Ports.P2P, existing.P2PPort, portValue(userPorts, func(p intent.PortMapping) int { return p.P2P }))
	node.Ports.Metrics = restore(node.Ports.Metrics, existing.MetricsPort, portValue(userPorts, func(p intent.PortMapping) int { return p.Metrics }))
	node.Ports.SolidityHTTP = restore(node.Ports.SolidityHTTP, existing.SolidityHTTPPort, portValue(userPorts, func(p intent.PortMapping) int { return p.SolidityHTTP }))
	node.Ports.SolidityGRPC = restore(node.Ports.SolidityGRPC, existing.SolidityGRPCPort, portValue(userPorts, func(p intent.PortMapping) int { return p.SolidityGRPC }))
	node.Ports.JSONRPC = restore(node.Ports.JSONRPC, existing.JSONRPCPort, portValue(userPorts, func(p intent.PortMapping) int { return p.JSONRPC }))
}

func portValue(ports *intent.PortMapping, get func(intent.PortMapping) int) int {
	if ports == nil {
		return 0
	}
	return get(*ports)
}
