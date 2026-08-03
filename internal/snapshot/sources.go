// Package snapshot fetches java-tron chain database snapshots from the
// upstream public mirrors and pipes them straight into a target directory
// (no on-disk .tgz copy in between).
//
// The source list mirrors tron-docker/tools/trond/config/node.go; last
// audited 2026-05-25 (Nile lite + Nile rocksdb HEAD-checked). When
// endpoints rotate, update SourceTable here — there is no canonical
// upstream "list-of-mirrors" endpoint, so a stale hard-coded table
// beats a fragile bootstrap call. The fragility of *this* table is
// instead addressed by the scheduled `--probe` check (#161 follow-up).
package snapshot

// Network distinguishes which TRON network a snapshot belongs to. Private
// networks don't have a public snapshot service; they sync from genesis.
type Network string

const (
	NetworkMainnet Network = "mainnet"
	NetworkNile    Network = "nile"
)

// DBKind narrows the database flavor of a snapshot. "lite" snapshots ship
// only recent blocks (~tens of GB) and are what most users want. "full"
// includes the entire chain history (~2 TB on mainnet) and is typically
// only useful when running an archive node or rebuilding from scratch.
type DBKind string

const (
	DBKindLite DBKind = "lite"
	DBKindFull DBKind = "full"
)

// DBEngine differentiates LevelDB vs RocksDB encodings of the same data.
// java-tron defaults to LevelDB; only one mainnet mirror publishes RocksDB.
type DBEngine string

const (
	EngineLevelDB DBEngine = "leveldb"
	EngineRocksDB DBEngine = "rocksdb"
)

// Region is purely informational — used so users in Asia / Americas can
// pick a closer mirror to reduce download time. Not enforced anywhere.
type Region string

const (
	RegionSingapore Region = "singapore"
	RegionAmerica   Region = "america"
)

// Source describes one snapshot mirror.
//
// The `BaseURL` is what trond actually downloads from; it's a fully-formed
// URL prefix because nile uses an S3 https endpoint while the mainnet
// mirrors are plain `http://<ip>` directory listings. The struct is the
// minimum trond needs to (a) render a `sources` table and (b) construct
// per-backup tarball URLs in download.go.
// JSON tags mirror schemas/output/snapshot-sources.schema.json so a
// `trond snapshot sources -o json` round-trips through the published
// schema. Field names that differ from the schema (DBKind→kind,
// DBEngine→engine, ApproxSizeGB→approx_size_gb) keep their Go names
// for clarity in source.
type Source struct {
	Network        Network  `json:"network"`
	DBKind         DBKind   `json:"kind"`
	DBEngine       DBEngine `json:"engine"`
	Region         Region   `json:"region,omitempty"`
	Domain         string   `json:"domain"`                   // primary key for --domain flag matching
	BaseURL        string   `json:"base_url"`                 // "http(s)://host[/prefix]" — no trailing slash
	IndexStrategy  string   `json:"index_strategy,omitempty"` // "html" or "date" — how ListBackups builds the list
	IncludesIntTx  bool     `json:"includes_int_tx,omitempty"`
	IncludesAcctTx bool     `json:"includes_acct_tx,omitempty"`
	ApproxSizeGB   int      `json:"approx_size_gb,omitempty"`
	Description    string   `json:"description,omitempty"`
}

