package main

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/cloudflare/circl/sign/mldsa/mldsa44"

	"github.com/tronprotocol/tron-deployment/internal/tronaddr"
)

// Post-quantum (抗量子) signing for txgen.
//
// java-tron's PQ support (PR: feat(crypto): add post-quantum signature
// support) lets a transaction carry, instead of (or alongside) the ECDSA
// `signature`, a `pq_auth_sig` envelope:
//
//	pq_auth_sig: [{ scheme, public_key, signature }]
//
// The signed digest is identical to the ECDSA path — txID == sha256(raw_data)
// — so generation reuses txgen's normal "build unsigned tx → sign txID →
// attach → broadcast" flow; only the signer and the attached field change.
//
// Scheme support: only ML_DSA_44 (FIPS 204 ML-DSA-44 / CRYSTALS-Dilithium-2)
// is implemented. It interoperates cleanly with java-tron's BouncyCastle
// verifier: cloudflare/circl follows FIPS 204, so the 1312-byte public key
// (rho‖t1), 2420-byte signature, and 32-byte seed → KeyGen(seed) derivation
// all match BC's encoding. FN_DSA_512 (Falcon-512) is intentionally NOT
// implemented: java-tron uses a BouncyCastle/EIP-8052 specific wire encoding
// that no Go library reproduces, so a Go-produced Falcon signature would be
// rejected on-chain.
//
// Account provisioning is out of scope: the sender account's permission must
// already contain this PQ public key with enough weight (e.g. installed via
// AccountPermissionUpdate) for the node to accept the signature. See README.
const (
	// SchemeMLDSA44 is the PQScheme enum name as serialized in java-tron's
	// HTTP JSON (the node parses the enum by name).
	SchemeMLDSA44 = "ML_DSA_44"

	// pqSeedHexLen is the hex length of a 32-byte ML-DSA seed.
	pqSeedHexLen = 64
)

// PQSigner signs transaction IDs with a post-quantum scheme and exposes the
// public key + derived address needed to attach a pq_auth_sig and to match
// the sender account's permission.
type PQSigner struct {
	scheme  string
	priv    *mldsa44.PrivateKey
	pubKey  []byte // FIPS 204 encoding (rho ‖ t1), 1312 bytes
	hexAddr string // 0x41 ‖ Keccak-256(pubKey)[12..32]
	b58Addr string
}

// NewPQSigner builds a signer for the given scheme from a 32-byte hex seed.
// The keypair is derived deterministically via FIPS 204 KeyGen(seed), which
// matches java-tron's BouncyCastle FixedSecureRandom(seed) path, so the same
// seed yields the same on-chain address on both sides.
func NewPQSigner(scheme, seedHex string) (*PQSigner, error) {
	if scheme != SchemeMLDSA44 {
		return nil, fmt.Errorf("unsupported pq scheme %q (only %s is implemented)", scheme, SchemeMLDSA44)
	}
	if len(seedHex) != pqSeedHexLen {
		return nil, fmt.Errorf("pq seed must be %d hex chars (32 bytes)", pqSeedHexLen)
	}
	raw, err := hex.DecodeString(seedHex)
	if err != nil {
		return nil, fmt.Errorf("decode pq seed: %w", err)
	}
	var seed [mldsa44.SeedSize]byte
	copy(seed[:], raw)
	pub, priv := mldsa44.NewKeyFromSeed(&seed)

	pubBytes := pub.Bytes()
	// PQ address derivation matches the ECDSA flow: 0x41 ‖ Keccak-256(pk)[12..32].
	// tronaddr.AddressFromPubBytes hashes its input and keeps the last 20 bytes, so the
	// full PQ public key is passed in place of the 64-byte ECDSA X‖Y.
	hexAddr, b58 := tronaddr.AddressFromPubBytes(pubBytes)
	return &PQSigner{
		scheme:  scheme,
		priv:    priv,
		pubKey:  pubBytes,
		hexAddr: hexAddr,
		b58Addr: b58,
	}, nil
}

// Sign produces the PQ signature over the 32-byte txID (hex). The message is
// the raw txID bytes — the same digest ECDSA signs — with an empty context
// and hedged randomness, matching java-tron's default MLDSASigner.
func (s *PQSigner) Sign(txIDHex string) ([]byte, error) {
	msg, err := hex.DecodeString(txIDHex)
	if err != nil {
		return nil, err
	}
	if len(msg) != 32 {
		return nil, errors.New("txID must be 32 bytes")
	}
	sig := make([]byte, mldsa44.SignatureSize)
	if err := mldsa44.SignTo(s.priv, msg, nil, true, sig); err != nil {
		return nil, err
	}
	return sig, nil
}

// SchemeName returns the PQScheme enum name (e.g. "ML_DSA_44").
func (s *PQSigner) SchemeName() string { return s.scheme }

// PublicKeyHex returns the FIPS 204 public-key encoding as lowercase hex.
func (s *PQSigner) PublicKeyHex() string { return hex.EncodeToString(s.pubKey) }

// HexAddress returns the PQ-derived TRON address (21-byte hex form).
func (s *PQSigner) HexAddress() string { return s.hexAddr }

// Base58Address returns the PQ-derived TRON address ("T..." form).
func (s *PQSigner) Base58Address() string { return s.b58Addr }

// Close is a no-op for the pure-Go ML-DSA signer. It exists so PQSigner
// satisfies the same signer interface as FalconSigner, letting generate.go
// clean up both paths uniformly.
func (s *PQSigner) Close() {}
