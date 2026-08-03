package snapshot

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/tronprotocol/tron-deployment/internal/snapshot"
)

// The mainnet mirrors serve cleartext HTTP with no HTTPS endpoint, so the
// only defence trond can offer is to make that visible. A human reads the
// stderr warning; an agent reads stdout. These tests pin the stdout half —
// that `plaintext_transport` is actually emitted, and that the payload
// carrying it still validates against the published contract.

const schemaRelPath = "../../schemas/output/snapshot-download.schema.json"

func TestDownloadPayload_CarriesTransportFactsAndValidates(t *testing.T) {
	src := snapshot.LookupDomain("34.143.247.77") // a cleartext mainnet mirror
	if src == nil {
		t.Fatal("expected the SG mainnet mirror in SourceTable")
	}
	res := &snapshot.DownloadResult{
		BytesDownloaded:    4096,
		Duration:           3 * time.Second,
		DurationMs:         3000,
		ActualMD5:          strings.Repeat("a", 32),
		FilesExtracted:     12,
		SHA256:             strings.Repeat("b", 64),
		PlaintextTransport: true,
	}
	pre := &snapshot.PreflightResult{PlaintextTransport: true}

	payload := downloadPayload(src, "backup20250115", "/tmp/dest", res, pre)

	got, ok := payload["plaintext_transport"].(bool)
	if !ok || !got {
		t.Errorf("plaintext_transport should be true for an http:// mirror, got %v", payload["plaintext_transport"])
	}
	if payload["sha256"] != res.SHA256 {
		t.Errorf("sha256 missing from payload: %v", payload["sha256"])
	}
	if v, ok := payload["sha256_verified"].(bool); !ok || v {
		t.Errorf("sha256_verified should be false with no pin, got %v", payload["sha256_verified"])
	}
	if _, present := payload["expected_sha256"]; present {
		t.Error("expected_sha256 must be omitted when no pin was supplied")
	}
	mustValidate(t, payload)
}

func TestDownloadPayload_HTTPSMirrorReportsFalse(t *testing.T) {
	src := snapshot.LookupDomain("snapshots.nileex.io") // the one HTTPS mirror
	if src == nil {
		t.Fatal("expected the nile mirror in SourceTable")
	}
	res := &snapshot.DownloadResult{
		BytesDownloaded:    4096,
		DurationMs:         3000,
		ActualMD5:          strings.Repeat("a", 32),
		FilesExtracted:     12,
		SHA256:             strings.Repeat("b", 64),
		PlaintextTransport: false,
	}
	payload := downloadPayload(src, "backup20260524", "/tmp/dest", res, &snapshot.PreflightResult{})

	if v, ok := payload["plaintext_transport"].(bool); !ok || v {
		t.Errorf("plaintext_transport should be false for the https:// nile mirror, got %v", payload["plaintext_transport"])
	}
	mustValidate(t, payload)
}

func TestDownloadPayload_WithPinValidates(t *testing.T) {
	src := snapshot.LookupDomain("34.143.247.77")
	pin := strings.Repeat("c", 64)
	res := &snapshot.DownloadResult{
		BytesDownloaded:    4096,
		DurationMs:         3000,
		ActualMD5:          strings.Repeat("a", 32),
		ExpectedMD5:        strings.Repeat("a", 32),
		MD5Verified:        true,
		FilesExtracted:     12,
		SHA256:             pin,
		ExpectedSHA256:     pin,
		SHA256Verified:     true,
		PlaintextTransport: true,
	}
	payload := downloadPayload(src, "backup20250115", "/tmp/dest", res, &snapshot.PreflightResult{})

	if payload["expected_sha256"] != pin {
		t.Errorf("expected_sha256 should echo the pin, got %v", payload["expected_sha256"])
	}
	if v, ok := payload["sha256_verified"].(bool); !ok || !v {
		t.Error("sha256_verified should be true when the pin matched")
	}
	mustValidate(t, payload)
}

// TestPlanPayload_PreflightCarriesTransportFact covers the --dry-run shape,
// where the operator decides whether to spend hours on the transfer at all.
func TestPlanPayload_PreflightCarriesTransportFact(t *testing.T) {
	src := snapshot.LookupDomain("34.143.247.77")
	pre := &snapshot.PreflightResult{
		URL:                snapshot.TarballURL(*src, "backup20250115", snapshot.DBKindLite),
		ExpectedSize:       1024,
		FreeBytes:          8192,
		NeededBytes:        2048,
		PlaintextTransport: true,
	}
	payload := planPayload(src, "backup20250115", "/tmp/dest", pre)

	round := roundTrip(t, payload)
	flight, ok := round["preflight"].(map[string]any)
	if !ok {
		t.Fatalf("preflight missing from plan payload: %#v", round)
	}
	if v, ok := flight["plaintext_transport"].(bool); !ok || !v {
		t.Errorf("preflight.plaintext_transport should be true for an http:// mirror, got %v",
			flight["plaintext_transport"])
	}
	mustValidate(t, payload)
}

// TestTransportLabel_DoesNotOverclaim — the dry-run table must not describe
// a cleartext mirror in words an operator could read as "verified".
func TestTransportLabel_DoesNotOverclaim(t *testing.T) {
	plain := transportLabel(true)
	if !strings.Contains(plain, "NOT authenticated") {
		t.Errorf("cleartext label should say it is not authenticated, got %q", plain)
	}
	if secure := transportLabel(false); secure != "https" {
		t.Errorf("https label = %q, want %q", secure, "https")
	}
}

// Helpers ----------------------------------------------------------------

// roundTrip marshals the payload and decodes it back, so assertions run
// against the bytes a consumer actually sees rather than the Go map.
func roundTrip(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return out
}

// mustValidate asserts the payload satisfies the published output schema.
// The schema's top-level oneOf means this also proves the payload matches
// exactly one of the three documented variants.
func mustValidate(t *testing.T, payload map[string]any) {
	t.Helper()
	schemaPath, err := filepath.Abs(schemaRelPath)
	if err != nil {
		t.Fatalf("abs schema path: %v", err)
	}
	f, err := os.Open(schemaPath)
	if err != nil {
		t.Fatalf("open schema: %v", err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaPath, doc); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	sch, err := c.Compile(schemaPath)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	if err := sch.Validate(roundTrip(t, payload)); err != nil {
		t.Fatalf("payload does not validate against %s: %v", schemaRelPath, err)
	}
}
