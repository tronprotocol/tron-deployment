package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AddressRow is one (base58, hex, private-key) triple for the receivers
// sidecar.
type AddressRow struct {
	Base58     string
	HexAddress string
	PrivateKey string
}

// WriteAddressList writes rows to path in CSV form with a header.
//
// Every row carries a receiver's cleartext secp256k1 private key, so the
// file is created 0600 inside a 0700 directory — never the 0666&umask
// that os.Create would give it. The explicit Chmod covers the case where
// path already exists (O_CREATE leaves a pre-existing file's mode alone)
// and runs before the first row is written, so the keys are never
// readable by another local uid, not even for an instant.
func WriteAddressList(path string, rows []AddressRow) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("restrict %s to 0600: %w", path, err)
	}
	w := csv.NewWriter(f)
	if err := w.Write([]string{"base58", "hex_address", "private_key"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := w.Write([]string{r.Base58, r.HexAddress, r.PrivateKey}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// ListGeneratedTxFiles returns every generate-tx*.csv in dir, sorted.
func ListGeneratedTxFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "generate-tx") && strings.HasSuffix(name, ".csv") {
			files = append(files, filepath.Join(dir, name))
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no generate-tx*.csv files in %s", dir)
	}
	return files, nil
}
