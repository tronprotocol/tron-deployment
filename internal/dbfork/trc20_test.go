package dbfork

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/tronprotocol/tron-deployment/internal/dbfork/db"
	"github.com/tronprotocol/tron-deployment/internal/dbfork/stores"
	pb "github.com/tronprotocol/tron-deployment/internal/tronproto/pb"
)

// TestKeccak256_EmptyVector pins that our keccak256 implementation is
// Ethereum-flavored Keccak-256, NOT NIST SHA3-256. The two algorithms
// share a structure but differ in padding bytes — they produce
// different outputs for every input. Mixing them up would silently
// break byte-equivalence with java-tron, since java's `Hash.sha3()`
// calls into a Keccak-256 implementation.
//
// The known vector: Keccak-256 of empty input =
// c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470
// NIST SHA3-256 of empty input would be
// a7ffc6f8bf1ed76651c14756a061d662f580ff4de43b49fa82d80a4b80f8434a
// (a different value), so a regression to sha3.New256() would surface
// here immediately.
func TestKeccak256_EmptyVector(t *testing.T) {
	got := keccak256([]byte{})
	want, _ := hex.DecodeString("c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470")
	if !bytes.Equal(got, want) {
		t.Errorf("keccak256(\"\") = %x; want %x (Keccak-256, not NIST SHA3-256)", got, want)
	}
}

// TestKeccak256_NonEmptyVector pins the algorithm on a non-trivial
// input. "abc" is a standard SHA-3 test vector; the Keccak-256 result
// is well-known and easily verified via web tools / cast keccak / any
// independent implementation. A Keccak vs NIST-SHA3 mix-up would also
// fail here (NIST SHA3-256 of "abc" = 3a985da74fe225b2…), giving
// belt-and-suspenders coverage on top of the empty vector.
func TestKeccak256_NonEmptyVector(t *testing.T) {
	got := keccak256([]byte("abc"))
	want, _ := hex.DecodeString("4e03657aea45a94fc7d47ba826c8d667c0d1e6e33a64a036ec44f58fa12d6c45")
	if !bytes.Equal(got, want) {
		t.Errorf("keccak256(\"abc\") = %x; want %x (Keccak-256)", got, want)
	}
}

// TestKeccak256_MultiPartConcat pins the variadic-concatenation
// behavior of our keccak256 helper. The composition test (rowKey)
// relies on this: hashing (parts...) must be equivalent to hashing
// the byte concatenation of parts. A bug in the helper that, say,
// inserted a delimiter or hashed parts independently would silently
// produce wrong rowKeys.
func TestKeccak256_MultiPartConcat(t *testing.T) {
	twoPart := keccak256([]byte("ab"), []byte("c"))
	onePart := keccak256([]byte("abc"))
	if !bytes.Equal(twoPart, onePart) {
		t.Errorf("keccak256(\"ab\", \"c\") = %x; want = keccak256(\"abc\") = %x",
			twoPart, onePart)
	}
}

