package dbfork

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"

	"google.golang.org/protobuf/proto"

	"github.com/tronprotocol/tron-deployment/internal/dbfork/db"
	pb "github.com/tronprotocol/tron-deployment/internal/tronproto/pb"
)

// deterministicMarshal sorts map keys (Account.asset_v2 and any other
// future map fields) so proto.Marshal output is reproducible across
// runs and platforms. Shared at package scope so witnesses/properties
// can adopt it too once they touch any map-typed proto fields (today
// neither does — Witness has no maps, properties writes raw bytes).
var deterministicMarshal = proto.MarshalOptions{Deterministic: true}

// AccountSpec is one entry from fork.conf's `accounts:` list. Mirrors
// java DbFork's parsed Config shape (DbFork.java:216-293; key strings
// from utils/Constant.java:11-18):
//
//	{ address = "T..."; accountName = "..."; accountType = "Normal";
//	  balance = 100; owner = "T..."; trc10Id = "1000001";
//	  trc10Balance = 50 }
//
// Per-entry semantics (preserved verbatim from java DbFork):
//   - All fields except Address are optional. Address is rejected at
//     decode time if missing or malformed.
//   - Balance only applied when > 0 (matches java :249).
//   - TRC10 holding only applied when both TRC10ID and TRC10Balance
//     are set, TRC10Balance > 0, and the asset already exists in
//     assetIssueV2Store (java :268-285).
//   - AccountType, AccountName, Owner only applied when present.
//   - Existing accounts are merge-updated: the on-disk proto is read,
//     selectively mutated, and re-marshalled. Unlisted fields survive.
//   - New accounts (not yet in accountStore) are created with only
//     address populated, then selectively mutated.
//   - Each entry holds AT MOST one (TRC10ID, TRC10Balance) pair. To
//     write multiple holdings for one address, list the account
//     multiple times — java DbFork has the same restriction.
type AccountSpec struct {
	// Address is the Base58Check-encoded TRON address. Required.
	Address string `yaml:"address"`

	// AccountName is the optional nickname.
	AccountName string `yaml:"accountName"`

	// AccountType is one of "Normal" / "AssetIssue" / "Contract" —
	// matches the AccountType enum name. Case-sensitive.
	AccountType string `yaml:"accountType"`

	// Balance is the TRX balance in sun (1 TRX = 10^6 sun). Only
	// applied when > 0; zero/negative skipped per java DbFork :249.
	Balance int64 `yaml:"balance"`

	// Owner is the optional Base58Check-encoded owner-permission
	// address. When set, replaces account.owner_permission with a
	// single-key default permission (weight 1, threshold 1).
	Owner string `yaml:"owner"`

	// TRC10ID is the optional asset ID (string-form numeric, e.g.
	// "1000001"). Asset must already exist in assetIssueV2Store —
	// DbFork only updates holdings, it does not issue new tokens.
	TRC10ID string `yaml:"trc10Id"`

	// TRC10Balance is the holding amount in token base units (the
	// scale is per-asset precision, not auto-scaled). Only applied
	// when > 0 AND TRC10ID set AND the asset exists.
	TRC10Balance int64 `yaml:"trc10Balance"`
}

