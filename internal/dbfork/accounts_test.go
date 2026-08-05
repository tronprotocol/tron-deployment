package dbfork

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/tronprotocol/tron-deployment/internal/dbfork/db"
	"github.com/tronprotocol/tron-deployment/internal/dbfork/stores"
	pb "github.com/tronprotocol/tron-deployment/internal/tronproto/pb"
)

// openAccountTriplet seeds the 3 stores MutateAccounts touches under
// one tempdir and returns them open. Tests use this to avoid
// re-typing the same plumbing five times.
func openAccountTriplet(t *testing.T) (
	accountEng, accountAssetEng, assetIssueV2Eng db.Engine,
) {
	t.Helper()
	dataDir := seedLevelDBStore(t, stores.AccountStore)
	seedLevelDBStoreUnder(t, dataDir, stores.AccountAssetStore)
	seedLevelDBStoreUnder(t, dataDir, stores.AssetIssueV2Store)
	var err error
	accountEng, err = db.OpenLevelDB(dataDir, stores.AccountStore)
	if err != nil {
		t.Fatalf("open account: %v", err)
	}
	t.Cleanup(func() { _ = accountEng.Close() })
	accountAssetEng, err = db.OpenLevelDB(dataDir, stores.AccountAssetStore)
	if err != nil {
		t.Fatalf("open account-asset: %v", err)
	}
	t.Cleanup(func() { _ = accountAssetEng.Close() })
	assetIssueV2Eng, err = db.OpenLevelDB(dataDir, stores.AssetIssueV2Store)
	if err != nil {
		t.Fatalf("open asset-issue-v2: %v", err)
	}
	t.Cleanup(func() { _ = assetIssueV2Eng.Close() })
	return accountEng, accountAssetEng, assetIssueV2Eng
}

// seedAsset registers a TRC10 asset in assetIssueV2Store under the
// given ID so MutateAccounts' existence-probe passes. Value content
// doesn't matter for DbFork's logic — it only checks presence.
func seedAsset(t *testing.T, eng db.Engine, assetID string) {
	t.Helper()
	b := eng.NewBatch()
	b.Put([]byte(assetID), []byte{0x01}) // marker
	if err := b.Write(); err != nil {
		t.Fatalf("seed asset %s: %v", assetID, err)
	}
	b.Close()
}

// TestMutateAccounts_MergeUpdatesExisting is the canonical merge
// test: pre-populate one account with several fields set, then run
// MutateAccounts with a spec that touches only a subset — the
// unmentioned fields MUST survive verbatim.
//
// This is the contract that distinguishes accounts from witnesses
// (witnesses wipe-and-rewrite; accounts merge). A regression here
// would wipe arbitrary fields off live mainnet accounts during a
// shadow fork.
func TestMutateAccounts_MergeUpdatesExisting(t *testing.T) {
	accountEng, accountAssetEng, assetIssueV2Eng := openAccountTriplet(t)

	addr, raw := makeAddress(t, [20]byte{0x42})

	// Pre-existing account with several fields set — these are
	// "live mainnet state" we don't want clobbered.
	existing := &pb.Account{
		Address:     raw,
		Balance:     100_000_000, // 100 TRX
		AccountName: []byte("OriginalName"),
		Type:        pb.AccountType_AssetIssue,
		CreateTime:  1_500_000_000_000,
		Allowance:   42, // a field MutateAccounts never touches
	}
	existingBytes, _ := proto.Marshal(existing)
	{
		b := accountEng.NewBatch()
		b.Put(raw, existingBytes)
		_ = b.Write()
		b.Close()
	}

	// Update only balance. Name/type/createTime/allowance must
	// survive.
	modified, err := MutateAccounts(accountEng, accountAssetEng, assetIssueV2Eng,
		[]AccountSpec{{Address: addr, Balance: 999_000_000}})
	if err != nil {
		t.Fatalf("MutateAccounts: %v", err)
	}
	if modified != 1 {
		t.Errorf("modified = %d; want 1", modified)
	}

	updatedBytes, err := accountEng.Get(raw)
	if err != nil {
		t.Fatalf("Get account: %v", err)
	}
	var got pb.Account
	if err := proto.Unmarshal(updatedBytes, &got); err != nil {
		t.Fatal(err)
	}
	if got.Balance != 999_000_000 {
		t.Errorf("Balance = %d; want 999_000_000", got.Balance)
	}
	if string(got.AccountName) != "OriginalName" {
		t.Errorf("AccountName = %q; merge LOST the original name", got.AccountName)
	}
	if got.Type != pb.AccountType_AssetIssue {
		t.Errorf("Type = %v; merge LOST the AssetIssue type", got.Type)
	}
	if got.CreateTime != 1_500_000_000_000 {
		t.Errorf("CreateTime = %d; merge LOST it", got.CreateTime)
	}
	if got.Allowance != 42 {
		t.Errorf("Allowance = %d; merge LOST untouched-by-DbFork field", got.Allowance)
	}
}

