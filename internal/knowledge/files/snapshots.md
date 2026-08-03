# Chain Database Snapshots

A fresh TRON fullnode normally syncs from the genesis block. On mainnet
that takes days and many GB of P2P traffic. The official mirrors publish
periodic dumps of the chain database; trond fetches and extracts them so
a node can start *caught-up*.

## When to use a snapshot

- First-time deployment of a new fullnode.
- Re-provisioning after data corruption or disk loss.
- Standing up a CI fixture that needs a "live-ish" chain state without
  burning compute on a multi-day sync.

A snapshot is **not** what you want when:
- You're running a witness with already-mined blocks (snapshot doesn't
  ship `userdata/`; trond preserves it across extraction, but the rest
  of the database will overwrite anything custom under `database/`).
- You're on a private network — snapshots only exist for mainnet and
  Nile.

## Streaming pipeline

`snapshot download` does *not* persist the upstream `.tgz`. The HTTP body
flows through one io.Reader chain:

```
HTTP body
  → TeeReader → md5.Hash       (verify integrity inline)
  → progress wrapper           (10 Hz UI updates, no buffering)
  → gzip.Reader                (streaming decompress)
  → tar.Reader                 (entry-by-entry)
  → os.OpenFile / io.Copy      (each tar entry written directly to disk)
```

So peak disk usage is roughly *the extracted size* — you don't need 2×
to hold the tarball as a temporary staging file. Wall-clock time is
roughly the network transfer time (CPU is far cheaper than the wire).

## Lite vs. full

| Kind | Size (approx) | Use case |
|---|---|---|
| `lite` | ~50 GB mainnet, ~30 GB nile | Default. Holds recent blocks; fine for fullnode operation |
| `full` | ~2 TB mainnet | Archive node, indexer, anything that needs full history |

LevelDB vs. RocksDB: java-tron defaults to LevelDB. One mainnet mirror
publishes a RocksDB encoding (`--db-engine rocksdb`); only choose it if
you've explicitly configured `storage.db.engine=ROCKSDB` via
`config_overrides`.

## Picking a mirror

```bash
trond snapshot sources              # the full table
```

Defaults are deliberate:

- `--network mainnet` (no other flags): Singapore lite mirror — fastest
  setup for the common case.
- `--network nile`: the only nile mirror (S3 https endpoint).

Override with `--region america`, `--type full`, or pin a specific host
via `--domain 35.247.128.170`.

## Disk-space pre-check

Before any tarball bytes hit the wire, trond:

1. Sends an HTTP HEAD to the tarball URL → reads `Content-Length`. The
   body is never opened on this probe.
2. Issues a separate HEAD to the `.md5sum` sidecar → records whether
   inline verification will be possible.
3. `Statfs(destination)` → reads available bytes (Bavail × Bsize, the
   same number `df` shows).
4. Refuses to start the GET if free space < `Content-Length × 2`.

