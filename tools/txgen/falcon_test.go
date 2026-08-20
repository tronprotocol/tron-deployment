//go:build falcon

package main

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/tronaddr"
)

func TestGenerateFalconKeypair(t *testing.T) {
	kp, err := GenerateFalconKeypair()
	if err != nil {
		t.Fatalf("GenerateFalconKeypair: %v", err)
	}

	sk, err := hex.DecodeString(kp.PrivateKeyHex)
	if err != nil {
		t.Fatalf("decode SK: %v", err)
	}
	if len(sk) != falconSKLen {
		t.Errorf("SK len = %d, want %d", len(sk), falconSKLen)
	}

	hPoly, err := hex.DecodeString(kp.PublicKeyHex)
	if err != nil {
		t.Fatalf("decode h polynomial: %v", err)
	}
	if len(hPoly) != falconHLen {
		t.Errorf("h polynomial len = %d, want %d", len(hPoly), falconHLen)
	}

	if len(kp.HexAddress) != tronaddr.AddressHexLen {
		t.Errorf("hex addr len = %d, want %d", len(kp.HexAddress), tronaddr.AddressHexLen)
	}
	if !strings.HasPrefix(kp.HexAddress, "41") {
		t.Errorf("hex addr missing 0x41 prefix: %s", kp.HexAddress)
	}
	if !strings.HasPrefix(kp.Base58Address, "T") {
		t.Errorf("b58 addr should start with T: %s", kp.Base58Address)
	}

	// Two consecutive keypairs must differ.
	kp2, err := GenerateFalconKeypair()
	if err != nil {
		t.Fatalf("GenerateFalconKeypair (2nd): %v", err)
	}
	if kp.PrivateKeyHex == kp2.PrivateKeyHex {
		t.Error("two keypairs are identical — randomness not advancing")
	}
}

func TestFalconSignerSign(t *testing.T) {
	kp, err := GenerateFalconKeypair()
	if err != nil {
		t.Fatalf("GenerateFalconKeypair: %v", err)
	}

	s, err := NewFalconSigner(kp.PrivateKeyHex, kp.PublicKeyHex)
	if err != nil {
		t.Fatalf("NewFalconSigner: %v", err)
	}
	defer s.Close()

	txID := "a1b2c3d4e5f6071829304a5b6c7d8e9f00112233445566778899aabbccddeeff"
	sig, err := s.Sign(txID)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Must be within TRON's acceptance window [617, 667].
	if len(sig) < 617 || len(sig) > 667 {
		t.Errorf("sig len = %d, want [617, 667]", len(sig))
	}
	// Must start with compressed-format header 0x39 that BouncyCastle checks.
	if sig[0] != 0x39 {
		t.Errorf("sig[0] = 0x%02x, want 0x39 (compressed Falcon header)", sig[0])
	}

	// Falcon signing is randomized — successive signatures must differ.
	sig2, err := s.Sign(txID)
	if err != nil {
		t.Fatalf("Sign (2nd): %v", err)
	}
	if string(sig) == string(sig2) {
		t.Error("two signatures are identical — randomness not advancing")
	}

	// Scheme / key accessors.
	if s.SchemeName() != SchemeFNDSA512 {
		t.Errorf("SchemeName = %q, want %q", s.SchemeName(), SchemeFNDSA512)
	}
	if s.PublicKeyHex() != kp.PublicKeyHex {
		t.Error("PublicKeyHex mismatch")
	}
	if s.HexAddress() != kp.HexAddress {
		t.Error("HexAddress mismatch")
	}

	// Address derived from the same h polynomial must match keygen output.
	hexAddr, _ := addressFromPubBytes([]byte(func() []byte {
		b, _ := hex.DecodeString(kp.PublicKeyHex)
		return b
	}()))
	if hexAddr != kp.HexAddress {
		t.Errorf("address re-derived from pubkey = %s, want %s", hexAddr, kp.HexAddress)
	}

	// Bad-input paths.
	if _, err := s.Sign("nothex"); err == nil {
		t.Error("expected error for non-hex txID")
	}
	if _, err := s.Sign("abcd"); err == nil {
		t.Error("expected error for wrong-length txID")
	}
}

func TestFalconSignerBadInput(t *testing.T) {
	// Too-short SK.
	if _, err := NewFalconSigner(strings.Repeat("ab", 100), strings.Repeat("cd", falconHLen)); err == nil {
		t.Error("expected error for short SK")
	}
	// Too-short PK (h polynomial).
	kp, _ := GenerateFalconKeypair()
	if _, err := NewFalconSigner(kp.PrivateKeyHex, strings.Repeat("ab", 10)); err == nil {
		t.Error("expected error for short h polynomial")
	}
}
