package dbfork

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/syndtr/goleveldb/leveldb"

	"github.com/tronprotocol/tron-deployment/internal/dbfork/db"
	"github.com/tronprotocol/tron-deployment/internal/dbfork/stores"
	pb "github.com/tronprotocol/tron-deployment/internal/tronproto/pb"
)

// seedLevelDBStore creates an empty (but openable) java-tron-layout
// LevelDB store at <dataDir>/database/<storeName>/ for tests. It
// seeds one key so the engine has something on disk; tests that need
// an "actually-empty" view should erase it themselves via the
// engine's batch.
//
// Returns the dataDir for use with OpenLevelDB.
func seedLevelDBStore(t *testing.T, storeName string) (dataDir string) {
	t.Helper()
	dataDir = t.TempDir()
	storeDir := filepath.Join(dataDir, "database", storeName)
	if err := os.MkdirAll(filepath.Dir(storeDir), 0o755); err != nil {
		t.Fatal(err)
	}
	// goleveldb won't create the LDB on disk unless we put + close.
	// OpenLevelDB sets ErrorIfMissing so we must materialise it.
	ldb, err := leveldb.OpenFile(storeDir, nil)
	if err != nil {
		t.Fatalf("seed %s: %v", storeName, err)
	}
	if err := ldb.Put([]byte("__seed__"), []byte("seed"), nil); err != nil {
		t.Fatalf("seed put %s: %v", storeName, err)
	}
	if err := ldb.Close(); err != nil {
		t.Fatalf("seed close %s: %v", storeName, err)
	}
	return dataDir
}