// TestMutateAccounts_CreatesStubForNewAddress: an address not yet
// in accountStore gets a bare proto created with only Address set,
// then selectively populated from the spec. Matches java DbFork
// :242-246.
func TestMutateAccounts_CreatesStubForNewAddress(t *testing.T) {
	accountEng, accountAssetEng, assetIssueV2Eng := openAccountTriplet(t)
	addr, raw := makeAddress(t, [20]byte{0x99})

	_, err := MutateAccounts(accountEng, accountAssetEng, assetIssueV2Eng,
		[]AccountSpec{{Address: addr, Balance: 1, AccountName: "NewKid"}})
	if err != nil {
		t.Fatalf("MutateAccounts: %v", err)
	}

	got := readAccount(t, accountEng, raw)
	if !bytes.Equal(got.Address, raw) {
		t.Errorf("new account Address = %x; want %x", got.Address, raw)
	}
	if got.Balance != 1 {
		t.Errorf("new account Balance = %d; want 1", got.Balance)
	}
	if string(got.AccountName) != "NewKid" {
		t.Errorf("AccountName = %q; want NewKid", got.AccountName)
	}
	// Untouched fields must be at proto zero — no inherited state
	// from anywhere (defensive check that the stub started clean).
	if got.CreateTime != 0 {
		t.Errorf("CreateTime = %d; new stub should be zero", got.CreateTime)
	}
}

// TestMutateAccounts_BalanceZeroOrNegativeSkipped pins java's
// `balance > 0` gate (:249). Setting Balance=0 in fork.conf must
// preserve the existing balance, not overwrite it with zero — a
// silent zero-overwrite would torch real funds on a shadow fork.
func TestMutateAccounts_BalanceZeroOrNegativeSkipped(t *testing.T) {
	accountEng, accountAssetEng, assetIssueV2Eng := openAccountTriplet(t)
	addr, raw := makeAddress(t, [20]byte{0x55})

	// Existing account with non-zero balance.
	seedAccount(t, accountEng, raw, &pb.Account{Address: raw, Balance: 5_000})

	// Spec with Balance=0 (zero-value Go field) should NOT touch
	// the on-disk balance.
	_, err := MutateAccounts(accountEng, accountAssetEng, assetIssueV2Eng,
		[]AccountSpec{{Address: addr, AccountName: "RenamedOnly"}})
	if err != nil {
		t.Fatal(err)
	}

	got := readAccount(t, accountEng, raw)
	if got.Balance != 5_000 {
		t.Errorf("Balance = %d; spec.Balance=0 should NOT have overwritten", got.Balance)
	}
	if string(got.AccountName) != "RenamedOnly" {
		t.Errorf("AccountName = %q; rename should still apply", got.AccountName)
	}
}

