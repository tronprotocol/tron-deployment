package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	applypkg "github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/schema"
	"github.com/tronprotocol/tron-deployment/internal/security"
)

// TestOutputSchemaContracts exercises the two output producers involved in
// the schema-drift incident. It uses the same applyResultMap and
// security.AuditEntry producers as the CLI, without starting a runtime.
func TestOutputSchemaContracts(t *testing.T) {
	t.Run("apply", func(t *testing.T) {
		for _, hash := range []string{
			"v2:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		} {
			result := applyResultMap(&applypkg.Result{
				Name: "contract-node", Outcome: "created", IntentHash: hash,
				ConfigHash: strings64('b'), Version: "4.8.1", Runtime: "docker",
				Network: "private", IsPrivate: true,
				Endpoints:       map[string]string{"http": "http://127.0.0.1:8090", "grpc": "127.0.0.1:50051"},
				Build:           &applypkg.BuildSummary{CacheKey: "0123456789ab-b01234567", SourceRevision: strings40('c'), Dirty: true, CacheHit: false, DurationMs: 12},
				MonitoringError: "warning", MonitoringEndpoints: map[string]string{"prometheus_url": "http://127.0.0.1:9090", "grafana_url": "http://127.0.0.1:3000"},
			}, 42)
			result["ready"] = true
			result["waited_ms"] = int64(7)
			result["wait_error"] = ""
			validateOutputSchema(t, "apply", result)
		}
	})

	t.Run("events", func(t *testing.T) {
		entry, err := json.Marshal(security.AuditEntry{
			Timestamp: time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC),
			Command:   "apply", Node: "contract-node", Target: "local", IntentHash: "v2:" + strings64('a'),
			Result: "success", DurationMs: 42, ErrorCode: "TEST", Detail: "intent.yaml", RunID: "run-123",
		})
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(entry, &value); err != nil {
			t.Fatal(err)
		}
		validateOutputSchema(t, "events", value)
	})

	t.Run("status intent hash", func(t *testing.T) {
		for _, hash := range []string{"v2:" + strings64('a'), strings64('b')} {
			if err := validateSchemaValue("status", map[string]any{
				"name": "contract-node", "status": "running", "intent_hash": hash,
			}); err != nil {
				t.Fatalf("valid intent_hash %q failed schema: %v", hash, err)
			}
		}
		if err := validateSchemaValue("status", map[string]any{
			"name": "contract-node", "status": "running", "intent_hash": "v2:xyz",
		}); err == nil {
			t.Fatal("invalid status intent_hash unexpectedly passed schema")
		}
	})

	// Synthetic status payload exercising the additive `monitoring` block
	// (schema 1.16.1): both ports must be integers, the probed health must
	// stay inside its enum, and all three keys are required.
	t.Run("status monitoring", func(t *testing.T) {
		if err := validateSchemaValue("status", map[string]any{
			"name": "contract-node", "status": "running",
			"monitoring": map[string]any{
				"prometheus_port": 9090,
				"grafana_port":    3000,
				"status":          "running",
			},
		}); err != nil {
			t.Fatalf("valid monitoring object failed schema: %v", err)
		}
		for name, mon := range map[string]map[string]any{
			"port not an integer":  {"prometheus_port": "9090", "grafana_port": 3000, "status": "running"},
			"status outside enum":  {"prometheus_port": 9090, "grafana_port": 3000, "status": "degraded"},
			"missing grafana_port": {"prometheus_port": 9090, "status": "running"},
		} {
			if err := validateSchemaValue("status", map[string]any{
				"name": "contract-node", "status": "running", "monitoring": mon,
			}); err == nil {
				t.Errorf("invalid monitoring (%s) unexpectedly passed schema", name)
			}
		}
	})

	// Mirror of the status intent-hash subtest above for inspect node
	// entries: versioned `v2:` and legacy bare digests are accepted;
	// everything else is refused.
	t.Run("inspect intent hash", func(t *testing.T) {
		nodeWithHash := func(hash string) map[string]any {
			return map[string]any{
				"nodes": []any{map[string]any{"name": "contract-node", "intent_hash": hash}},
			}
		}
		for _, hash := range []string{"v2:" + strings64('a'), strings64('b')} {
			if err := validateSchemaValue("inspect", nodeWithHash(hash)); err != nil {
				t.Fatalf("valid intent_hash %q failed schema: %v", hash, err)
			}
		}
		for _, hash := range []string{
			strings.ToUpper(strings64('c')), // uppercase hex is not [0-9a-f]
			"v3:" + strings64('d'),          // unknown version prefix
			strings64('e')[:63],             // truncated digest
		} {
			if err := validateSchemaValue("inspect", nodeWithHash(hash)); err == nil {
				t.Errorf("invalid inspect intent_hash %q unexpectedly passed schema", hash)
			}
		}
	})
}

func validateSchemaValue(name string, value any) error {
	doc, ok := schema.Get(name)
	if !ok {
		return fmt.Errorf("embedded schema %q missing", name)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	compiledDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	const id = "https://trond.test/schema.json"
	if err := compiler.AddResource(id, compiledDoc); err != nil {
		return err
	}
	compiled, err := compiler.Compile(id)
	if err != nil {
		return err
	}
	return compiled.Validate(value)
}

func validateOutputSchema(t *testing.T, name string, value any) {
	t.Helper()
	// Validate the JSON value, not Go structs/maps. This mirrors the CLI wire
	// boundary and applies JSON tags/omitempty exactly as output.WriteJSON does.
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	doc, ok := schema.Get(name)
	if !ok {
		t.Fatalf("embedded schema %q missing", name)
	}
	properties, ok := doc["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema %q has no properties", name)
	}
	actual, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s producer did not emit an object: %T", name, value)
	}
	expected := map[string]map[string]bool{
		"apply":  {"name": true, "result": true, "intent_hash": true, "runtime": true, "version": true, "network": true, "is_private": true, "endpoints": true, "duration_ms": true, "config_hash": true, "build": true, "monitoring_error": true, "monitoring": true, "ready": true, "waited_ms": true, "wait_error": true},
		"events": {"timestamp": true, "command": true, "node": true, "target": true, "intent_hash": true, "result": true, "duration_ms": true, "error_code": true, "detail": true, "run_id": true},
	}
	for key := range expected[name] {
		if _, ok := actual[key]; !ok {
			t.Errorf("%s producer omitted expected field %q", name, key)
		}
	}
	for key := range properties {
		if !expected[name][key] {
			t.Errorf("%s schema declares unexpected field %q; update the producer contract test", name, key)
		}
	}
	for key := range actual {
		if _, ok := properties[key]; !ok {
			t.Errorf("%s producer emitted %q but schema does not declare it", name, key)
		}
	}

	data, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	compiledDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const id = "https://trond.test/schema.json"
	if err := compiler.AddResource(id, compiledDoc); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(value); err != nil {
		t.Fatalf("%s output failed embedded schema: %v", name, err)
	}
}

func strings64(ch byte) string { return repeated(ch, 64) }
func strings40(ch byte) string { return repeated(ch, 40) }
func repeated(ch byte, n int) string {
	b := bytes.Repeat([]byte{ch}, n)
	return string(b)
}
