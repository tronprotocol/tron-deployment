// Package dbfork is the Go port of java-tron's DbFork toolkit
// utility, embedded into trond's `shadow-fork mutate` subcommand.
//
// Scope: open a halted java-tron data dir (`output-directory/`) and
// apply the state mutations described in a fork.conf — replace
// witnesses, fund accounts, set TRC10/TRC20 balances, adjust
// timestamps + maintenance window — so the data dir is ready for a
// shadow-fork private network to launch against.
//
// Architectural seams:
//
//	internal/dbfork/stores   on-disk store names + fixed byte keys
//	internal/dbfork/db       LevelDB / RocksDB engine abstraction
//	internal/tronproto       protobuf bindings (generated from
//	                         tronprotocol/protocol)
//	(this package)           public Apply entry + per-section
//	                         mutation files (witnesses.go,
//	                         accounts.go, trc20.go, …)
//
// Equivalence with java DbFork is the release gate — see
// equivalence_test.go (Task #152). Until then, integration tests
// run against synthetic fixtures; Phase 1 PoC ships LevelDB only.
package dbfork

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tronprotocol/tron-deployment/internal/dbfork/db"
	"github.com/tronprotocol/tron-deployment/internal/dbfork/stores"
)

// Config is the parsed fork.conf — driver-agnostic of YAML vs HOCON
// (Task #150 wires both into this single shape). Each section is a
// slice so the loader can build the same shape from either format.
//
// Each apply step (witnesses, accounts, trc20, properties) reads
// from its corresponding field and is a no-op when the field is
// empty / zero. fork.conf is a partial spec: operators can fork
// only witnesses without touching accounts, for example.
type Config struct {
	// Task #147 — witness set replacement (with active set).
	Witnesses []WitnessSpec

	// Task #147 — DynamicProperties timing knobs.
	Properties PropertiesSpec

	// Task #148 — account balance/name/type/owner + per-account
	// TRC10 holding update. Per-entry semantics match java DbFork:
	// merge into existing account, create stub if absent.
	Accounts []AccountSpec

	// Task #149 — TRC20 holding updates. Per-entry writes one
	// EVM storage row (32-byte BE balance) under the keccak-derived
	// slot key for the contract's `balances[account]` mapping.
	TRC20Contracts []TRC20Spec
}

// Options configures the Apply call. RetainWitnesses mirrors java
// DbFork's --retain-witnesses flag (keep existing witnesses + active
// set in addition to whatever fork.conf adds).
type Options struct {
	RetainWitnesses bool
	// DryRun: if true, walk the mutation plan and report what
	// WOULD change but don't write. The CLI surfaces this for
	// operators sizing a fork before committing.
	DryRun bool
}

// Result summarises what Apply actually did. The CLI surfaces this
// as JSON so MCP agents can chain build/apply/inspect.
type Result struct {
	// WitnessesWritten is the count of Witness protos written to
	// witnessStore. Equals len(cfg.Witnesses) on a successful run.
	WitnessesWritten int `json:"witnesses_written"`

	// ActiveWitnessesSet is the count of addresses in the new
	// witnessScheduleStore active-witness slate. Capped at 27
	// (MaxActiveWitnessNum) per TRON's scheduling rules.
	ActiveWitnessesSet int `json:"active_witnesses_set"`

	// PropertiesUpdated counts the DynamicPropertiesStore keys
	// modified (0..3 depending on which PropertiesSpec fields
	// were non-zero in fork.conf).
	PropertiesUpdated int `json:"properties_updated"`

	// AccountsModified counts fork.conf account specs processed.
	// Equals len(cfg.Accounts) on the happy path; matches java
	// DbFork's stdout (`{n} accounts have been modified`,
	// DbFork.java:290-291). When the same address appears in
	// multiple specs (e.g. one per TRC10 holding), the count is
	// the spec count, not the distinct-address count.
	AccountsModified int `json:"accounts_modified"`

	// TRC20SlotsUpdated counts storage-row slots written.
	// Mirrors java DbFork's `{n} TRC20 contracts have been modified`
	// (DbFork.java:367-369). Specs that target a missing contract are
	// silently skipped and do NOT increment this counter, matching
	// java's per-iter cnt.getAndIncrement() inside the success path.
	TRC20SlotsUpdated int `json:"trc20_slots_updated"`
}

