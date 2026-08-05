package dbfork

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/tronprotocol/tron-deployment/internal/dbfork/db"
	"github.com/tronprotocol/tron-deployment/internal/dbfork/stores"
	pb "github.com/tronprotocol/tron-deployment/internal/tronproto/pb"
)

// TestApply_GuardsAndNoOp pins the surface contracts of Apply that
// don't need a real on-disk store:
//
//   - nil config errors out clearly (caller bug).
//   - DryRun is not yet implemented and surfaces a TODO error
//     pointing at the right task.
//   - An "empty everything" config (no witnesses, no property
//     tweaks, RetainWitnesses=true) is a true no-op — Apply returns
//     a zero Result with no engine traffic.
//
// The no-op case is what enables fork.conf authors to commit a
// `properties: {}` partial config without surprising side-effects.
func TestApply_GuardsAndNoOp(t *testing.T) {
	t.Run("nil config errors", func(t *testing.T) {
		_, err := Apply("/nonexistent", nil, Options{})
		if err == nil {
			t.Fatal("expected nil-config error")
		}
		if !strings.Contains(err.Error(), "nil config") {
			t.Errorf("err = %v; want 'nil config' message", err)
		}
	})
	t.Run("dry-run rejected with Task #150 hint", func(t *testing.T) {
		_, err := Apply("/nonexistent", &Config{}, Options{DryRun: true})
		if err == nil {
			t.Fatal("expected dry-run not-implemented error")
		}
		if !strings.Contains(err.Error(), "Task #150") {
			t.Errorf("err = %v; should reference Task #150", err)
		}
	})
	t.Run("missing data dir errors clearly", func(t *testing.T) {
		// New defensive check: Apply validates <dataDir>/database
		// exists before any section gating. Without this, an empty
		// or properties-only config would silently report "0
		// modifications" on a bogus dir (operator trap caught in
		// pass-2 review of Task #153).
		_, err := Apply("/nonexistent", &Config{}, Options{})
		if err == nil {
			t.Fatal("expected error for missing data directory")
		}
		if !strings.Contains(err.Error(), "data directory") {
			t.Errorf("err = %v; should mention data directory", err)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("err = %v; should wrap os.ErrNotExist so the CLI "+
				"can map to ExitValidationError", err)
		}
	})
	t.Run("empty config + valid dir → no-op result", func(t *testing.T) {
		// Pass the dataDir check by giving Apply a real (empty)
		// database/ subdir, then verify the section gates short-
		// circuit: zero counters with no errors.
		dataDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dataDir, "database"), 0o755); err != nil {
			t.Fatal(err)
		}
		res, err := Apply(dataDir, &Config{}, Options{})
		if err != nil {
			t.Fatalf("no-op apply: %v", err)
		}
		if res.WitnessesWritten != 0 || res.ActiveWitnessesSet != 0 ||
			res.PropertiesUpdated != 0 {
			t.Errorf("no-op result not all zero: %+v", res)
		}
	})
	t.Run("properties-only config does not touch witness stores", func(t *testing.T) {
		// Pins the principle-of-least-surprise contract: a fork.conf
		// that only tunes timing must NOT wipe the witness store
		// just because RetainWitnesses defaulted to false. We give
		// Apply a data dir with database/ but no store subdirs, so
		// the properties branch's openStore surfaces a DetectKind
		// error — the error must name the PROPERTIES store, proving
		// the witness store was never opened.
		dataDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dataDir, "database"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := Apply(dataDir, &Config{
			Properties: PropertiesSpec{MaintenanceTimeInterval: 60_000},
		}, Options{}) // RetainWitnesses defaults to false — must still skip witnesses
		if err == nil {
			t.Fatal("expected DetectKind error on missing properties store")
		}
		if !strings.Contains(err.Error(), stores.DynamicPropertiesStore) {
			t.Errorf("err = %v; want properties-store path, not witness", err)
		}
		if strings.Contains(err.Error(), stores.WitnessStore) {
			t.Errorf("err = %v; witness store should NOT have been opened", err)
		}
	})
}