The 2× headroom covers concurrent extraction (when the new database is
landing while the old one hasn't been removed yet) and the slop java-tron
adds when it first opens the new DB.

## Existing-database handling

Two adjacent directories under the destination get special treatment:

| Path | Behaviour |
|---|---|
| `output-directory/database/` | If non-empty, refuse without `--force` (HUMAN_REQUIRED, exit 10). With `--force`, files are overwritten in place. |
| `userdata/` | Always preserved. Holds witness keys / operator state and is **not** part of the snapshot tarball. |

Pre-existing symlinks at any target path are refused, never followed.
Any tar entry containing `..` is rejected before `open()`.

## Transport: the mainnet mirrors are cleartext

Probed 2026-07-31, and worth knowing before you trust a snapshot:

| Mirror | Transport |
|---|---|
| `snapshots.nileex.io` (both nile rows) | **HTTPS.** Port 443 serves HTTP/2; `:80` 301-redirects to it. |
| The six mainnet mirrors (`34.143.247.77`, `35.247.128.170`, `34.86.86.229`, `34.48.6.163`, `35.197.17.205`) | **Cleartext HTTP only.** Port 443 is closed on every one; `:80` answers. They are bare IPs, so they could not present a CA-issued certificate anyway. |

trond does not pretend otherwise. It will not rewrite those URLs to
`https://` (every mainnet download would break) and it does not try HTTPS
opportunistically (that buys a connect timeout per transfer and nothing
else). Instead it makes the cleartext hop visible:

- A warning on **stderr** before any bytes move — including under
  `--detach`, where the child's stderr is the job log.
- `"plaintext_transport": true` in `-o json`, on both the completion
  payload and the `--dry-run` `preflight` object, so an agent can branch
  on it.
- A `transport:` row in the human `--dry-run` table.

If a mirror ever gains HTTPS, switch its `BaseURL` in
`internal/snapshot/sources.go` and the warning stops on its own.

## What the MD5 sidecar does and does not buy

Mainnet mirrors publish `<tarball>.tgz.md5sum` sidecars. trond:

1. HEADs the sidecar in preflight (records "has md5 sidecar: true/false").
2. Downloads the sidecar (a few hundred bytes).
3. Hashes the tarball stream while extracting.
4. Compares — mismatch fails the operation with the database in whatever
   partial state extraction reached.

**Read this before treating `md5 ✓` as a security control.** The sidecar
comes down the same cleartext connection as the tarball. Anyone in a
position to substitute the archive — hostile Wi-Fi, an upstream ISP or
transparent proxy, a BGP hijack of the mirror's IP — is equally able to
substitute the sidecar, and the two will agree. MD5 is also collision-prone
on its own merits. So the sidecar check proves the transfer was not
*corrupted*; it proves nothing about where the bytes came from.

Nile and the occasional outage may leave the sidecar absent. trond will
still extract; the result message reads `(md5 sidecar absent — not
verified)`. Pass `--no-verify` to suppress that note when you've made
the choice deliberately.

## `--sha256`: the check that can detect substitution

A digest is only worth as much as the channel you got it from. trond
computes the SHA-256 of every download and reports it (`sha256:` in the
text output, `"sha256"` in JSON) whether or not you asked for one.

```bash
# First fetch: record the digest trond reports.
trond snapshot download --network mainnet --to /srv/chain -o json | jq -r .sha256

# Every later fetch of that same backup: pin it.
trond snapshot download --network mainnet --backup backup20250115 \
  --to /srv/chain2 --sha256 <hex>
```

A mismatching pin fails the download (`sha256 mismatch: expected ..., got
...`); a malformed pin is rejected up front rather than after a multi-hour
transfer. `"sha256_verified": true` appears in the JSON only when a pin was
supplied **and** matched — computing a digest is not verifying it.

The pin is worth exactly as much as the path you obtained it over. A digest
you read off the same cleartext mirror is worth nothing; one you got from a
trusted operator, a previous verified fetch, or a peer who independently
downloaded the same backup is worth a great deal. The MCP
`snapshot_download` tool takes the same value as its `sha256` argument.

`--no-verify` skips only the MD5 sidecar. It does **not** disable a
`--sha256` pin: if you asked for that check explicitly, you get it.

Neither `--sha256` nor the MD5 sidecar changes *when* verification happens:
the stream is extracted as it arrives, so a failed check leaves partially
extracted data behind. Re-run with `--force` to replace it.

## Long downloads: `--detach`

Mainnet full snapshots can take many hours over a residential link. The
foreground form ties the download to the controlling terminal — closing
the SSH session or laptop lid sends SIGHUP and the work is lost.

`--detach` re-execs trond with `SysProcAttr.Setsid=true`. The child:

- Becomes a session leader (immune to SIGHUP from the parent's terminal).
- Has its stdin tied to `/dev/null`, stdout+stderr to
  `~/.trond/snapshots/<id>.log`.
- Is reparented to PID 1 (launchd / init) once the parent calls
  `Process.Release()`.

The parent prints the job id and exits. Manage the job:

```bash
trond snapshot jobs                       # ID, PID, running/stopped, last log line
trond snapshot logs <job-id> -f           # follow progress
trond snapshot stop <job-id>              # SIGTERM
trond snapshot stop <job-id> --force      # SIGKILL (last resort)
```

The job manifest lives at `~/.trond/snapshots/<id>.json`. Liveness uses
`kill(0)` so finished jobs persist as `state=stopped` with the last log
line as `exit_note`. Delete a finished job's files manually if you don't
want it shown in `jobs` output.

## Putting a snapshot under a managed node

trond intentionally **does not** integrate snapshot download into
`trond apply`. apply is supposed to be fast and idempotent (seconds);
a multi-hour download inside apply would break that contract for every
caller, including CI. The two stages stay decoupled:

```text
[snapshot download --detach]   ──hours──>   tarball extracted to host path
                                                   │
                                                   ▼
                                  [apply --intent <intent.yaml>]
                                  reads storage.data, bind-mounts the
                                  extracted directory into the container,
                                  starts the node already-caught-up.
```

### Path layout (docker runtime)

The upstream tarball expands as `<dest>/output-directory/database/…`.
java-tron expects to find the database at
`/java-tron/output-directory/database` inside the container, so the
intent's `storage.data` must point at the `output-directory` directory
on the host:

```yaml
storage:
  data: /srv/tron/my-fullnode/output-directory
  logs: /srv/tron/my-fullnode/logs
```

Then:

```bash
trond snapshot download --network mainnet --to /srv/tron/my-fullnode --detach
# wait for snapshot jobs to show state=stopped
trond apply --intent <intent.yaml> --auto-approve --wait
```

`storage.path: /srv/tron/my-fullnode` *also* works but mounts
`<path>/data` (a synthetic name trond made up) which doesn't line up
with the tarball's top-level `output-directory/`. If you want the
single-root convenience of `storage.path`, you'd need to first move
`<path>/output-directory` to `<path>/data` after extraction — usually
not worth the trouble; just use `storage.data` directly.

Annotated example: [`examples/mainnet-fullnode-snapshot.yaml`](https://github.com/tronprotocol/tron-deployment/blob/master/examples/mainnet-fullnode-snapshot.yaml).

### Path layout (jar runtime)

For jar-runtime nodes `trond snapshot download --node <name>` resolves
the destination from `install_path` in state automatically — the
container/host distinction goes away.

### What if I want apply to wait?

We deliberately did not add `--snapshot` or `--wait-for-snapshot` flags
to apply. If your CI really needs a single command, wrap them in shell:

```bash
trond snapshot download --network mainnet --to /srv/tron/my-fullnode --detach -o json \
  | jq -r .job_id > /tmp/snap.id
# poll until done; jobs returns rows so a tiny shell loop works:
until [[ "$(trond snapshot jobs -o json | jq -r ".jobs[] | select(.id==\"$(cat /tmp/snap.id)\") | .running")" == "false" ]]; do
  sleep 60
done
trond apply --intent <intent.yaml> --auto-approve --wait
```

Most production users want to run these steps days apart anyway —
snapshot is a one-time per-node cost; apply runs on every config or
version change.

## Common errors

`HUMAN_REQUIRED: existing database at <path>; pass --force to overwrite`
  - Means the destination already has data. Either you want to keep it
    (delete the snapshot intent) or `--force`.

`DISK_SPACE_ERROR: need ~X GB free in <path>, have Y GB`
  - Free space, then retry. There's no `--ignore-space` escape hatch by
    design; running out mid-extract leaves a broken half-database.

`md5 mismatch: expected ..., got ...`
  - The tarball got corrupted in flight or the mirror is serving a
    different file than its sidecar advertises. Retry; if the mismatch
    persists, switch `--region` or `--domain`.

`sha256 mismatch: expected ..., got ...`
  - The archive is not the one your pinned digest describes. Unlike an md5
    mismatch this is not routine corruption — on a cleartext mirror it is
    exactly what a substituted archive looks like. Do not `--no-verify`
    around it. Re-check that the pin belongs to this `--backup` (digests
    are per-backup), then retry from a different `--domain`; if a second
    mirror produces the same unexpected digest, the pin is probably stale
    rather than the download hostile.

`invalid --sha256 "...": expected 64 hex characters`
  - Malformed pin, rejected before the transfer starts. SHA-256 digests are
    64 hex characters; you may have pasted an MD5 (32).

## Local database backup with `db_cp`

`scripts/db_cp.sh` makes a fast, space-efficient local copy of a
java-tron LevelDB/RocksDB database directory. It is the recommended way
to snapshot an already-extracted database before a risky operation (node
upgrade, config migration, shadow-fork mutation, etc.).

### Why not plain `cp -r`?

A mainnet database is hundreds of GB of `.sst` immutable segment files.
`cp` would duplicate every byte. `db_cp` hard-links `.sst` files instead
— the destination shares the same data blocks, so the copy is nearly
instantaneous and uses negligible extra disk space. Only the small
bookkeeping files (LOCK, CURRENT, MANIFEST, OPTIONS) get independent
inodes, which is required so each copy can be opened independently by
java-tron.

### Requirements

- `rsync` (handles the non-`.sst` tree)
- `ln` (POSIX hard-link; src and dst must be on the **same filesystem**)

### Usage

```bash
# source the function into the current shell
source scripts/db_cp.sh

# one-liner backup with a timestamp suffix
db_cp \
  /data/tron/output-directory/mainnet/database \
  /data/tron/output-directory/mainnet/database-backup-$(date +%Y%m%d-%H%M%S)
```

Or run it directly as a script:

```bash
./scripts/db_cp.sh <src> <dst>
```

### Disk-space accounting

| File type | Copy method | Incremental disk cost |
|---|---|---|
| `.sst` segment files | `ln` (hard-link) | ~0 (shared blocks) |
| LOCK / CURRENT / MANIFEST / OPTIONS | `rsync` (full copy) | bytes, negligible |

The destination directory must not exist before the call — `db_cp`
refuses to clobber an existing path.

### Typical workflow

```bash
# 1. Stop the node (or use a quiesced snapshot)
trond stop --node mainnet-1

# 2. Back up the database
source scripts/db_cp.sh
db_cp \
  /data/tron/output-directory/mainnet/database \
  /data/tron/output-directory/mainnet/database-bak-$(date +%Y%m%d-%H%M%S)

# 3. Perform the risky operation (upgrade, mutation, …)
trond apply --intent intent.yaml --auto-approve --wait

# 4. If something goes wrong, restore:
#    stop the node, delete the current database, rename the backup back.
```

### Limitations

- Hard-links only work within a single filesystem. If src and dst are on
  different block devices, `ln` will fail. Use `rsync -a` instead — it
  will be slower and use full disk space.
- Do **not** use `db_cp` on a live, write-active node. java-tron may be
  mid-compaction, leaving `.sst` files in a transient state. Stop the
  node (or use an OS-level snapshot) first.