// MutateAccounts applies the fork.conf accounts block:
//
//   - For each spec, merge-updates the AccountStore entry (creating
//     a stub with just Address if it didn't exist).
//   - When the on-disk account has asset_optimized=true, TRC10
//     holdings are written to AccountAssetStore under the composite
//     key `address || []byte(trc10Id)`, value = BigEndian uint64.
//   - When asset_optimized=false, the TRC10 holding is merged into
//     the Account.asset_v2 map (preserving existing entries) and
//     persisted as part of the Account proto.
//   - When a referenced TRC10ID is missing from assetIssueV2Store,
//     the holding is skipped with a log line (matches java :281-284).
//
// All AccountStore writes land in one batch (atomic per store);
// AccountAssetStore writes land in their own batch. Cross-store
// atomicity is not guaranteed, matching java DbFork's per-store
// independent puts.
//
// assetIssueV2Eng is read-only here — DbFork only checks asset
// existence; the issuance store itself is never written.
//
// Returns the number of AccountStore entries written (one per spec
// on the happy path). TRC10 skips do NOT decrement this — the
// account proto is still re-marshalled and put, matching java.
func MutateAccounts(
	accountEng, accountAssetEng, assetIssueV2Eng db.Engine,
	specs []AccountSpec,
) (modified int, err error) {
	accountBatch := accountEng.NewBatch()
	defer accountBatch.Close()
	accountAssetBatch := accountAssetEng.NewBatch()
	defer accountAssetBatch.Close()

	// java DbFork puts each account synchronously inside its loop
	// (DbFork.java:288), so the next iteration that targets the
	// SAME address reads back the mutated proto and continues
	// accumulating. A naive batched port would diverge here: the
	// second spec would read the pre-batch on-disk state and lose
	// the first spec's mutations (only relevant for fields the
	// engine wouldn't know to merge, like the AssetV2 map).
	//
	// We replicate java's semantic by staging account mutations in
	// an in-memory map keyed by address bytes. Each spec's "fetch"
	// step either reuses a pending account from the map (carrying
	// prior-spec mutations forward) or hydrates from the engine the
	// first time we see the address. At the end of the loop we
	// marshal + Put each pending account exactly once.
	//
	// Memory note: pending grows O(distinct addresses in specs).
	// Realistic fork.conf loads are <500 entries (DBFork.md example
	// has 2); a 1M-entry conf would hold ~250MB of *pb.Account
	// pointers and should be chunked at the caller. Not in scope
	// for Phase 1 PoC.
	pending := make(map[string]*pb.Account)

	for _, s := range specs {
		addr, decodeErr := DecodeAddress(s.Address)
		if decodeErr != nil {
			return 0, fmt.Errorf("dbfork: account %q: %w", s.Address, decodeErr)
		}

		account, ok := pending[string(addr)]
		if !ok {
			existing, getErr := accountEng.Get(addr)
			switch {
			case getErr == nil:
				account = &pb.Account{}
				if unmErr := proto.Unmarshal(existing, account); unmErr != nil {
					return 0, fmt.Errorf("dbfork: unmarshal existing account %x: %w",
						addr, unmErr)
				}
			case errors.Is(getErr, db.ErrNotFound):
				// java DbFork :242-246 — bare-address account if no
				// existing entry. All other proto fields stay at
				// zero until explicitly set below.
				account = &pb.Account{Address: addr}
			default:
				return 0, fmt.Errorf("dbfork: get account %x: %w", addr, getErr)
			}
			pending[string(addr)] = account
		}

		if s.Balance > 0 {
			account.Balance = s.Balance
		}
		if s.AccountName != "" {
			// java uses ByteArray.fromString (UTF-8); Go []byte(s) is
			// identical for valid UTF-8 strings.
			account.AccountName = []byte(s.AccountName)
		}
		if s.AccountType != "" {
			t, ok := pb.AccountType_value[s.AccountType]
			if !ok {
				return 0, fmt.Errorf("dbfork: account %s: unknown accountType %q "+
					"(want Normal/AssetIssue/Contract)", s.Address, s.AccountType)
			}
			account.Type = pb.AccountType(t)
		}
		if s.Owner != "" {
			ownerAddr, ownerErr := DecodeAddress(s.Owner)
			if ownerErr != nil {
				return 0, fmt.Errorf("dbfork: account %s owner %q: %w",
					s.Address, s.Owner, ownerErr)
			}
			account.OwnerPermission = defaultOwnerPermission(ownerAddr)
			// java DbFork calls AccountCapsule.updatePermissions(owner,
			// null, null), which unconditionally clears active
			// permissions before re-adding any (chainbase
			// AccountCapsule.java:1311). Passing null for actives means
			// "clear without re-adding" — so any existing
			// active_permission list on the account is dropped. Missing
			// this clear would byte-diverge from java on every account
			// that previously had an active permission.
			account.ActivePermission = nil
		}

		if s.TRC10ID != "" && s.TRC10Balance > 0 {
			if applyTRC10Err := applyTRC10Holding(
				assetIssueV2Eng, accountAssetBatch, account, addr, s,
			); applyTRC10Err != nil {
				return 0, applyTRC10Err
			}
		}

		modified++
	}

	// Flush each pending account exactly once. Iteration order over
	// a Go map is randomised; since each Put goes to a distinct key
	// (the address), goleveldb's batch result is order-independent.
	for addrStr, account := range pending {
		// Deterministic: true sorts map keys (Account.asset_v2) so two
		// equivalent forks produce identical bytes. java's
		// `toByteArray()` is also non-deterministic by default, but the
		// equivalence test (Task #152) round-trips both sides through
		// canonical Marshal before diffing — we set Deterministic here
		// so the Go side is canonical on first emission.
		marshaled, mErr := deterministicMarshal.Marshal(account)
		if mErr != nil {
			return 0, fmt.Errorf("dbfork: marshal account %x: %w",
				[]byte(addrStr), mErr)
		}
		accountBatch.Put([]byte(addrStr), marshaled)
	}

	if writeErr := accountBatch.Write(); writeErr != nil {
		return 0, fmt.Errorf("dbfork: write account batch: %w", writeErr)
	}
	if writeErr := accountAssetBatch.Write(); writeErr != nil {
		return 0, fmt.Errorf("dbfork: write account-asset batch: %w", writeErr)
	}
	return modified, nil
}