// TestIsNullOrEmpty pins the placeholder-trx-hash detection EXACTLY
// matches java ByteUtil.isNullOrZeroArray (java-tron common :396-398):
// `(array == null) || (array.length == 0)`. Despite the Java helper's
// name, it does NOT scan bytes — an all-zero 32-byte slice returns
// false on both sides. A naive Go port that scans bytes (the obvious
// reading of the name) would diverge from Java on any contract
// written with a deliberately zero trx_hash, taking the wrong
// addressHash branch and writing storage rows under the wrong key.
func TestIsNullOrEmpty(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"nil", nil, true},
		{"empty", []byte{}, true},
		// CRITICAL: a length-32 all-zero slice must return FALSE,
		// matching java's `array.length == 0` check (NOT a byte scan).
		{"all zeros (length 32)", make([]byte, 32), false},
		{"trailing nonzero", []byte{0, 0, 0, 1}, false},
		{"leading nonzero", []byte{1, 0, 0, 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNullOrEmpty(tc.in); got != tc.want {
				t.Errorf("isNullOrEmpty(%v) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

// openTRC20Triplet seeds and opens the 2 stores MutateTRC20Contracts
// touches: contractStore (read-only) and storage-row (write).
func openTRC20Triplet(t *testing.T) (contractEng, storageRowEng db.Engine) {
	t.Helper()
	dataDir := seedLevelDBStore(t, stores.ContractStore)
	seedLevelDBStoreUnder(t, dataDir, stores.StorageRowStore)
	var err error
	contractEng, err = db.OpenLevelDB(dataDir, stores.ContractStore)
	if err != nil {
		t.Fatalf("open contract: %v", err)
	}
	t.Cleanup(func() { _ = contractEng.Close() })
	storageRowEng, err = db.OpenLevelDB(dataDir, stores.StorageRowStore)
	if err != nil {
		t.Fatalf("open storage-row: %v", err)
	}
	t.Cleanup(func() { _ = storageRowEng.Close() })
	return contractEng, storageRowEng
}

// seedContract marshals a SmartContract proto and puts it under the
// contract address key, so MutateTRC20Contracts' existence-probe
// passes and reads back the expected version + trx_hash.
func seedContract(t *testing.T, eng db.Engine, addr []byte, sc *pb.SmartContract) {
	t.Helper()
	raw, err := proto.Marshal(sc)
	if err != nil {
		t.Fatalf("marshal SmartContract: %v", err)
	}
	b := eng.NewBatch()
	b.Put(addr, raw)
	if err := b.Write(); err != nil {
		t.Fatalf("seed contract: %v", err)
	}
	b.Close()
}

// expectedRowKey reproduces the storage-row-key derivation
// independently of MutateTRC20Contracts, so tests have a known-good
// expected value computed from the same primitives without copy-
// pasting the production code path. If the production code's
// composition drifts (wrong concat order, wrong slice bounds, wrong
// version branch), the on-disk key will differ from this helper.
func expectedRowKey(
	t *testing.T,
	contractAddr []byte, // 21 bytes, 0x41 prefix
	accountAddr []byte, // 21 bytes
	slot int,
	trxHash []byte,
	version int32,
) []byte {
	t.Helper()
	account20 := accountAddr[1:]
	addr32 := make([]byte, 32)
	copy(addr32[12:], account20)
	slot32 := make([]byte, 32)
	binary.BigEndian.PutUint64(slot32[24:], uint64(slot))
	contractKey := keccak256(addr32, slot32)
	if version == 1 {
		contractKey = keccak256(contractKey)
	}
	var addressHash []byte
	if isNullOrEmpty(trxHash) {
		addressHash = keccak256(contractAddr)
	} else {
		addressHash = keccak256(contractAddr, trxHash)
	}
	rowKey := make([]byte, 32)
	copy(rowKey[:16], addressHash[:16])
	copy(rowKey[16:], contractKey[16:])
	return rowKey
}

// expectedRowValue: balance as 32-byte BE uint256 — same primitive
// as production. Pinned here so future refactors can't silently swap
// to little-endian.
func expectedRowValue(t *testing.T, decimalBalance string) []byte {
	t.Helper()
	v, ok := new(big.Int).SetString(decimalBalance, 10)
	if !ok {
		t.Fatalf("bad balance literal %q", decimalBalance)
	}
	out := make([]byte, 32)
	v.FillBytes(out)
	return out
}

// TestMutateTRC20Contracts_Version0_NoTrxHash is the canonical happy
// path: USDT-like contract with version=0 and empty trx_hash (the
// system-deployed-contract placeholder case). Pins the slot
// derivation, the addressHash-from-contractAddress-only branch, and
// the rowKey split.
func TestMutateTRC20Contracts_Version0_NoTrxHash(t *testing.T) {
	contractEng, storageRowEng := openTRC20Triplet(t)
	contractAddrStr, contractRaw := makeAddress(t, [20]byte{0xc0, 0x01})
	accountAddrStr, accountRaw := makeAddress(t, [20]byte{0xac, 0x70})

	seedContract(t, contractEng, contractRaw, &pb.SmartContract{
		// Version 0 (default), TrxHash empty (default).
		Name: "USDT-test",
	})

	modified, err := MutateTRC20Contracts(contractEng, storageRowEng,
		[]TRC20Spec{{
			ContractAddress:      contractAddrStr,
			BalancesSlotPosition: 0,
			Account:              accountAddrStr,
			Balance:              "1000000", // 1 USDT (6 decimals)
		}})
	if err != nil {
		t.Fatalf("MutateTRC20Contracts: %v", err)
	}
	if modified != 1 {
		t.Errorf("modified = %d; want 1", modified)
	}

	wantKey := expectedRowKey(t, contractRaw, accountRaw, 0, nil, 0)
	wantVal := expectedRowValue(t, "1000000")
	gotVal, err := storageRowEng.Get(wantKey)
	if err != nil {
		t.Fatalf("Get rowKey: %v", err)
	}
	if !bytes.Equal(gotVal, wantVal) {
		t.Errorf("rowValue = %x; want %x", gotVal, wantVal)
	}
}

// TestMutateTRC20Contracts_Version1_DoubleHashesContractKey pins the
// java :351-354 branch: version-1 contracts get an extra keccak round
// on contractKey BEFORE the rowKey split. A regression that skips the
// branch would write to the version-0 slot, which is a different
// rowKey — the test would fail because the spec'd value lands at the
// wrong key.
func TestMutateTRC20Contracts_Version1_DoubleHashesContractKey(t *testing.T) {
	contractEng, storageRowEng := openTRC20Triplet(t)
	contractAddrStr, contractRaw := makeAddress(t, [20]byte{0xc1})
	accountAddrStr, accountRaw := makeAddress(t, [20]byte{0xab})

	seedContract(t, contractEng, contractRaw, &pb.SmartContract{
		Version: 1, // triggers the double-hash branch
	})

	_, err := MutateTRC20Contracts(contractEng, storageRowEng,
		[]TRC20Spec{{
			ContractAddress: contractAddrStr,
			Account:         accountAddrStr,
			Balance:         "42",
		}})
	if err != nil {
		t.Fatal(err)
	}

	wantKey := expectedRowKey(t, contractRaw, accountRaw, 0, nil, 1)
	if _, err := storageRowEng.Get(wantKey); err != nil {
		t.Errorf("expected rowKey for version=1 not present: %v", err)
	}
	// And the version=0 rowKey for the same inputs must NOT exist —
	// double-hashing actually changes the key.
	wrongKey := expectedRowKey(t, contractRaw, accountRaw, 0, nil, 0)
	if _, err := storageRowEng.Get(wrongKey); err == nil {
		t.Error("version=0 rowKey unexpectedly present — version branching is broken")
	}
}

// TestMutateTRC20Contracts_NonEmptyTrxHash_ChangesAddressHash: the
// addressHash branch should use keccak256(contractAddr || trxHash)
// when trxHash is non-zero. Verify by comparing rowKeys for the same
// inputs with empty vs non-empty trxHash — they must differ.
func TestMutateTRC20Contracts_NonEmptyTrxHash_ChangesAddressHash(t *testing.T) {
	contractEng, storageRowEng := openTRC20Triplet(t)
	contractAddrStr, contractRaw := makeAddress(t, [20]byte{0xc2})
	accountAddrStr, accountRaw := makeAddress(t, [20]byte{0xad})

	// Plant a non-zero trx_hash so the merge branch fires.
	trxHash := bytes.Repeat([]byte{0xee}, 32)
	seedContract(t, contractEng, contractRaw, &pb.SmartContract{
		TrxHash: trxHash,
	})

	_, err := MutateTRC20Contracts(contractEng, storageRowEng,
		[]TRC20Spec{{
			ContractAddress: contractAddrStr,
			Account:         accountAddrStr,
			Balance:         "100",
		}})
	if err != nil {
		t.Fatal(err)
	}

	wantKey := expectedRowKey(t, contractRaw, accountRaw, 0, trxHash, 0)
	if _, err := storageRowEng.Get(wantKey); err != nil {
		t.Errorf("expected rowKey (with trxHash) not present: %v", err)
	}

	// The empty-trxHash variant of the rowKey must NOT exist — the
	// trxHash truly affects the key.
	plainKey := expectedRowKey(t, contractRaw, accountRaw, 0, nil, 0)
	if !bytes.Equal(wantKey, plainKey) {
		if _, err := storageRowEng.Get(plainKey); err == nil {
			t.Error("plain-addressHash rowKey unexpectedly present — trxHash branch is broken")
		}
	}
}

// TestMutateTRC20Contracts_NonZeroSlot pins the BalancesSlotPosition
// override branch (java :331-333). A bug that ignored the spec value
// would write the same key as slot=0.
func TestMutateTRC20Contracts_NonZeroSlot(t *testing.T) {
	contractEng, storageRowEng := openTRC20Triplet(t)
	contractAddrStr, contractRaw := makeAddress(t, [20]byte{0xc3})
	accountAddrStr, accountRaw := makeAddress(t, [20]byte{0xae})

	seedContract(t, contractEng, contractRaw, &pb.SmartContract{})

	_, err := MutateTRC20Contracts(contractEng, storageRowEng,
		[]TRC20Spec{{
			ContractAddress:      contractAddrStr,
			BalancesSlotPosition: 5,
			Account:              accountAddrStr,
			Balance:              "1",
		}})
	if err != nil {
		t.Fatal(err)
	}

	wantKey := expectedRowKey(t, contractRaw, accountRaw, 5, nil, 0)
	if _, err := storageRowEng.Get(wantKey); err != nil {
		t.Errorf("slot=5 rowKey not present: %v", err)
	}
	// slot=0 rowKey should NOT be there.
	zeroKey := expectedRowKey(t, contractRaw, accountRaw, 0, nil, 0)
	if _, err := storageRowEng.Get(zeroKey); err == nil {
		t.Error("slot=0 rowKey unexpectedly present — slot override is broken")
	}
}

// TestMutateTRC20Contracts_UInt256Balance pins that balances larger
// than int64 round-trip correctly through the BigInt → 32-byte BE
// pipeline. A naive int64 implementation would overflow on real-world
// supplies (e.g., USDT total supply is ~6e10 * 1e6 = 6e16, fits; but
// BTC-pegged tokens at 1e18 precision easily exceed int64).
func TestMutateTRC20Contracts_UInt256Balance(t *testing.T) {
	contractEng, storageRowEng := openTRC20Triplet(t)
	contractAddrStr, contractRaw := makeAddress(t, [20]byte{0xc4})
	accountAddrStr, accountRaw := makeAddress(t, [20]byte{0xaf})

	seedContract(t, contractEng, contractRaw, &pb.SmartContract{})

	// 2^200 — way past int64 (2^63) but well under uint256 (2^256).
	hugeBalance := new(big.Int).Lsh(big.NewInt(1), 200).String()

	_, err := MutateTRC20Contracts(contractEng, storageRowEng,
		[]TRC20Spec{{
			ContractAddress: contractAddrStr,
			Account:         accountAddrStr,
			Balance:         hugeBalance,
		}})
	if err != nil {
		t.Fatal(err)
	}

	wantKey := expectedRowKey(t, contractRaw, accountRaw, 0, nil, 0)
	wantVal := expectedRowValue(t, hugeBalance)
	gotVal, err := storageRowEng.Get(wantKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotVal, wantVal) {
		t.Errorf("rowValue mismatch for huge balance:\n got %x\nwant %x", gotVal, wantVal)
	}
}

// TestMutateTRC20Contracts_MissingContract_Skipped pins java's
// :316-320 behavior: when contractStore lacks the address, log + skip
// + don't increment the modified counter.
func TestMutateTRC20Contracts_MissingContract_Skipped(t *testing.T) {
	contractEng, storageRowEng := openTRC20Triplet(t)
	// Deliberately do NOT seed any contract.
	contractAddrStr, _ := makeAddress(t, [20]byte{0xc5})
	accountAddrStr, _ := makeAddress(t, [20]byte{0xaf})

	modified, err := MutateTRC20Contracts(contractEng, storageRowEng,
		[]TRC20Spec{{
			ContractAddress: contractAddrStr,
			Account:         accountAddrStr,
			Balance:         "1",
		}})
	if err != nil {
		t.Fatalf("MutateTRC20Contracts: %v (should skip, not error)", err)
	}
	if modified != 0 {
		t.Errorf("modified = %d; want 0 (missing contract should be skipped)", modified)
	}
}

// TestMutateTRC20Contracts_PartialSpecRejected: java filters out
// specs missing any of the 4 required fields (:301-304). Go errors
// explicitly to surface the malformed entry.
func TestMutateTRC20Contracts_PartialSpecRejected(t *testing.T) {
	contractEng, storageRowEng := openTRC20Triplet(t)

	cases := []TRC20Spec{
		{Account: "T...", Balance: "1"},            // no contract
		{ContractAddress: "T...", Balance: "1"},    // no account
		{ContractAddress: "T...", Account: "T..."}, // no balance
	}
	for i, sp := range cases {
		_, err := MutateTRC20Contracts(contractEng, storageRowEng, []TRC20Spec{sp})
		if err == nil {
			t.Errorf("case %d (%+v): expected error for partial spec", i, sp)
		}
	}
}

// TestMutateTRC20Contracts_InvalidBalance: non-numeric / negative /
// overflow balances are rejected. The 3 paths share a code branch,
// so we cover each.
func TestMutateTRC20Contracts_InvalidBalance(t *testing.T) {
	contractEng, storageRowEng := openTRC20Triplet(t)
	contractAddrStr, contractRaw := makeAddress(t, [20]byte{0xc6})
	accountAddrStr, _ := makeAddress(t, [20]byte{0xb0})
	seedContract(t, contractEng, contractRaw, &pb.SmartContract{})

	cases := []struct {
		name    string
		balance string
		errSub  string
	}{
		{"not a number", "abc", "valid decimal"},
		{"negative", "-1", "non-negative"},
		// 2^257 — exceeds uint256.
		{"overflow", new(big.Int).Lsh(big.NewInt(1), 257).String(), "exceeds uint256"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := MutateTRC20Contracts(contractEng, storageRowEng,
				[]TRC20Spec{{
					ContractAddress: contractAddrStr,
					Account:         accountAddrStr,
					Balance:         tc.balance,
				}})
			if err == nil {
				t.Fatalf("expected error for balance %q", tc.balance)
			}
			if !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("err = %v; want substring %q", err, tc.errSub)
			}
		})
	}
}

// TestMutateTRC20Contracts_PartialFailureRollsBackBatch pins
// atomic-rollback for the genuine mid-loop failure case: spec[0]
// queues a real storage-row Put, spec[1] errors at balance parsing.
// Without the per-call `if err return 0, … ` before `batch.Write`,
// spec[0]'s row would silently land — exactly the regression
// `TestMutateAccounts_PartialFailureRollsBackBothStores` guards
// against in the accounts package.
func TestMutateTRC20Contracts_PartialFailureRollsBackBatch(t *testing.T) {
	contractEng, storageRowEng := openTRC20Triplet(t)
	contractAddrStr, contractRaw := makeAddress(t, [20]byte{0xc7})
	accountAddrStr, accountRaw := makeAddress(t, [20]byte{0xb1})

	seedContract(t, contractEng, contractRaw, &pb.SmartContract{})

	_, err := MutateTRC20Contracts(contractEng, storageRowEng,
		[]TRC20Spec{
			// spec[0]: VALID — would queue a Put to the storage-row batch.
			{
				ContractAddress: contractAddrStr,
				Account:         accountAddrStr,
				Balance:         "100",
			},
			// spec[1]: invalid balance → error after spec[0] queued.
			{
				ContractAddress: contractAddrStr,
				Account:         accountAddrStr,
				Balance:         "not-a-number",
			},
		})
	if err == nil {
		t.Fatal("expected error from invalid balance in spec[1]")
	}

	// spec[0]'s rowKey must NOT exist — its batch.Put was never .Write()'d.
	wantKey := expectedRowKey(t, contractRaw, accountRaw, 0, nil, 0)
	if _, err := storageRowEng.Get(wantKey); err == nil {
		t.Error("spec[0] rowKey present despite mid-loop error — atomic rollback FAILED")
	}
}

// TestMutateTRC20Contracts_InvalidContractAddress: address-decode
// failure halts the whole apply (atomic-rollback contract — batch
// not written). Lighter than the partial-failure test above — this
// errors before any Put is queued.
func TestMutateTRC20Contracts_InvalidContractAddress(t *testing.T) {
	contractEng, storageRowEng := openTRC20Triplet(t)

	_, err := MutateTRC20Contracts(contractEng, storageRowEng,
		[]TRC20Spec{{
			ContractAddress: "garbage-address",
			Account:         "Talsobad",
			Balance:         "1",
		}})
	if err == nil {
		t.Fatal("expected address-decode error")
	}
	// storage-row store must still have only __seed__ — no partial write.
	if _, err := storageRowEng.Get([]byte("__seed__")); err != nil {
		t.Errorf("storage-row __seed__ gone — partial write happened: %v", err)
	}
}
