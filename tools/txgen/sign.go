package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	ecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"

	"github.com/tronprotocol/tron-deployment/internal/tronaddr"
)

// secp256k1Order is the group order N of the secp256k1 curve, used for
// low-S signature normalization. Hardcoded to avoid the deprecated
// secp256k1.S256() / elliptic.Curve path.
var secp256k1Order, _ = new(big.Int).SetString(
	"fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141", 16)

// SignTxID signs the 32-byte txID (== sha256(raw_data)) with privHex.
// Returns the 65-byte ECDSA signature (r || s || v) in hex form, which
// is exactly the shape java-tron expects in tx.signature[0].
//
// Note: TRON signatures must be canonical (low-S). The decred library
// returns RFC 6979 deterministic ECDSA but does NOT enforce low-S, so
// we normalize manually.
func SignTxID(privHex, txIDHex string) (string, error) {
	if len(privHex) != tronaddr.PrivateKeyHexLen {
		return "", errors.New("private key must be 64 hex chars")
	}
	privRaw, err := hex.DecodeString(privHex)
	if err != nil {
		return "", err
	}
	priv := secp256k1.PrivKeyFromBytes(privRaw)

	msg, err := hex.DecodeString(txIDHex)
	if err != nil {
		return "", err
	}
	if len(msg) != 32 {
		return "", errors.New("txID must be 32 bytes")
	}

	sig := ecdsa.SignCompact(priv, msg, false)
	// SignCompact returns 65 bytes: [v, r(32), s(32)]. TRON expects
	// [r, s, v] where v is 0 or 1 (recovery id). Rotate.
	if len(sig) != 65 {
		return "", errors.New("unexpected signature length")
	}
	v := sig[0]
	r := sig[1:33]
	s := sig[33:65]

	// SignCompact encodes v as 27+rid (or 31+rid for compressed). Strip
	// to 0/1 because java-tron's verifier expects the raw recovery id.
	if v >= 31 {
		v -= 31
	} else if v >= 27 {
		v -= 27
	}

	// Low-S normalization (BIP-62 / EIP-2 style). secp256k1Order is the
	// group order N; we use the constant directly because secp256k1.S256()
	// is deprecated (elliptic.Curve interface).
	curveN := secp256k1Order
	halfN := new(big.Int).Rsh(curveN, 1)
	sBig := new(big.Int).SetBytes(s)
	if sBig.Cmp(halfN) == 1 {
		sBig.Sub(curveN, sBig)
		// v flips when S is negated.
		v ^= 1
	}
	sBytes := sBig.FillBytes(make([]byte, 32))

	out := make([]byte, 65)
	copy(out[0:32], r)
	copy(out[32:64], sBytes)
	out[64] = v
	return hex.EncodeToString(out), nil
}

// Sha256Hex returns sha256(hexBytes) as a hex string. Used to recompute
// txID from raw_data_hex as a sanity check.
func Sha256Hex(hexInput string) (string, error) {
	raw, err := hex.DecodeString(hexInput)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), nil
}