// TestMutateAccounts_TRC10AssetOptimized_WritesCompositeKey pins
// the AccountAssetStore composite-key format and value encoding
// when the account has asset_optimized=true. java DbFork :272-274.
func TestMutateAccounts_TRC10AssetOptimized_WritesCompositeKey(t *testing.T) {
	accountEng, accountAssetEng, assetIssueV2Eng := openAccountTriplet(t)
	addr, raw := makeAddress(t, [20]byte{0xa1})

	const assetID = "1000001"
	seedAsset(t, assetIssueV2Eng, assetID)
	// Existing account with asset_optimized=true.
	seedAccount(t, accountEng, raw, &pb.Account{
		Address:        raw,
		AssetOptimized: true,
	})

	_, err := MutateAccounts(accountEng, accountAssetEng, assetIssueV2Eng,
		[]AccountSpec{{
			Address:      addr,
			TRC10ID:      assetID,
			TRC10Balance: 500_000_000,
		}})
	if err != nil {
		t.Fatal(err)
	}

	// Composite key = 21-byte addr || ASCII("1000001") = 28 bytes.
	wantKey := append(append([]byte{}, raw...), []byte(assetID)...)
	if len(wantKey) != 21+len(assetID) {
		t.Fatalf("composite key length sanity: %d", len(wantKey))
	}
	gotVal, err := accountAssetEng.Get(wantKey)
	if err != nil {
		t.Fatalf("Get composite key: %v", err)
	}
	wantVal := make([]byte, 8)
	binary.BigEndian.PutUint64(wantVal, 500_000_000)
	if !bytes.Equal(gotVal, wantVal) {
		t.Errorf("AccountAsset value = %x; want %x (BE long)", gotVal, wantVal)
	}

	// AssetV2 map on the account must NOT have been touched in the
	// optimized branch — java does NOT dual-write.
	got := readAccount(t, accountEng, raw)
	if v, present := got.AssetV2[assetID]; present {
		t.Errorf("AssetV2[%s] = %d; should be absent in optimized mode", assetID, v)
	}
}

// TestMutateAccounts_TRC10NonOptimized_MergesIntoAssetV2 pins the
// other branch: asset_optimized=false → write into Account.asset_v2
// map, preserving any existing entries for OTHER tokens.
func TestMutateAccounts_TRC10NonOptimized_MergesIntoAssetV2(t *testing.T) {
	accountEng, accountAssetEng, assetIssueV2Eng := openAccountTriplet(t)
	addr, raw := makeAddress(t, [20]byte{0xa2})

	const newID = "1000002"
	const keepID = "1000001"
	seedAsset(t, assetIssueV2Eng, newID)

	// Pre-existing account with one holding for keepID. After the
	// spec runs we expect BOTH keepID (preserved) AND newID (added).
	seedAccount(t, accountEng, raw, &pb.Account{
		Address:        raw,
		AssetOptimized: false,
		AssetV2:        map[string]int64{keepID: 1_000},
	})

	_, err := MutateAccounts(accountEng, accountAssetEng, assetIssueV2Eng,
		[]AccountSpec{{
			Address:      addr,
			TRC10ID:      newID,
			TRC10Balance: 2_000,
		}})
	if err != nil {
		t.Fatal(err)
	}

	got := readAccount(t, accountEng, raw)
	if got.AssetV2[keepID] != 1_000 {
		t.Errorf("AssetV2[%s] = %d; existing holding LOST", keepID, got.AssetV2[keepID])
	}
	if got.AssetV2[newID] != 2_000 {
		t.Errorf("AssetV2[%s] = %d; want 2000", newID, got.AssetV2[newID])
	}

	// AccountAssetStore must be empty for this address — non-optimized
	// path NEVER writes there.
	compositeKey := append(append([]byte{}, raw...), []byte(newID)...)
	if _, err := accountAssetEng.Get(compositeKey); err == nil {
		t.Error("AccountAssetStore unexpectedly written in non-optimized branch")
	}
}

// TestMutateAccounts_TRC10MissingAsset_Skipped: when the referenced
// trc10Id isn't in assetIssueV2Store, the holding is SILENTLY
// skipped (log line only). The rest of the account update still
// applies. Matches java DbFork :281-284.
func TestMutateAccounts_TRC10MissingAsset_Skipped(t *testing.T) {
	accountEng, accountAssetEng, assetIssueV2Eng := openAccountTriplet(t)
	addr, raw := makeAddress(t, [20]byte{0xa3})

	// Deliberately do NOT seed assetIssueV2Eng for "9999999".
	modified, err := MutateAccounts(accountEng, accountAssetEng, assetIssueV2Eng,
		[]AccountSpec{{
			Address:      addr,
			Balance:      777,
			TRC10ID:      "9999999",
			TRC10Balance: 1,
		}})
	if err != nil {
		t.Fatalf("MutateAccounts: %v (should skip TRC10, not error)", err)
	}
	if modified != 1 {
		t.Errorf("modified = %d; want 1 (balance update still applies)", modified)
	}

	got := readAccount(t, accountEng, raw)
	if got.Balance != 777 {
		t.Errorf("Balance = %d; balance write should still happen", got.Balance)
	}
	if len(got.AssetV2) != 0 {
		t.Errorf("AssetV2 = %v; missing asset should NOT have been added", got.AssetV2)
	}
}