// SourceTable is the curated list of upstream mirrors. Order matters
// only insofar as users expect a stable column layout in the table view.
//
// IMPORTANT: when refreshing this list, prefer adding new entries over
// rewriting existing ones — automation may key off `Domain`. Mark old
// entries with a Description hint when they're being decommissioned.
//
// Transport, probed 2026-07-31 (`curl` to :443 and :80 on every entry):
//
//   - snapshots.nileex.io serves HTTPS (HTTP/2 200 on a real sidecar path)
//     and 301-redirects :80 → :443. Both nile rows already use https://
//     and must stay that way.
//   - All six mainnet rows are bare IPs with port 443 closed — the TLS
//     connect times out while :80 answers 200 from the same host. A bare
//     IP could not present a CA-issued certificate anyway, so there is no
//     HTTPS to prefer and no upgrade to attempt: rewriting these to https://
//     would break every mainnet download, and an opportunistic "try HTTPS
//     first" would buy nothing but a connect timeout per transfer.
//
// So trond does not silently upgrade these. It makes the cleartext hop
// visible instead: PreflightResult/DownloadResult carry
// `plaintext_transport`, and Download emits PlaintextWarning through
// DownloadOptions.WarnFn before any bytes move. Re-probe when this table
// is refreshed; if a mirror gains HTTPS, switch its BaseURL here and the
// warning stops firing on its own.
var SourceTable = []Source{
	{
		Network:       NetworkMainnet,
		DBKind:        DBKindLite,
		DBEngine:      EngineLevelDB,
		Region:        RegionSingapore,
		Domain:        "34.143.247.77",
		BaseURL:       "http://34.143.247.77",
		IndexStrategy: "html",
		ApproxSizeGB:  46,
		Description:   "Lite fullnode (~46 GB) — fastest start for typical fullnode use",
	},
	{
		Network:       NetworkMainnet,
		DBKind:        DBKindFull,
		DBEngine:      EngineLevelDB,
		Region:        RegionSingapore,
		Domain:        "34.143.247.77",
		BaseURL:       "http://34.143.247.77",
		IndexStrategy: "html",
		ApproxSizeGB:  2093,
		Description:   "Full archive, no internal transactions",
	},
	{
		Network:       NetworkMainnet,
		DBKind:        DBKindFull,
		DBEngine:      EngineLevelDB,
		Region:        RegionSingapore,
		Domain:        "35.247.128.170",
		BaseURL:       "http://35.247.128.170",
		IndexStrategy: "html",
		IncludesIntTx: true,
		ApproxSizeGB:  2278,
		Description:   "Full archive WITH internal transactions",
	},
	{
		Network:       NetworkMainnet,
		DBKind:        DBKindFull,
		DBEngine:      EngineLevelDB,
		Region:        RegionAmerica,
		Domain:        "34.86.86.229",
		BaseURL:       "http://34.86.86.229",
		IndexStrategy: "html",
		ApproxSizeGB:  2094,
		Description:   "Full archive, no internal transactions",
	},
	{
		Network:        NetworkMainnet,
		DBKind:         DBKindFull,
		DBEngine:       EngineLevelDB,
		Region:         RegionAmerica,
		Domain:         "34.48.6.163",
		BaseURL:        "http://34.48.6.163",
		IndexStrategy:  "html",
		IncludesAcctTx: true,
		ApproxSizeGB:   2627,
		Description:    "Full archive with account history TRX balance",
	},
	{
		Network:       NetworkMainnet,
		DBKind:        DBKindFull,
		DBEngine:      EngineRocksDB,
		Region:        RegionAmerica,
		Domain:        "35.197.17.205",
		BaseURL:       "http://35.197.17.205",
		IndexStrategy: "html",
		ApproxSizeGB:  2067,
		Description:   "Full archive — RocksDB encoding (atypical)",
	},
	{
		Network:       NetworkNile,
		DBKind:        DBKindLite,
		DBEngine:      EngineLevelDB,
		Region:        RegionSingapore,
		Domain:        "snapshots.nileex.io",
		BaseURL:       "https://snapshots.nileex.io",
		IndexStrategy: "date",
		ApproxSizeGB:  30,
		Description:   "Nile testnet fullnode/lite-fullnode (~30 GB, LevelDB)",
	},
	{
		// Nile RocksDB-encoded full snapshot. No lite variant published.
		// Required for arm64 hosts (java-tron's Storage.java:180 forces
		// RocksDB on arm64 regardless of config), and for any operator
		// running with storage.db.engine = ROCKSDB. The /rocksdb prefix
		// is baked into BaseURL so download.go's BaseURL+"/"+backup+"/"+
		// tarball composition keeps the same shape as every other row.
		Network:       NetworkNile,
		DBKind:        DBKindFull,
		DBEngine:      EngineRocksDB,
		Region:        RegionSingapore,
		Domain:        "snapshots.nileex.io",
		BaseURL:       "https://snapshots.nileex.io/rocksdb",
		IndexStrategy: "date",
		ApproxSizeGB:  90,
		Description:   "Nile testnet fullnode (~90 GB, RocksDB — required on arm64)",
	},
}

// Filter returns sources matching the given criteria. Empty values match
// all (so passing only Network=mainnet returns every mainnet entry).
type Filter struct {
	Network  Network
	DBKind   DBKind
	DBEngine DBEngine
	Region   Region
	Domain   string
}

// Match returns the sources matching f. Empty fields in f act as wildcards
// — passing a zero-value Filter returns every entry.
func Match(f Filter) []Source {
	out := make([]Source, 0, len(SourceTable))
	for _, s := range SourceTable {
		if f.Network != "" && s.Network != f.Network {
			continue
		}
		if f.DBKind != "" && s.DBKind != f.DBKind {
			continue
		}
		if f.DBEngine != "" && s.DBEngine != f.DBEngine {
			continue
		}
		if f.Region != "" && s.Region != f.Region {
			continue
		}
		if f.Domain != "" && s.Domain != f.Domain {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Pick returns the first source matching f, or nil if none match. Used by
// the download command to resolve --network/--type/--region into a single
// concrete mirror without forcing the user to know the IP.
func Pick(f Filter) *Source {
	matches := Match(f)
	if len(matches) == 0 {
		return nil
	}
	return &matches[0]
}

// LookupDomain returns the source by exact domain match, or nil. Used by
// `--domain` overrides where the user supplies a specific mirror.
func LookupDomain(domain string) *Source {
	for i := range SourceTable {
		if SourceTable[i].Domain == domain {
			return &SourceTable[i]
		}
	}
	return nil
}