// TestApply_EndToEnd is the wiring smoke test: stand up the 3 stores
// Apply touches (witness, witness_schedule, properties), call Apply
// with both witness specs + property tweaks, and verify the
// per-store state. This is what the equivalence test (Task #152)
// will scale up against a Java DbFork-produced fixture.
func TestApply_EndToEnd(t *testing.T) {
	dataDir := seedLevelDBStore(t, stores.WitnessStore)
	// reuse dataDir as the data root — seed the other two stores
	// under the same root so DetectKind + Open find them.
	seedLevelDBStoreUnder(t, dataDir, stores.WitnessScheduleStore)
	seedLevelDBStoreUnder(t, dataDir, stores.DynamicPropertiesStore)

	// Force a compaction so the .ldb sentinel files appear on disk
	// (otherwise DetectKind sees an empty dir).
	compactAllStores(t, dataDir)

	addr, raw := makeAddress(t, [20]byte{0x42})

	res, err := Apply(dataDir, &Config{
		Witnesses: []WitnessSpec{
			{Address: addr, URL: "http://w.example", VoteCount: 1000},
		},
		Properties: PropertiesSpec{
			LatestBlockHeaderTimestamp: 1_700_000_000_000,
			MaintenanceTimeInterval:    21_600_000,
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.WitnessesWritten != 1 || res.ActiveWitnessesSet != 1 {
		t.Errorf("witness counts off: %+v", res)
	}
	if res.PropertiesUpdated != 2 {
		t.Errorf("propertiesUpdated = %d; want 2 (NextMaintenanceTime omitted)",
			res.PropertiesUpdated)
	}

	// Re-open the witness store and verify the active slate.
	scheduleEng, err := db.OpenLevelDB(dataDir, stores.WitnessScheduleStore)
	if err != nil {
		t.Fatalf("re-open schedule: %v", err)
	}
	defer scheduleEng.Close()
	got, err := scheduleEng.Get([]byte(stores.KeyActiveWitnesses))
	if err != nil {
		t.Fatalf("Get active_witnesses: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("active slate = %x; want %x", got, raw)
	}

	// Spot-check one property — the BigEndian encoding pins the on-
	// disk format Java expects.
	propsEng, err := db.OpenLevelDB(dataDir, stores.DynamicPropertiesStore)
	if err != nil {
		t.Fatal(err)
	}
	defer propsEng.Close()
	tsRaw, err := propsEng.Get([]byte(stores.KeyLatestBlockHeaderTimestamp))
	if err != nil {
		t.Fatalf("Get timestamp: %v", err)
	}
	wantTS := make([]byte, 8)
	binary.BigEndian.PutUint64(wantTS, 1_700_000_000_000)
	if !bytes.Equal(tsRaw, wantTS) {
		t.Errorf("timestamp on disk = %x; want %x", tsRaw, wantTS)
	}
}

// TestApply_EndToEnd_TRC20 is the wiring smoke for the Task #149
// path: stand up contract + storage-row stores, seed a SmartContract
// proto, call Apply with one TRC20Spec, verify the storage-row was
// written. The keccak derivation correctness is covered in
// trc20_test.go; this just proves Apply opens the right stores.
func TestApply_EndToEnd_TRC20(t *testing.T) {
	dataDir := seedLevelDBStore(t, stores.ContractStore)
	seedLevelDBStoreUnder(t, dataDir, stores.StorageRowStore)
	compactAllStores(t, dataDir)

	contractAddrStr, contractRaw := makeAddress(t, [20]byte{0xe0})
	accountAddrStr, _ := makeAddress(t, [20]byte{0xe1})

	// Seed a SmartContract proto under the contract address — Apply's
	// MutateTRC20Contracts needs it to derive addressHash + version.
	{
		eng, err := db.OpenLevelDB(dataDir, stores.ContractStore)
		if err != nil {
			t.Fatal(err)
		}
		sc := &pb.SmartContract{Version: 0}
		raw, _ := proto.Marshal(sc)
		b := eng.NewBatch()
		b.Put(contractRaw, raw)
		_ = b.Write()
		b.Close()
		_ = eng.Close()
	}

	res, err := Apply(dataDir, &Config{
		TRC20Contracts: []TRC20Spec{{
			ContractAddress: contractAddrStr,
			Account:         accountAddrStr,
			Balance:         "7777",
		}},
	}, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.TRC20SlotsUpdated != 1 {
		t.Errorf("TRC20SlotsUpdated = %d; want 1", res.TRC20SlotsUpdated)
	}
}

// TestApply_EndToEnd_Accounts is the wiring smoke for the Task #148
// path: stand up the 3 account-related stores, call Apply with one
// AccountSpec, and verify Result counters + on-disk merge state.
// The deeper merge/TRC10 semantics are exercised in accounts_test.go;
// this just proves Apply routes correctly.
func TestApply_EndToEnd_Accounts(t *testing.T) {
	dataDir := seedLevelDBStore(t, stores.AccountStore)
	seedLevelDBStoreUnder(t, dataDir, stores.AccountAssetStore)
	seedLevelDBStoreUnder(t, dataDir, stores.AssetIssueV2Store)
	compactAllStores(t, dataDir)

	addr, raw := makeAddress(t, [20]byte{0x77})

	res, err := Apply(dataDir, &Config{
		Accounts: []AccountSpec{
			{Address: addr, Balance: 5_000_000, AccountName: "wired"},
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.AccountsModified != 1 {
		t.Errorf("AccountsModified = %d; want 1", res.AccountsModified)
	}

	// Re-open account store and verify the proto landed.
	accountEng, err := db.OpenLevelDB(dataDir, stores.AccountStore)
	if err != nil {
		t.Fatal(err)
	}
	defer accountEng.Close()
	got := readAccount(t, accountEng, raw)
	if got.Balance != 5_000_000 {
		t.Errorf("Balance on disk = %d; want 5_000_000", got.Balance)
	}
	if string(got.AccountName) != "wired" {
		t.Errorf("AccountName = %q; want wired", got.AccountName)
	}
}

// TestApply_SweepFailureSurfacesAsError locks the fix for the HIGH
// review finding: Apply used to discard every Engine.Close() error via
// `defer func() { _ = eng.Close() }()`, so a failed #164 .ldb->.sst
// sweep would leave an unbootable store on disk while Apply reported
// success. Apply now uses a named return + closeStore() that promotes
// the first close error.
//
// We inject a deterministic sweep failure: plant a NON-EMPTY directory
// named "*.old" in a store Apply opens. convertGoleveldbToSST removes
// .ldb rename target as a NON-EMPTY directory, so os.Rename fails —
// exactly the class of post-commit filesystem failure (ENOSPC, EACCES,
// a held-open file) the fix must surface rather than swallow.
func TestApply_SweepFailureSurfacesAsError(t *testing.T) {
	dataDir := seedLevelDBStore(t, stores.WitnessStore)
	seedLevelDBStoreUnder(t, dataDir, stores.WitnessScheduleStore)
	seedLevelDBStoreUnder(t, dataDir, stores.DynamicPropertiesStore)
	compactAllStores(t, dataDir)

	// Make the post-close sweep's os.Rename fail deterministically: plant
	// a regular file "poison.ldb" whose rename target "poison.sst" already
	// exists as a NON-EMPTY directory. convertGoleveldbToSST processes the
	// .ldb file and os.Rename(poison.ldb, poison.sst) fails because the
	// target is a non-empty directory. This injection survives the
	// dir-skip guard (the .ldb itself is a file). The witness store IS
	// opened because the config below lists a witness.
	wDir := filepath.Join(dataDir, "database", stores.WitnessStore)
	if err := os.WriteFile(filepath.Join(wDir, "poison.ldb"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wDir, "poison.sst", "child"), 0o755); err != nil {
		t.Fatal(err)
	}

	addr, _ := makeAddress(t, [20]byte{0x42})
	res, err := Apply(dataDir, &Config{
		Witnesses: []WitnessSpec{{Address: addr, VoteCount: 1000}},
	}, Options{})

	if err == nil {
		t.Fatalf("Apply returned nil error despite a failed .ldb->.sst sweep; "+
			"a corrupt store was reported as success (res=%+v)", res)
	}
	if !strings.Contains(err.Error(), "sweep") {
		t.Errorf("error should identify the post-close sweep failure, got: %v", err)
	}
}