// TestMutateAccounts_AccountTypeEnum: valid enum string → correct
// AccountType; invalid string → typed error mentioning the bad value.
func TestMutateAccounts_AccountTypeEnum(t *testing.T) {
	accountEng, accountAssetEng, assetIssueV2Eng := openAccountTriplet(t)
	addr, raw := makeAddress(t, [20]byte{0xa4})

	_, err := MutateAccounts(accountEng, accountAssetEng, assetIssueV2Eng,
		[]AccountSpec{{Address: addr, AccountType: "Contract"}})
	if err != nil {
		t.Fatalf("MutateAccounts: %v", err)
	}
	got := readAccount(t, accountEng, raw)
	if got.Type != pb.AccountType_Contract {
		t.Errorf("Type = %v; want Contract", got.Type)
	}

	_, err = MutateAccounts(accountEng, accountAssetEng, assetIssueV2Eng,
		[]AccountSpec{{Address: addr, AccountType: "NotARealType"}})
	if err == nil {
		t.Fatal("expected error for bogus accountType")
	}
	if !strings.Contains(err.Error(), "NotARealType") {
		t.Errorf("err = %v; should mention the bad enum value", err)
	}
}

// TestMutateAccounts_OwnerPermissionShape pins the exact Permission
// proto shape DbFork writes — single key, threshold 1, type Owner,
// name "owner", id 0, parentId 0. Verbatim from java-tron's
// AccountCapsule.createDefaultOwnerPermission (chainbase :194-208).
//
// A drift here would invalidate any signature the new owner tried to
// make against the shadow chain.
func TestMutateAccounts_OwnerPermissionShape(t *testing.T) {
	accountEng, accountAssetEng, assetIssueV2Eng := openAccountTriplet(t)
	addr, raw := makeAddress(t, [20]byte{0xa5})
	ownerAddr, ownerRaw := makeAddress(t, [20]byte{0xa6})

	// Pre-seed with a non-empty ActivePermission so the test can
	// also pin the side effect of java's
	// AccountCapsule.updatePermissions(owner, null, null) — line
	// 1311 calls builder.clearActivePermission() unconditionally,
	// so any prior active perms must be gone after the Owner rewrite.
	staleActive := &pb.Permission{
		Type:           pb.Permission_Active,
		Id:             2,
		PermissionName: "stale-active",
		Threshold:      1,
		Keys: []*pb.Key{
			{Address: raw, Weight: 1},
		},
	}
	seedAccount(t, accountEng, raw, &pb.Account{
		Address:          raw,
		ActivePermission: []*pb.Permission{staleActive},
	})

	_, err := MutateAccounts(accountEng, accountAssetEng, assetIssueV2Eng,
		[]AccountSpec{{Address: addr, Owner: ownerAddr}})
	if err != nil {
		t.Fatal(err)
	}

	got := readAccount(t, accountEng, raw)
	if got.OwnerPermission == nil {
		t.Fatal("OwnerPermission not set")
	}
	op := got.OwnerPermission
	if op.Type != pb.Permission_Owner {
		t.Errorf("OwnerPermission.Type = %v; want Owner", op.Type)
	}
	if op.Id != 0 {
		t.Errorf("OwnerPermission.Id = %d; want 0", op.Id)
	}
	if op.PermissionName != "owner" {
		t.Errorf("OwnerPermission.PermissionName = %q; want owner", op.PermissionName)
	}
	if op.Threshold != 1 {
		t.Errorf("OwnerPermission.Threshold = %d; want 1", op.Threshold)
	}
	if op.ParentId != 0 {
		t.Errorf("OwnerPermission.ParentId = %d; want 0", op.ParentId)
	}
	if len(op.Keys) != 1 {
		t.Fatalf("OwnerPermission.Keys len = %d; want 1", len(op.Keys))
	}
	if !bytes.Equal(op.Keys[0].Address, ownerRaw) {
		t.Errorf("Keys[0].Address = %x; want %x", op.Keys[0].Address, ownerRaw)
	}
	if op.Keys[0].Weight != 1 {
		t.Errorf("Keys[0].Weight = %d; want 1", op.Keys[0].Weight)
	}
	// ActivePermission must be cleared by the owner update — matches
	// AccountCapsule.java:1311 clearActivePermission() unconditional call.
	if len(got.ActivePermission) != 0 {
		t.Errorf("ActivePermission len = %d; want 0 (java clears unconditionally on owner update)",
			len(got.ActivePermission))
	}
}

