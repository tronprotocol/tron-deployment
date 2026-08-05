package dbfork

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math/big"

	"golang.org/x/crypto/sha3"
	"google.golang.org/protobuf/proto"

	"github.com/tronprotocol/tron-deployment/internal/dbfork/db"
	pb "github.com/tronprotocol/tron-deployment/internal/tronproto/pb"
)

// TRC20Spec is one entry from fork.conf's `trc20Contracts:` list.
// Mirrors java DbFork's parsed shape (DbFork.java:295-371; key strings
// from utils/Constant.java:20-24):
//
//	{ contractAddress = "T..."; balancesSlotPosition = 0;
//	  address = "T..."; balance = "1000000" }
//
// All four fields are required — java DbFork filters out specs missing
// any of them (:301-304). The Go port matches by erroring at validation
// time rather than silently dropping.
//
// Why a string for Balance: TRC20 balances are uint256 (32-byte big-
// endian), which doesn't fit int64. Java parses with `new BigInteger
// (balance, 10)`; Go uses math/big.Int.SetString(., 10).
type TRC20Spec struct {
	// ContractAddress is the TRC20 contract's TRON address
	// (Base58Check). Must already exist in contractStore — DbFork
	// validates by reading the SmartContract proto for version +
	// trx_hash.
	ContractAddress string `yaml:"contractAddress"`

	// BalancesSlotPosition is the EVM storage slot index of the
	// contract's `mapping(address => uint256) balances`. Default 0
	// covers most ERC20/TRC20 implementations (OpenZeppelin's _balances
	// slot, USDT's balances slot). Custom contracts may need a non-
	// zero value; java applies the override only when > 0 (:331-333).
	BalancesSlotPosition int `yaml:"balancesSlotPosition"`

	// Account is the holder address (Base58Check). The leading 0x41
	// byte is stripped to 20 bytes before the keccak slot derivation,
	// matching Ethereum's address encoding convention. Java's
	// fork.conf names this key `address` — the Go field is renamed
	// to disambiguate from ContractAddress at the call site.
	Account string `yaml:"address"`

	// Balance is the decimal-string token amount. Encoded as 32-byte
	// big-endian uint256 in the on-disk storage row (TRC20 standard).
	//
	// In fork.conf (HOCON or YAML) this field MUST be quoted because
	// real-world TRC20 supplies (e.g. 1.28e26 for a wrapped BTC peg)
	// overflow int64 (max 9.2e18). The loader's wrong-type check
	// surfaces an "expected string" error if the conf author omits
	// the quotes on a large value, but small unquoted numbers would
	// silently parse-and-lose precision.
	//
	// Documented divergence from java DbFork: java's
	// `BigInteger(s, 10)` + `%064x` accepts a negative literal and
	// then crashes deeper in fromHexString (negative hex isn't
	// reversible). The Go port rejects negatives up-front with a
	// typed error — strictly safer, but means a fork.conf that
	// accidentally negates a balance gets a clean error on Go and a
	// stack trace on java. The equivalence test (Task #152) seeds
	// only non-negative values, so this never triggers there.
	Balance string `yaml:"balance"`
}