// applyTRC10Holding routes the TRC10 update to the right store based
// on the account's asset_optimized flag (java DbFork :272-279). Split
// out to keep MutateAccounts readable.
//
// account is the pointer held in MutateAccounts's pending map; the
// AssetV2-merge mutation here propagates to that map automatically (no
// re-store needed).
func applyTRC10Holding(
	assetIssueV2Eng db.Engine,
	accountAssetBatch db.Batch,
	account *pb.Account,
	addr []byte,
	s AccountSpec,
) error {
	// Existence check on the asset. java DbFork :271 uses
	// `assetIssueV2Store.get(...) != null` — leveldb's Get returns nil
	// for absent keys in Java; Go's wrapper returns ErrNotFound.
	if _, getErr := assetIssueV2Eng.Get([]byte(s.TRC10ID)); getErr != nil {
		if errors.Is(getErr, db.ErrNotFound) {
			// Matches java :282-284: log + skip, don't error out the
			// whole apply. This is opt-in: an operator who lists a
			// non-existent TRC10 expects to be told, not blocked.
			log.Printf("dbfork: TRC10 %q not in assetIssueV2Store; "+
				"skipping holding for account %s", s.TRC10ID, s.Address)
			return nil
		}
		return fmt.Errorf("dbfork: probe asset %q: %w", s.TRC10ID, getErr)
	}

	if account.AssetOptimized {
		// Composite key = 21-byte address || ASCII bytes of the
		// trc10Id. java uses Bytes.concat(address, ByteArray.
		// fromString(trc10Id)); ByteArray.fromString is UTF-8.
		key := make([]byte, 0, len(addr)+len(s.TRC10ID))
		key = append(key, addr...)
		key = append(key, []byte(s.TRC10ID)...)
		val := make([]byte, 8)
		// Longs.toByteArray is big-endian (same encoding as the
		// dynamic-properties writes in properties.go).
		binary.BigEndian.PutUint64(val, uint64(s.TRC10Balance))
		accountAssetBatch.Put(key, val)
		return nil
	}

	// Non-optimized: merge the holding into Account.asset_v2. Java's
	// pattern is clearAssetV2() + addAssetMapV2(map); the net effect
	// is "replace the entry for this trc10Id, preserve all others".
	if account.AssetV2 == nil {
		account.AssetV2 = map[string]int64{}
	}
	account.AssetV2[s.TRC10ID] = s.TRC10Balance
	return nil
}

// defaultOwnerPermission mirrors AccountCapsule.
// createDefaultOwnerPermission (java-tron chainbase, line 194-208):
// a single-key permission of type Owner with the given address,
// weight 1, id 0, name "owner", parentId 0, threshold 1. Operations
// is left unset (zero-length bytes) to match Java's builder default.
func defaultOwnerPermission(addr []byte) *pb.Permission {
	return &pb.Permission{
		Type:           pb.Permission_Owner,
		Id:             0,
		PermissionName: "owner",
		Threshold:      1,
		ParentId:       0,
		Keys:           []*pb.Key{{Address: addr, Weight: 1}},
	}
}