// TestMutateAccounts_InvalidAddress: a malformed Address halts the
// whole apply with no partial writes (atomic-per-batch contract).
func TestMutateAccounts_InvalidAddress(t *testing.T) {
	accountEng, accountAssetEng, assetIssueV2Eng := openAccountTriplet(t)

	_, err := MutateAccounts(accountEng, accountAssetEng, assetIssueV2Eng,
		[]AccountSpec{{Address: "garbage", Balance: 1}})
	if err == nil {
		t.Fatal("expected error for invalid address")
	}
	if _, err := accountEng.Get([]byte("__seed__")); err != nil {
		t.Errorf("__seed__ key gone — partial write on validation error: %v", err)
	}
}

// TestMutateAccounts_MultipleTRC10ForSameAddress pins the
// "one-entry-one-holding" constraint by listing the SAME address
// twice with different trc10Ids. BOTH holdings must land in
// Account.AssetV2 — matching java DbFork's per-iteration synchronous
// put (DbFork.java:288), which lets the second iteration read back
// the first iteration's update. The Go port stages account mutations
// in an in-memory map across specs to mirror this contract; a
// regression would silently drop holdings on shadow forks that fund
// multi-token treasuries from a single account entry per token.
func TestMutateAccounts_MultipleTRC10ForSameAddress(t *testing.T) {
	accountEng, accountAssetEng, assetIssueV2Eng := openAccountTriplet(t)
	addr, raw := makeAddress(t, [20]byte{0xa7})

	seedAsset(t, assetIssueV2Eng, "1000001")
	seedAsset(t, assetIssueV2Eng, "1000002")

	_, err := MutateAccounts(accountEng, accountAssetEng, assetIssueV2Eng,
		[]AccountSpec{
			{Address: addr, TRC10ID: "1000001", TRC10Balance: 100},
			{Address: addr, TRC10ID: "1000002", TRC10Balance: 200},
		})
	if err != nil {
		t.Fatal(err)
	}

	got := readAccount(t, accountEng, raw)
	if got.AssetV2["1000001"] != 100 {
		t.Errorf("AssetV2[1000001] = %d; want 100 (first spec's holding)",
			got.AssetV2["1000001"])
	}
	if got.AssetV2["1000002"] != 200 {
		t.Errorf("AssetV2[1000002] = %d; want 200 (second spec's holding)",
			got.AssetV2["1000002"])
	}
}

// TestMutateAccounts_NoFieldsStillRewrites pins the contract from
// java DbFork.java:288: every spec that flows through the loop ends
// with an unconditional `accountStore.put(address, capsule.getData())`,
// even when no field was mutated. A future "optimization" that elides
// the put for no-op specs would diverge from java's behavior on the
// proto's serialized form (LevelDB sequence-number bump etc.).
func TestMutateAccounts_NoFieldsStillRewrites(t *testing.T) {
	accountEng, accountAssetEng, assetIssueV2Eng := openAccountTriplet(t)
	addr, raw := makeAddress(t, [20]byte{0xb1})

	// Pre-seed with a known proto so we can detect a re-marshal by
	// observing the byte sequence on disk equals the deterministic
	// re-marshal of the same proto.
	original := &pb.Account{Address: raw, Balance: 7}
	seedAccount(t, accountEng, raw, original)

	// Spec with ONLY Address — no Balance, no Name, no TRC10.
	modified, err := MutateAccounts(accountEng, accountAssetEng, assetIssueV2Eng,
		[]AccountSpec{{Address: addr}})
	if err != nil {
		t.Fatal(err)
	}
	if modified != 1 {
		t.Errorf("modified = %d; want 1 (java increments accounts.size even for no-op specs)", modified)
	}

	// Account proto must still exist on disk with the original fields.
	got := readAccount(t, accountEng, raw)
	if got.Balance != 7 {
		t.Errorf("Balance = %d; pre-existing field clobbered by no-op spec", got.Balance)
	}
}

