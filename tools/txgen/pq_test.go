package main

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudflare/circl/sign/mldsa/mldsa44"

	"github.com/tronprotocol/tron-deployment/internal/tronaddr"
)

const testSeedHex = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

func TestNewPQSignerRejectsBadInput(t *testing.T) {
	if _, err := NewPQSigner("FN_DSA_512", testSeedHex); err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
	if _, err := NewPQSigner(SchemeMLDSA44, "abcd"); err == nil {
		t.Fatal("expected error for short seed")
	}
	if _, err := NewPQSigner(SchemeMLDSA44, strings.Repeat("zz", 32)); err == nil {
		t.Fatal("expected error for non-hex seed")
	}
}

func TestPQSignerDeterministicAddress(t *testing.T) {
	a, err := NewPQSigner(SchemeMLDSA44, testSeedHex)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewPQSigner(SchemeMLDSA44, testSeedHex)
	if err != nil {
		t.Fatal(err)
	}
	// Same seed must yield the same keypair/address so the on-chain account
	// derived from it stays stable across runs (and matches java-tron).
	if a.HexAddress() != b.HexAddress() {
		t.Fatalf("address not deterministic: %s != %s", a.HexAddress(), b.HexAddress())
	}
	if a.PublicKeyHex() != b.PublicKeyHex() {
		t.Fatal("public key not deterministic")
	}
	// Address must be the 21-byte TRON hex form with the 0x41 prefix.
	if len(a.HexAddress()) != tronaddr.AddressHexLen {
		t.Fatalf("address hex len = %d, want %d", len(a.HexAddress()), tronaddr.AddressHexLen)
	}
	if !strings.HasPrefix(a.HexAddress(), "41") {
		t.Fatalf("address missing 0x41 prefix: %s", a.HexAddress())
	}
	if !strings.HasPrefix(a.Base58Address(), "T") {
		t.Fatalf("base58 address should start with T: %s", a.Base58Address())
	}
	// Public key is the FIPS 204 ML-DSA-44 encoding (1312 bytes).
	if pk, _ := hex.DecodeString(a.PublicKeyHex()); len(pk) != mldsa44.PublicKeySize {
		t.Fatalf("public key len = %d, want %d", len(pk), mldsa44.PublicKeySize)
	}
}

func TestPQSignerSignVerifies(t *testing.T) {
	s, err := NewPQSigner(SchemeMLDSA44, testSeedHex)
	if err != nil {
		t.Fatal(err)
	}
	txID := "a1b2c3d4e5f6071829304a5b6c7d8e9f00112233445566778899aabbccddeeff"
	sig, err := s.Sign(txID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != mldsa44.SignatureSize {
		t.Fatalf("signature len = %d, want %d", len(sig), mldsa44.SignatureSize)
	}

	// Re-derive the public key from the seed and verify exactly as the node
	// would: message = raw txID bytes, empty context. This is the cross-check
	// that the signature is a valid FIPS 204 ML-DSA-44 signature.
	raw, _ := hex.DecodeString(testSeedHex)
	var seed [mldsa44.SeedSize]byte
	copy(seed[:], raw)
	pub, _ := mldsa44.NewKeyFromSeed(&seed)
	msg, _ := hex.DecodeString(txID)
	if !mldsa44.Verify(pub, msg, nil, sig) {
		t.Fatal("signature failed verification")
	}

	if _, err := s.Sign("nothex"); err == nil {
		t.Fatal("expected error signing non-hex txID")
	}
	if _, err := s.Sign("abcd"); err == nil {
		t.Fatal("expected error signing wrong-length txID")
	}
}

func TestAttachPQSignature(t *testing.T) {
	unsigned := []byte(`{"txID":"aa","raw_data":{"x":1},"raw_data_hex":"bb"}`)
	out, err := AttachPQSignature(unsigned, SchemeMLDSA44, "deadbeef", "cafe")
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	// Original fields are preserved and no ECDSA signature is added.
	if _, ok := obj["raw_data"]; !ok {
		t.Fatal("raw_data dropped")
	}
	if _, ok := obj["signature"]; ok {
		t.Fatal("ECDSA signature should not be present on a PQ-only tx")
	}
	var entries []map[string]string
	if err := json.Unmarshal(obj["pq_auth_sig"], &entries); err != nil {
		t.Fatalf("pq_auth_sig not an array of objects: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 pq_auth_sig entry, got %d", len(entries))
	}
	e := entries[0]
	if e["scheme"] != SchemeMLDSA44 || e["public_key"] != "deadbeef" || e["signature"] != "cafe" {
		t.Fatalf("unexpected pq_auth_sig entry: %v", e)
	}
}
