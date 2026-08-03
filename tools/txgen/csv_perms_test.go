package main

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
)

// receivers.csv holds one cleartext secp256k1 private key per row, so
// these tests pin the on-disk permissions of WriteAddressList: 0600 for
// the file, 0700 for any directory it creates, on a fresh path and on a
// pre-existing one.

func sampleRows() []AddressRow {
	return []AddressRow{
		{Base58: "TAAA", HexAddress: "41aa", PrivateKey: "aa" + testPrivKeyHex[2:]},
		{Base58: "TBBB", HexAddress: "41bb", PrivateKey: "bb" + testPrivKeyHex[2:]},
	}
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return st.Mode().Perm()
}

func TestWriteAddressListCreatesFile0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receivers.csv")
	if err := WriteAddressList(path, sampleRows()); err != nil {
		t.Fatalf("WriteAddressList: %v", err)
	}
	if got := modeOf(t, path); got != 0o600 {
		t.Errorf("receivers.csv mode = %#o, want 0600 (file holds private keys)", got)
	}
}

func TestWriteAddressListCreatesDir0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "txgen-output")
	path := filepath.Join(dir, "receivers.csv")
	if err := WriteAddressList(path, sampleRows()); err != nil {
		t.Fatalf("WriteAddressList: %v", err)
	}
	if got := modeOf(t, dir); got != 0o700 {
		t.Errorf("output dir mode = %#o, want 0700", got)
	}
}

func TestWriteAddressListTightensExistingFile0644(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receivers.csv")
	if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := WriteAddressList(path, sampleRows()); err != nil {
		t.Fatalf("WriteAddressList: %v", err)
	}
	// O_CREATE leaves a pre-existing file's mode alone, so the explicit
	// Chmod is what closes this case.
	if got := modeOf(t, path); got != 0o600 {
		t.Errorf("pre-existing 0644 file left at %#o, want 0600", got)
	}
}

func TestWriteAddressListTightensExistingFile0666(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receivers.csv")
	if err := os.WriteFile(path, []byte("stale\n"), 0o666); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatalf("chmod seed file: %v", err)
	}
	if err := WriteAddressList(path, sampleRows()); err != nil {
		t.Fatalf("WriteAddressList: %v", err)
	}
	if got := modeOf(t, path); got != 0o600 {
		t.Errorf("pre-existing world-writable file left at %#o, want 0600", got)
	}
}

func TestWriteAddressListKeepsExistingDirMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "preexisting")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	if err := WriteAddressList(filepath.Join(dir, "receivers.csv"), sampleRows()); err != nil {
		t.Fatalf("WriteAddressList: %v", err)
	}
	// MkdirAll is a no-op on an existing directory: the caller's chosen
	// mode is left as-is rather than silently re-chmod'ed.
	if got := modeOf(t, dir); got != 0o755 {
		t.Errorf("pre-existing dir mode = %#o, want 0755 (unchanged)", got)
	}
}

func TestWriteAddressListContentAtRestrictedMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receivers.csv")
	rows := sampleRows()
	if err := WriteAddressList(path, rows); err != nil {
		t.Fatalf("WriteAddressList: %v", err)
	}
	if got := modeOf(t, path); got != 0o600 {
		t.Fatalf("mode = %#o, want 0600", got)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	if len(recs) != len(rows)+1 {
		t.Fatalf("got %d records, want %d (header + rows)", len(recs), len(rows)+1)
	}
	if recs[0][0] != "base58" || recs[0][1] != "hex_address" || recs[0][2] != "private_key" {
		t.Errorf("header = %v, want [base58 hex_address private_key]", recs[0])
	}
	for i, r := range rows {
		got := recs[i+1]
		if got[0] != r.Base58 || got[1] != r.HexAddress || got[2] != r.PrivateKey {
			t.Errorf("row %d = %v, want %v", i, got, r)
		}
	}
}

func TestWriteAddressListReceiverSidecarEndToEnd(t *testing.T) {
	// The runGenerate path, minus the node round-trips: mint real
	// receivers and dump them exactly as the sidecar write does.
	const n = 3
	addrs, err := buildReceivers(n)
	if err != nil {
		t.Fatalf("buildReceivers: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "txgen-output")
	path := filepath.Join(dir, "receivers.csv")
	if err := WriteAddressList(path, addrs); err != nil {
		t.Fatalf("WriteAddressList: %v", err)
	}
	if got := modeOf(t, dir); got != 0o700 {
		t.Errorf("output dir mode = %#o, want 0700", got)
	}
	if got := modeOf(t, path); got != 0o600 {
		t.Errorf("sidecar mode = %#o, want 0600", got)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	if len(recs) != n+1 {
		t.Fatalf("got %d records, want %d", len(recs), n+1)
	}
	for i := 1; i < len(recs); i++ {
		if len(recs[i][2]) != 64 {
			t.Errorf("row %d private key length = %d, want 64 hex chars", i, len(recs[i][2]))
		}
	}
}