// TestMutateAccounts_PartialFailureRollsBackBothStores pins the
// cross-store atomicity contract: when an error is returned mid-loop
// (e.g., spec[1] has a bad AccountType), NO writes from spec[0] —
// neither to AccountStore nor to AccountAssetStore — must be flushed.
// The contract relies on the per-store batches only being .Write()'d
// after the full loop completes successfully.
func TestMutateAccounts_PartialFailureRollsBackBothStores(t *testing.T) {
	accountEng, accountAssetEng, assetIssueV2Eng := openAccountTriplet(t)
	addrOK, rawOK := makeAddress(t, [20]byte{0xb2})
	addrBad, _ := makeAddress(t, [20]byte{0xb3})

	const assetID = "1000003"
	seedAsset(t, assetIssueV2Eng, assetID)
	// AssetOptimized=true so spec[0]'s TRC10 write goes to
	// AccountAssetStore (exercising both batches' rollback).
	seedAccount(t, accountEng, rawOK, &pb.Account{
		Address:        rawOK,
		AssetOptimized: true,
	})

	_, err := MutateAccounts(accountEng, accountAssetEng, assetIssueV2Eng,
		[]AccountSpec{
			// spec[0] is valid — its writes get queued in both batches.
			{Address: addrOK, TRC10ID: assetID, TRC10Balance: 42},
			// spec[1] errors → returns before batch.Write.
			{Address: addrBad, AccountType: "NotAValidType"},
		})
	if err == nil {
		t.Fatal("expected error from invalid AccountType")
	}

	// AccountStore: spec[0]'s pending Account proto must NOT be on disk.
	// The pre-existing seeded account is unchanged (still has only the
	// AssetOptimized=true bit set; no new fields from this call).
	got := readAccount(t, accountEng, rawOK)
	if got.Balance != 0 {
		t.Errorf("seeded account balance changed despite mid-loop error: %d", got.Balance)
	}

	// AccountAssetStore: composite key from spec[0] must NOT exist —
	// the batch's Put was queued but never .Write()'d.
	compositeKey := append(append([]byte{}, rawOK...), []byte(assetID)...)
	if _, err := accountAssetEng.Get(compositeKey); err == nil {
		t.Error("AccountAssetStore composite key written despite mid-loop error — atomic rollback FAILED")
	}
}

// --- helpers ---------------------------------------------------------------

// seedAccount writes a proto.Marshal(account) under the address key
// so merge-tests can pre-populate "live" state.
func seedAccount(t *testing.T, eng db.Engine, addr []byte, a *pb.Account) {
	t.Helper()
	raw, err := proto.Marshal(a)
	if err != nil {
		t.Fatalf("seedAccount marshal: %v", err)
	}
	b := eng.NewBatch()
	b.Put(addr, raw)
	if err := b.Write(); err != nil {
		t.Fatalf("seedAccount write: %v", err)
	}
	b.Close()
}

// readAccount fetches and unmarshals the Account at addr. Fatals
// the test on any failure — the merge tests need this to be a hard
// assertion.
func readAccount(t *testing.T, eng db.Engine, addr []byte) *pb.Account {
	t.Helper()
	raw, err := eng.Get(addr)
	if err != nil {
		t.Fatalf("readAccount Get: %v", err)
	}
	a := &pb.Account{}
	if err := proto.Unmarshal(raw, a); err != nil {
		t.Fatalf("readAccount Unmarshal: %v", err)
	}
	return a
}
