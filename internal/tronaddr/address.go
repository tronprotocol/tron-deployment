package tronaddr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/sha3"
)

// TRON address layout: 0x41 prefix + 20-byte body (Keccak-256 of the
// uncompressed public key, last 20 bytes). Wire form is hex; user-
// facing form is Base58Check (the leading "T..." string).
//
// TRC token transfer payloads expect the hex form (21 bytes). The
// broadcast and createtransaction HTTP endpoints accept hex too.
const tronAddrPrefix byte = 0x41

// PrivateKeyHexLen is the expected length of a hex-encoded secp256k1 key.
const PrivateKeyHexLen = 64

// AddressHexLen is the length of the hex form of a TRON address (21 bytes).
const AddressHexLen = 42

// AddressFromPrivateKey derives the TRON address (hex, 21 bytes) from a
// secp256k1 private key (hex, 32 bytes). Returns both the hex address
// and the Base58Check ("T...") string.
func AddressFromPrivateKey(privHex string) (hexAddr, base58Addr string, err error) {
	if len(privHex) != PrivateKeyHexLen {
		return "", "", errors.New("private key must be 64 hex chars")
	}
	raw, err := hex.DecodeString(privHex)
	if err != nil {
		return "", "", err
	}
	priv := secp256k1.PrivKeyFromBytes(raw)
	pub := priv.PubKey()
	uncompressed := pub.SerializeUncompressed() // 65 bytes, 0x04 + X(32) + Y(32)
	addrHex, addrB58 := AddressFromPubBytes(uncompressed[1:])
	return addrHex, addrB58, nil
}

// AddressFromPubBytes turns 64 raw pubkey bytes (X || Y) into TRON's
// 21-byte address (hex) + Base58Check form.
func AddressFromPubBytes(pubXY []byte) (string, string) {
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(pubXY)
	hash := hasher.Sum(nil) // 32 bytes
	addr := make([]byte, 21)
	addr[0] = tronAddrPrefix
	copy(addr[1:], hash[len(hash)-20:])
	return hex.EncodeToString(addr), base58CheckEncode(addr)
}

// NewRandomAddress generates a fresh secp256k1 keypair and returns
// (privHex, hexAddr, base58Addr). Used by generate to build receivers.
func NewRandomAddress() (privHex, hexAddr, base58Addr string, err error) {
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return "", "", "", err
	}
	privHex = hex.EncodeToString(priv.Serialize())
	uncompressed := priv.PubKey().SerializeUncompressed()
	hexAddr, base58Addr = AddressFromPubBytes(uncompressed[1:])
	return privHex, hexAddr, base58Addr, nil
}

// Base58CheckDecode converts a "T..." TRON address to its 21-byte hex
// form. Returns an error if the checksum is wrong or the prefix is
// not 0x41.
func Base58CheckDecode(s string) ([]byte, error) {
	full, err := base58Decode(s)
	if err != nil {
		return nil, err
	}
	if len(full) < 5 {
		return nil, errors.New("base58: payload too short")
	}
	payload := full[:len(full)-4]
	checksum := full[len(full)-4:]
	h := sha256.Sum256(payload)
	h2 := sha256.Sum256(h[:])
	if !bytes.Equal(h2[:4], checksum) {
		return nil, errors.New("base58: checksum mismatch")
	}
	if payload[0] != tronAddrPrefix {
		return nil, errors.New("base58: not a TRON mainnet address (prefix != 0x41)")
	}
	return payload, nil
}

// NormalizeAddress accepts either a hex (21-byte) or Base58Check ("T...")
// address and returns the 21-byte hex form (lowercase, no 0x).
func NormalizeAddress(s string) (string, error) {
	if len(s) == AddressHexLen {
		raw, err := hex.DecodeString(s)
		if err == nil && raw[0] == tronAddrPrefix {
			return hex.EncodeToString(raw), nil
		}
	}
	raw, err := Base58CheckDecode(s)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// --- Base58 (Bitcoin alphabet) ------------------------------------------

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func base58CheckEncode(payload []byte) string {
	h := sha256.Sum256(payload)
	h2 := sha256.Sum256(h[:])
	full := append(append([]byte{}, payload...), h2[:4]...)
	return base58Encode(full)
}

func base58Encode(input []byte) string {
	zeros := 0
	for zeros < len(input) && input[zeros] == 0 {
		zeros++
	}
	x := new(big.Int).SetBytes(input)
	base := big.NewInt(58)
	mod := new(big.Int)
	out := make([]byte, 0, len(input)*2)
	for x.Sign() > 0 {
		x.DivMod(x, base, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for i := 0; i < zeros; i++ {
		out = append(out, base58Alphabet[0])
	}
	// reverse
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func base58Decode(s string) ([]byte, error) {
	x := big.NewInt(0)
	base := big.NewInt(58)
	for _, r := range s {
		idx := bytes.IndexByte([]byte(base58Alphabet), byte(r))
		if idx < 0 {
			return nil, errors.New("base58: invalid character")
		}
		x.Mul(x, base)
		x.Add(x, big.NewInt(int64(idx)))
	}
	decoded := x.Bytes()
	// re-add leading zero bytes (each leading '1' in the input).
	zeros := 0
	for zeros < len(s) && s[zeros] == base58Alphabet[0] {
		zeros++
	}
	return append(make([]byte, zeros), decoded...), nil
}