// makeAddress builds a valid Base58Check TRON address from a 20-byte
// payload, so tests can generate as many valid witness addresses as
// they need without copying real mainnet keys into source. Mirrors
// java-tron's `Commons.encode58Check`: body = 0x41 || payload, then
// append SHA256(SHA256(body))[0:4] and Base58-encode.
func makeAddress(t *testing.T, payload [20]byte) (encoded string, raw []byte) {
	t.Helper()
	body := make([]byte, 21)
	body[0] = 0x41
	copy(body[1:], payload[:])
	first := sha256.Sum256(body)
	second := sha256.Sum256(first[:])
	full := append(body, second[:4]...)

	// Base58 encode via positional repeated divide-by-58.
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	// Count leading zero bytes for the '1' prefix preservation.
	zeros := 0
	for _, b := range full {
		if b != 0 {
			break
		}
		zeros++
	}
	// Divide-by-58 loop on a working buffer (mutated in place).
	work := make([]byte, len(full))
	copy(work, full)
	var out []byte
	startAt := zeros
	for startAt < len(work) {
		rem := 0
		for i := startAt; i < len(work); i++ {
			v := rem*256 + int(work[i])
			work[i] = byte(v / 58)
			rem = v % 58
		}
		if work[startAt] == 0 {
			startAt++
		}
		out = append(out, alphabet[rem])
	}
	// Prepend zero indicators + reverse.
	for range zeros {
		out = append(out, alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out), body
}

// TestMutateWitnesses_EraseAndWrite is the happy-path smoke test:
// pre-populate witnessStore with a stale witness + a stale active-
// witness slate, then call MutateWitnesses with 3 new specs and
// retainExisting=false. Verify the stale witness is gone, the 3 new
// ones are present with correct proto fields, and the active slate
// equals the 3 new addresses concatenated in vote-count-desc order.
func TestMutateWitnesses_EraseAndWrite(t *testing.T) {
	witDir := seedLevelDBStore(t, stores.WitnessStore)
	schedDir := seedLevelDBStore(t, stores.WitnessScheduleStore)

	witnessEng, err := db.OpenLevelDB(witDir, stores.WitnessStore)
	if err != nil {
		t.Fatalf("open witness: %v", err)
	}
	defer witnessEng.Close()
	scheduleEng, err := db.OpenLevelDB(schedDir, stores.WitnessScheduleStore)
	if err != nil {
		t.Fatalf("open schedule: %v", err)
	}
	defer scheduleEng.Close()

	// Pre-populate a stale witness + active-witness key.
	staleAddr, _ := makeAddress(t, [20]byte{0xde, 0xad, 0xbe, 0xef})
	staleRaw, _ := DecodeAddress(staleAddr)
	stalePB, _ := proto.Marshal(&pb.Witness{Address: staleRaw, VoteCount: 999})
	{
		b := witnessEng.NewBatch()
		b.Put(staleRaw, stalePB)
		if err := b.Write(); err != nil {
			t.Fatal(err)
		}
		b.Close()
	}
	{
		b := scheduleEng.NewBatch()
		b.Put([]byte(stores.KeyActiveWitnesses), append([]byte{}, staleRaw...))
		if err := b.Write(); err != nil {
			t.Fatal(err)
		}
		b.Close()
	}

	// Build 3 new specs with distinct vote counts so the active-set
	// sort order is unambiguous (no tiebreaker needed for this test).
	addrA, rawA := makeAddress(t, [20]byte{0x01})
	addrB, rawB := makeAddress(t, [20]byte{0x02})
	addrC, rawC := makeAddress(t, [20]byte{0x03})

	specs := []WitnessSpec{
		{Address: addrA, URL: "http://a.example", VoteCount: 100},
		{Address: addrB, URL: "http://b.example", VoteCount: 300}, // highest
		{Address: addrC, URL: "http://c.example", VoteCount: 200},
	}

	written, active, err := MutateWitnesses(witnessEng, scheduleEng, specs, false)
	if err != nil {
		t.Fatalf("MutateWitnesses: %v", err)
	}
	if written != 3 {
		t.Errorf("witnessesWritten = %d; want 3", written)
	}
	if active != 3 {
		t.Errorf("activeSet = %d; want 3", active)
	}

	// Stale witness must be gone.
	if _, err := witnessEng.Get(staleRaw); err == nil {
		t.Error("stale witness still present after erase")
	}
	// "__seed__" key (from seed helper) must also be gone — the erase
	// is a wholesale wipe.
	if _, err := witnessEng.Get([]byte("__seed__")); err == nil {
		t.Error("__seed__ still present after wholesale erase")
	}

	// Each new witness present and decodes correctly.
	for _, want := range []struct {
		addr      []byte
		url       string
		voteCount int64
	}{
		{rawA, "http://a.example", 100},
		{rawB, "http://b.example", 300},
		{rawC, "http://c.example", 200},
	} {
		raw, err := witnessEng.Get(want.addr)
		if err != nil {
			t.Errorf("Get %x: %v", want.addr, err)
			continue
		}
		var w pb.Witness
		if err := proto.Unmarshal(raw, &w); err != nil {
			t.Errorf("unmarshal %x: %v", want.addr, err)
			continue
		}
		if !bytes.Equal(w.Address, want.addr) {
			t.Errorf("w.Address = %x; want %x", w.Address, want.addr)
		}
		if w.Url != want.url {
			t.Errorf("w.Url = %q; want %q", w.Url, want.url)
		}
		if w.VoteCount != want.voteCount {
			t.Errorf("w.VoteCount = %d; want %d", w.VoteCount, want.voteCount)
		}
		if !w.IsJobs {
			t.Errorf("w.IsJobs should be true (witness opts in to scheduling)")
		}
	}

	// Active-witness slate: B (300) || C (200) || A (100), each 21 bytes.
	gotActive, err := scheduleEng.Get([]byte(stores.KeyActiveWitnesses))
	if err != nil {
		t.Fatalf("Get active_witnesses: %v", err)
	}
	wantActive := append(append(append([]byte{}, rawB...), rawC...), rawA...)
	if !bytes.Equal(gotActive, wantActive) {
		t.Errorf("active slate = %x; want %x", gotActive, wantActive)
	}
}

// TestMutateWitnesses_RetainExisting: retainExisting=true must
// preserve the pre-existing witness AND not clear the active-
// witness slate from a prior fork. The new active slate is computed
// only from the new specs (java DbFork semantics — retain means
// keep witnessStore entries, NOT roll them into the active set).
func TestMutateWitnesses_RetainExisting(t *testing.T) {
	witDir := seedLevelDBStore(t, stores.WitnessStore)
	schedDir := seedLevelDBStore(t, stores.WitnessScheduleStore)

	witnessEng, err := db.OpenLevelDB(witDir, stores.WitnessStore)
	if err != nil {
		t.Fatal(err)
	}
	defer witnessEng.Close()
	scheduleEng, err := db.OpenLevelDB(schedDir, stores.WitnessScheduleStore)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduleEng.Close()

	keepAddr, _ := makeAddress(t, [20]byte{0xaa})
	keepRaw, _ := DecodeAddress(keepAddr)
	keepPB, _ := proto.Marshal(&pb.Witness{Address: keepRaw, VoteCount: 42})
	{
		b := witnessEng.NewBatch()
		b.Put(keepRaw, keepPB)
		if err := b.Write(); err != nil {
			t.Fatal(err)
		}
		b.Close()
	}

	newAddr, newRaw := makeAddress(t, [20]byte{0xbb})
	_, _, err = MutateWitnesses(witnessEng, scheduleEng, []WitnessSpec{
		{Address: newAddr, VoteCount: 500},
	}, true)
	if err != nil {
		t.Fatal(err)
	}

	// Pre-existing witness must survive.
	if _, err := witnessEng.Get(keepRaw); err != nil {
		t.Errorf("pre-existing witness gone under retainExisting=true: %v", err)
	}
	// New witness was added.
	if _, err := witnessEng.Get(newRaw); err != nil {
		t.Errorf("new witness missing: %v", err)
	}
}

// TestMutateWitnesses_CapAtMaxActiveWitnessNum: 30 specs → only 27
// addresses make the active slate (the top 27 by vote count).
// witnessStore gets all 30 entries; only the schedule is capped.
func TestMutateWitnesses_CapAtMaxActiveWitnessNum(t *testing.T) {
	witDir := seedLevelDBStore(t, stores.WitnessStore)
	schedDir := seedLevelDBStore(t, stores.WitnessScheduleStore)

	witnessEng, _ := db.OpenLevelDB(witDir, stores.WitnessStore)
	defer witnessEng.Close()
	scheduleEng, _ := db.OpenLevelDB(schedDir, stores.WitnessScheduleStore)
	defer scheduleEng.Close()

	specs := make([]WitnessSpec, 30)
	for i := range specs {
		var payload [20]byte
		// Distinct payload per spec.
		payload[0] = byte(i + 1)
		addr, _ := makeAddress(t, payload)
		specs[i] = WitnessSpec{
			Address: addr,
			// Vote count = i so spec[29] has the highest.
			VoteCount: int64(i),
		}
	}

	written, active, err := MutateWitnesses(witnessEng, scheduleEng, specs, false)
	if err != nil {
		t.Fatalf("MutateWitnesses: %v", err)
	}
	if written != 30 {
		t.Errorf("written = %d; want 30", written)
	}
	if active != stores.MaxActiveWitnessNum {
		t.Errorf("active = %d; want %d", active, stores.MaxActiveWitnessNum)
	}

	gotActive, err := scheduleEng.Get([]byte(stores.KeyActiveWitnesses))
	if err != nil {
		t.Fatalf("Get active: %v", err)
	}
	expectedLen := stores.MaxActiveWitnessNum * stores.AddressLength
	if len(gotActive) != expectedLen {
		t.Errorf("active slate length = %d; want %d", len(gotActive), expectedLen)
	}
}

// TestMutateWitnesses_EmptySpecsWipes: zero specs + retainExisting=false
// must wipe witnessStore and clear the active-witness key. This is the
// "tear down all witnesses for replacement in a follow-up apply" case.
func TestMutateWitnesses_EmptySpecsWipes(t *testing.T) {
	witDir := seedLevelDBStore(t, stores.WitnessStore)
	schedDir := seedLevelDBStore(t, stores.WitnessScheduleStore)

	witnessEng, _ := db.OpenLevelDB(witDir, stores.WitnessStore)
	defer witnessEng.Close()
	scheduleEng, _ := db.OpenLevelDB(schedDir, stores.WitnessScheduleStore)
	defer scheduleEng.Close()

	// Plant a witness and an active key.
	addr, _ := makeAddress(t, [20]byte{0xfe})
	raw, _ := DecodeAddress(addr)
	wb := witnessEng.NewBatch()
	wb.Put(raw, []byte("doesntmatter"))
	_ = wb.Write()
	wb.Close()
	sb := scheduleEng.NewBatch()
	sb.Put([]byte(stores.KeyActiveWitnesses), raw)
	_ = sb.Write()
	sb.Close()

	written, active, err := MutateWitnesses(witnessEng, scheduleEng, nil, false)
	if err != nil {
		t.Fatalf("MutateWitnesses: %v", err)
	}
	if written != 0 || active != 0 {
		t.Errorf("written=%d, active=%d; want 0,0", written, active)
	}
	if _, err := witnessEng.Get(raw); err == nil {
		t.Error("witness should be wiped")
	}
	if _, err := scheduleEng.Get([]byte(stores.KeyActiveWitnesses)); err == nil {
		t.Error("active-witness key should be cleared")
	}
}

// TestMutateWitnesses_InvalidAddress: a malformed Address halts the
// whole apply with no partial writes. Both stores must be untouched
// (atomic-per-store contract relies on the batch only being written
// once all specs decoded; decode-first / write-second).
func TestMutateWitnesses_InvalidAddress(t *testing.T) {
	witDir := seedLevelDBStore(t, stores.WitnessStore)
	schedDir := seedLevelDBStore(t, stores.WitnessScheduleStore)

	witnessEng, _ := db.OpenLevelDB(witDir, stores.WitnessStore)
	defer witnessEng.Close()
	scheduleEng, _ := db.OpenLevelDB(schedDir, stores.WitnessScheduleStore)
	defer scheduleEng.Close()

	// Use retainExisting=true so the test asserts NOTHING is written
	// (otherwise the wipe step would happen unconditionally).
	_, _, err := MutateWitnesses(witnessEng, scheduleEng, []WitnessSpec{
		{Address: "not-a-real-address", VoteCount: 1},
	}, true)
	if err == nil {
		t.Fatal("expected error for invalid address")
	}

	// __seed__ from setup must still be there — no partial write.
	if _, err := witnessEng.Get([]byte("__seed__")); err != nil {
		t.Errorf("witnessStore unexpectedly modified on validation error: %v", err)
	}
}

// TestMutateWitnesses_TiebreakerByAddress: equal vote counts → active
// slate sorted by raw address bytes ASC. Documented divergence from
// java DbFork (which uses ByteString.hashCode); the equivalence test
// in Task #152 pins fork.conf inputs to avoid this case in practice.
func TestMutateWitnesses_TiebreakerByAddress(t *testing.T) {
	witDir := seedLevelDBStore(t, stores.WitnessStore)
	schedDir := seedLevelDBStore(t, stores.WitnessScheduleStore)

	witnessEng, _ := db.OpenLevelDB(witDir, stores.WitnessStore)
	defer witnessEng.Close()
	scheduleEng, _ := db.OpenLevelDB(schedDir, stores.WitnessScheduleStore)
	defer scheduleEng.Close()

	// Two addresses with equal vote count. Payloads are crafted so the
	// raw bytes differ in the first byte, making the sort order
	// deterministic.
	addrLow, rawLow := makeAddress(t, [20]byte{0x10})
	addrHigh, rawHigh := makeAddress(t, [20]byte{0xf0})

	// Hand the higher-byte address first to defeat input-order
	// ambiguity — if the sort weren't actually comparing bytes, the
	// stable-sort would leave them in input order.
	_, _, err := MutateWitnesses(witnessEng, scheduleEng, []WitnessSpec{
		{Address: addrHigh, VoteCount: 100},
		{Address: addrLow, VoteCount: 100},
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	gotActive, err := scheduleEng.Get([]byte(stores.KeyActiveWitnesses))
	if err != nil {
		t.Fatal(err)
	}
	wantActive := append(append([]byte{}, rawLow...), rawHigh...)
	if !bytes.Equal(gotActive, wantActive) {
		t.Errorf("tiebreak order: got %x; want %x (low-bytes first)",
			gotActive, wantActive)
	}
}

// Sanity check on makeAddress + DecodeAddress round-trip — ensures the
// test harness itself isn't generating addresses dbfork would reject.
// If this fails, the witness tests would be testing nothing useful.
func TestMakeAddress_RoundTrip(t *testing.T) {
	for i := range 8 {
		var p [20]byte
		p[0] = byte(i)
		encoded, body := makeAddress(t, p)
		decoded, err := DecodeAddress(encoded)
		if err != nil {
			t.Fatalf("DecodeAddress(%s): %v", encoded, err)
		}
		if !bytes.Equal(decoded, body) {
			t.Errorf("decoded %x; want %x", decoded, body)
		}
	}
}