// Apply opens the data dir, mutates the relevant subset of the 8
// java-tron stores per cfg, and returns a Result. The engine handles
// + batches are managed internally; callers don't see Engine / Batch
// types from the db subpackage.
//
// Phase 1 PoC scope: witnesses + DynamicProperties (Task #147).
// Accounts / TRC10 / TRC20 land in subsequent tasks and gain
// counters on Result as they ship.
//
// DryRun (Task #150): walks the mutation plan and counts what
// WOULD change without writing. Not yet implemented; Apply errors
// when opts.DryRun is true.
// The return is named so the deferred Engine.Close() calls can
// surface a sweep failure. levelDBEngine.Close() runs the #164
// .ldb->.sst rename + .bak/.old cleanup, which is the most
// failure-prone step (os.Rename/os.Remove against ENOSPC, EACCES,
// EROFS, a transient I/O error, or a host indexer holding a file
// open). If a close fails AFTER the mutation batch already committed,
// the store is left with .ldb table files java-tron's leveldbjni
// cannot read — exactly the corruption #164 exists to prevent. We
// MUST turn that into a hard error rather than report success, so
// each defer promotes the first close error into the return when no
// mutation error already occurred.
func Apply(dataDir string, cfg *Config, opts Options) (res *Result, err error) {
	if cfg == nil {
		return nil, errors.New("dbfork: nil config")
	}
	if opts.DryRun {
		return nil, errors.New("dbfork: --dry-run not yet implemented (Task #150)")
	}

	// Validate that <dataDir>/database/ exists before any branch
	// gating decides whether to open a store. Without this check, an
	// otherwise-empty fork.conf (only Properties.MaintenanceTimeInterval
	// set, say, or fully empty) would silently report "0 modifications"
	// against a bogus data dir — the gating logic would skip every
	// section and return a zero Result with no I/O. The misleading
	// success is the operator trap pass-2 review caught at the cmd
	// layer; fixing it here means every caller (CLI, MCP, future
	// programmatic use) is protected uniformly.
	dbRoot := filepath.Join(dataDir, "database")
	if _, err := os.Stat(dbRoot); err != nil {
		return nil, fmt.Errorf("dbfork: data directory %s missing database/ subdir: %w",
			dataDir, err)
	}

	res = &Result{}

	// closeStore promotes a close (incl. the #164 sweep) failure into
	// the named err return — but only when no earlier mutation error
	// already set it, so the original cause wins. A bare
	// `defer eng.Close()` would silently discard a sweep failure and
	// leave Apply reporting success on an unbootable store.
	closeStore := func(eng db.Engine) {
		if cerr := eng.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}

	// openStore probes the on-disk engine + opens one store. Each
	// java-tron store is a separate LevelDB instance under
	// <dataDir>/database/<name>, so we sniff + open per-store. In
	// practice all 8 stores share the same engine per java-tron's
	// global storage.db.engine config, but we don't ASSUME that.
	openStore := func(name string) (db.Engine, error) {
		kind, kindErr := db.DetectKind(dataDir, name)
		if kindErr != nil {
			return nil, fmt.Errorf("dbfork: detect kind for %s: %w", name, kindErr)
		}
		eng, openErr := db.Open(dataDir, name, kind)
		if openErr != nil {
			return nil, fmt.Errorf("dbfork: open %s: %w", name, openErr)
		}
		return eng, nil
	}

	// Enter the witness branch ONLY when fork.conf actually lists
	// witnesses. A properties-only or empty-witness Config is a true
	// no-op on the witness stores — gating on `len > 0` here makes
	// the zero-value `Options{}` safe (otherwise RetainWitnesses=false
	// would silently wipe the witness store on a properties-only
	// call). RetainWitnesses then controls only the wipe-before-add
	// behaviour when witnesses ARE listed.
	//
	// Note: MutateWitnesses still supports the "wipe with no
	// replacement" path for direct callers, but Apply does not
	// expose it — wiping witnesses without replacement leaves the
	// shadow chain unable to produce blocks, so it's never the right
	// behaviour from a fork.conf.
	if len(cfg.Witnesses) > 0 {
		witnessEng, err := openStore(stores.WitnessStore)
		if err != nil {
			return nil, err
		}
		defer closeStore(witnessEng)
		scheduleEng, err := openStore(stores.WitnessScheduleStore)
		if err != nil {
			return nil, err
		}
		defer closeStore(scheduleEng)
		written, active, err := MutateWitnesses(witnessEng, scheduleEng,
			cfg.Witnesses, opts.RetainWitnesses)
		if err != nil {
			return nil, err
		}
		res.WitnessesWritten = written
		res.ActiveWitnessesSet = active
	}

	// Accounts — only enter the branch when fork.conf actually lists
	// accounts. Same principle-of-least-surprise gating as witnesses:
	// a properties-only or witness-only conf doesn't open the
	// account/asset stores at all.
	if len(cfg.Accounts) > 0 {
		accountEng, err := openStore(stores.AccountStore)
		if err != nil {
			return nil, err
		}
		defer closeStore(accountEng)
		accountAssetEng, err := openStore(stores.AccountAssetStore)
		if err != nil {
			return nil, err
		}
		defer closeStore(accountAssetEng)
		assetIssueV2Eng, err := openStore(stores.AssetIssueV2Store)
		if err != nil {
			return nil, err
		}
		defer closeStore(assetIssueV2Eng)
		modified, err := MutateAccounts(accountEng, accountAssetEng,
			assetIssueV2Eng, cfg.Accounts)
		if err != nil {
			return nil, err
		}
		res.AccountsModified = modified
	}

	// TRC20 contracts — only enter when fork.conf lists any. Two
	// stores: contractStore (read-only, for SmartContract.version +
	// trx_hash) and storageRowStore (one write per spec).
	if len(cfg.TRC20Contracts) > 0 {
		contractEng, err := openStore(stores.ContractStore)
		if err != nil {
			return nil, err
		}
		defer closeStore(contractEng)
		storageRowEng, err := openStore(stores.StorageRowStore)
		if err != nil {
			return nil, err
		}
		defer closeStore(storageRowEng)
		updated, err := MutateTRC20Contracts(contractEng, storageRowEng,
			cfg.TRC20Contracts)
		if err != nil {
			return nil, err
		}
		res.TRC20SlotsUpdated = updated
	}

	// DynamicProperties — only touch if at least one field is > 0.
	// Matches both java DbFork's per-field `> 0` gate and
	// MutateProperties' write gate, so the open-gate and the
	// write-gate agree (a properties block of only negative/zero
	// values is a true no-op and never opens the store).
	if cfg.Properties.LatestBlockHeaderTimestamp > 0 ||
		cfg.Properties.MaintenanceTimeInterval > 0 ||
		cfg.Properties.NextMaintenanceTime > 0 {
		propsEng, err := openStore(stores.DynamicPropertiesStore)
		if err != nil {
			return nil, err
		}
		defer closeStore(propsEng)
		written, err := MutateProperties(propsEng, cfg.Properties)
		if err != nil {
			return nil, err
		}
		res.PropertiesUpdated = written
	}

	return res, nil
}