// MutateTRC20Contracts applies the fork.conf trc20Contracts block:
// for each spec, derives the EVM storage-row key for
// `balances[account]` and writes the 32-byte big-endian balance.
//
// Algorithm (verbatim from java DbFork:312-366):
//
//  1. Look up the contract's SmartContract proto in contractStore.
//     If missing, log + skip (the spec doesn't count toward modified).
//  2. Derive contractKey = keccak256(addr32 || slot32) — the standard
//     Solidity mapping slot derivation.
//  3. If smartContract.version == 1, hash contractKey one more time.
//  4. Derive addressHash:
//     - if smartContract.trx_hash is missing or all-zero (placeholder
//     for system-deployed contracts): keccak256(contractAddress).
//     - otherwise: keccak256(contractAddress || trx_hash).
//  5. rowKey = addressHash[:16] || contractKey[16:32] — java-tron's
//     namespaced storage layout.
//  6. rowValue = balance as 32-byte big-endian uint256.
//  7. storageRowStore.put(rowKey, rowValue) — no per-row proto
//     wrapper; StorageRowCapsule.getData() (chainbase :73) returns
//     rowValue directly.
//
// Both contractEng and storageRowEng are required. contractEng is
// read-only here (DbFork only reads SmartContract metadata; it never
// modifies the bytecode). storageRowEng receives one Put per spec.
//
// Returns the count of TRC20 holdings written (skips for missing
// contracts are NOT counted, matching java :310-365 where cnt
// increments inside the success path only).
func MutateTRC20Contracts(
	contractEng, storageRowEng db.Engine,
	specs []TRC20Spec,
) (modified int, err error) {
	rowBatch := storageRowEng.NewBatch()
	defer rowBatch.Close()

	for _, s := range specs {
		// Validate up front — java filters at :301-304; we error so
		// callers see the partial spec rather than silent drops.
		if s.ContractAddress == "" || s.Account == "" || s.Balance == "" {
			return 0, fmt.Errorf("dbfork: TRC20 spec requires contractAddress, "+
				"address, and balance (got contract=%q account=%q balance=%q)",
				s.ContractAddress, s.Account, s.Balance)
		}

		contractAddr, decodeErr := DecodeAddress(s.ContractAddress)
		if decodeErr != nil {
			return 0, fmt.Errorf("dbfork: TRC20 contractAddress %q: %w",
				s.ContractAddress, decodeErr)
		}

		// Existence-check the contract (java :316-320). Missing
		// contracts surface a warning and skip — equivalent to "we
		// can't fund balances on a contract that doesn't exist."
		contractRaw, getErr := contractEng.Get(contractAddr)
		if getErr != nil {
			if errors.Is(getErr, db.ErrNotFound) {
				log.Printf("dbfork: TRC20 contract %s not in contractStore; skipping",
					s.ContractAddress)
				continue
			}
			return 0, fmt.Errorf("dbfork: get contract %s: %w",
				s.ContractAddress, getErr)
		}

		smartContract := &pb.SmartContract{}
		if unmErr := proto.Unmarshal(contractRaw, smartContract); unmErr != nil {
			// Documented divergence from java DbFork :325-328, which
			// prints the stack trace and `return`s from the lambda
			// (skips just this spec, others continue, cnt unchanged).
			// Go halts the whole apply. The Go behavior is strictly
			// safer — proto corruption in contractStore is a data-
			// integrity signal that should NOT be swallowed silently.
			// The equivalence test (Task #152) seeds well-formed
			// fixtures, so this never triggers there.
			return 0, fmt.Errorf("dbfork: unmarshal SmartContract %s: %w",
				s.ContractAddress, unmErr)
		}

		// Account address: strip the 0x41 mainnet prefix to a bare
		// 20-byte EVM-style address. java :334-336.
		accountAddrWithPrefix, decodeErr := DecodeAddress(s.Account)
		if decodeErr != nil {
			return 0, fmt.Errorf("dbfork: TRC20 account %q: %w", s.Account, decodeErr)
		}
		account20 := accountAddrWithPrefix[1:] // drop 0x41 byte → 20 bytes

		// Slot derivation: keccak256(left-padded-32-byte-address ||
		// 32-byte-slot). java uses String.format("%064x", …) +
		// fromHexString round-trip to get zero-padded hex strings —
		// the byte-level effect is right-aligned 32-byte big-endian
		// values, which we build directly.
		// java :330-333 — `balancesSlotPosition` defaults to 0 and is
		// only overridden when conf value > 0. A negative spec value
		// is treated the same as 0 (java's getInt returns 0 for
		// missing fields and only assigns when > 0).
		slotPos := max(s.BalancesSlotPosition, 0)
		addr32 := make([]byte, 32)
		copy(addr32[12:], account20) // right-align 20 bytes into 32
		slot32 := make([]byte, 32)
		binary.BigEndian.PutUint64(slot32[24:], uint64(slotPos))

		contractKey := keccak256(addr32, slot32)

		// addressHash: keccak256(contractAddress) when trx_hash is
		// absent/zero (placeholder for system-deployed contracts like
		// USDT pre-deployment), else keccak256(contractAddress ||
		// trx_hash). java :343-349.
		var addressHash []byte
		if isNullOrEmpty(smartContract.TrxHash) {
			addressHash = keccak256(contractAddr)
		} else {
			addressHash = keccak256(contractAddr, smartContract.TrxHash)
		}

		// Version 1 contracts get an extra hash round on the
		// contractKey (java :351-354). Newer contracts use this
		// double-hashed slot to namespace storage per contract
		// version. Don't double-hash version 0.
		if smartContract.Version == 1 {
			contractKey = keccak256(contractKey)
		}

		// rowKey = addressHash[0:16] || contractKey[16:32]. java
		// :355-357 — the first half namespaces by contract+creation
		// tx, the second half identifies the specific slot.
		rowKey := make([]byte, 32)
		copy(rowKey[:16], addressHash[:16])
		copy(rowKey[16:], contractKey[16:])

		// Balance: decimal string → uint256 → 32-byte BE. java
		// :359-361 uses BigInteger + String.format("%064x"); Go's
		// big.Int.FillBytes right-aligns into a fixed-size slice
		// (Go 1.15+).
		balanceBig, ok := new(big.Int).SetString(s.Balance, 10)
		if !ok {
			return 0, fmt.Errorf("dbfork: TRC20 balance %q is not a valid decimal integer",
				s.Balance)
		}
		if balanceBig.Sign() < 0 {
			return 0, fmt.Errorf("dbfork: TRC20 balance %q must be non-negative",
				s.Balance)
		}
		if balanceBig.BitLen() > 256 {
			return 0, fmt.Errorf("dbfork: TRC20 balance %q exceeds uint256",
				s.Balance)
		}
		rowValue := make([]byte, 32)
		balanceBig.FillBytes(rowValue)

		rowBatch.Put(rowKey, rowValue)
		modified++
	}

	if writeErr := rowBatch.Write(); writeErr != nil {
		return 0, fmt.Errorf("dbfork: write storage-row batch: %w", writeErr)
	}
	return modified, nil
}

// keccak256 hashes the concatenation of all inputs using the Ethereum-
// flavored Keccak-256 (not NIST SHA3-256). Java DbFork uses
// `Hash.sha3()` which dispatches to a Keccak-256 implementation —
// tron-core inherited Ethereum's pre-standard variant. Returns a
// 32-byte digest.
func keccak256(parts ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// isNullOrEmpty matches java's ByteUtil.isNullOrZeroArray exactly:
// `(array == null) || (array.length == 0)` (java-tron common ByteUtil
// :396-398). DESPITE THE NAME, the Java helper does NOT scan bytes —
// a non-empty slice of all-zero bytes returns `false`. Used to detect
// placeholder trx_hash fields on system-deployed contracts.
//
// Common case: proto3 deserializes an unset `bytes` field to a
// zero-length slice, so this returns true and the addressHash branch
// uses keccak256(contractAddress). If a contract was deliberately
// written with a 32-byte all-zero trx_hash (test fixture, manual
// seed), both Java and Go fall through to the
// keccak256(contractAddress || trxHash) branch.
func isNullOrEmpty(b []byte) bool {
	return len(b) == 0
}
