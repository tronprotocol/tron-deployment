# txgen

A TRON stress-test transaction generator. Pure-Go HTTP client — no java-tron source dependency, no JDK runtime.

Functional: three subcommands (`generate` / `broadcast` / `statistic`), TRX / TRC10 / TRC20 transaction types, CSV intermediate format, and a broadcast report that matches the upstream layout so existing dashboards keep working.

Useful for:
- Loading a private chain to its TPS ceiling with shaped traffic mixes.
- Snapshot validation paired with a `db fork` clone.
- Benchmarking node patches (mempool, EVM, signature verification) under reproducible load.

## Install

```bash
# From the tron-deployment repo root
make build-txgen        # → bin/txgen  (pure Go, no CGO)
make install-txgen      # → $(GOBIN)/txgen
```

Requires Go 1.21+. Builds a single ~10 MB static binary with no external runtime dependencies.

The HTTP broadcast layer lives in `tools/common/broadcast/` and is shared with `tools/replay/`.

### Build variants

| Make target | CGO | Falcon-512 | Portability |
|---|---|---|---|
| `build-txgen` | No | No (stub returns error) | Any platform, cross-compile works |
| `build-txgen-falcon` | Yes (liboqs) | Yes | Must build on target platform; liboqs required |

**`build-txgen` is the default and the only variant built in CI.** The `falcon` build tag is off by
default so Go's standard cross-compilation and static-binary guarantees are preserved.

`build-txgen-falcon` requires [liboqs](https://github.com/open-quantum-safe/liboqs) ≥ 0.10 installed
as a C library. It uses CGO, which means:

- A C compiler must be present at build time on every target platform.
- Cross-compilation (`GOOS=linux` from macOS) does not work without a cross-toolchain.
- The binary dynamically links `liboqs.so` at runtime unless you also pass `-extldflags="-static"`.

**CI behaviour:** `make build-txgen-falcon` detects whether liboqs is installed and **skips with a
warning (exit 0)** rather than failing when it is not found. This means CI runners without liboqs
installed will silently skip the falcon binary; they still build and test the pure-Go binary via
`make build-txgen`.

Local install (macOS):

```bash
brew install liboqs
make build-txgen-falcon   # → bin/txgen with Falcon-512 support
```

---

## Quickstart — generate and broadcast to a private chain

End-to-end flow against a private chain at `private_node_ip:8090`. Two commands.

### 1. Write a config

Save this as `txgen.json` next to the binary:

```json
{
  "node": "http://private_node_ip:8090",

  "generate": {
    "totalTxCount": 100000,
    "receiverAddressCount": 10000,
    "concurrency": 16,
    "privateKey": "<64-hex-char sender key>",
    "outputDir": "txgen-output",
    "txType": { "transfer": 100, "transferTrc10": 0, "transferTrc20": 0 },
    "transferAmount": 1,
    "expirationMillis": 60000
  },

  "broadcast": {
    "inputDir": "txgen-output",
    "tpsLimit": 2000,
    "saveTxId": true
  }
}
```

The sender (`generate.privateKey`) must hold enough TRX on the private chain to cover all activations + transfer amounts. Pre-fund it with `Toolkit.jar db fork` if needed.

### 2. Generate signed transactions

```bash
txgen generate -c txgen.json
```

`generate` first builds `receiverAddressCount` fresh secp256k1 receivers (dumped to `txgen-output/receivers.csv` for auditability or `db fork` pre-funding), then builds `totalTxCount` signed TRX transfers fanning out across those receivers and splits them into `txgen-output/generate-tx-NNNN.csv`. Workers issue one HTTP round-trip per tx to the node (`/wallet/createtransaction`) for the unsigned form, then sign locally with secp256k1.

By default txgen rewrites each unsigned transaction before signing so
`raw_data.expiration = raw_data.timestamp + 60000` (1 minute), matching
java-tron's own default. Override with `generate.expirationMillis` when you
need a longer broadcast window — but see
[Transaction expiration](#transaction-expiration) for why it must stay well
under 86400000.

### 3. Broadcast to the private chain

```bash
txgen broadcast -c txgen.json
```

Streams every `generate-tx-*.csv` through `/wallet/broadcasttransaction` at `tpsLimit` (token-bucket throttled). Captures the head block before and after, then walks that range to compute the actual on-chain TPS. Output:

- `broadcast-txid.csv` — txIDs the node accepted into its pool
- `broadcast-report.txt` — final report (TPS, on-chain rate, block size stats)

Sample report:

```
Stress test report:
broadcast tps limit:        2000
statistic block range:      startBlock: 67926067, endBlock: 67926133
total generate tx count:    100000
total broadcast ok:         98214
total broadcast fail:       1786
tx on chain rate:           0.96810
cost time:                  0.85 minutes
max block size:             9615
min block size:             3001
tps:                        1933.65
miss block rate:            0.000000
```

### 4. (Optional) Re-measure TPS for any block range

```bash
# Edit statistic.startBlock / endBlock in txgen.json, then:
txgen statistic -c txgen.json
```

Useful for tightening a wider broadcast window to just the active span, or re-running the math from a node that synced later.

---

## Mixing TRX, TRC10, TRC20

Change `generate.txType` weights (must sum to 100) and add the token identifiers:

```json
{
  "generate": {
    ... 
    "txType": {
      "transfer": 60,
      "transferTrc10": 10,
      "transferTrc20": 30
    },
    "trc10Id": "1000001",
    "trc20Address": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
    "trc20FeeLimit": 100000000
  }
}
```

The sender must hold both the TRC10 balance and TRC20 balance — pre-fund via `db fork` or send a few warm-up txs first.

---

## Post-quantum (PQ / 抗量子) transactions

For nodes built with java-tron's post-quantum signature support, txgen can sign
some fraction of transactions with a PQ scheme instead of secp256k1. A PQ tx
carries a `pq_auth_sig` envelope (`{scheme, public_key, signature}`) rather than
the ECDSA `signature`; the signed digest is unchanged (`txID = sha256(raw_data)`),
so the rest of the generate → broadcast → statistic flow is identical.

Enable it with a `generate.pq` block and set `ratio` to the percent of
transactions that should be PQ-signed (the rest are ECDSA-signed):

```json
{
  "generate": {
    "...": "...",
    "privateKey": "<64-hex-char sender key>",
    "txType": { "transfer": 100, "transferTrc10": 0, "transferTrc20": 0 },
    "pq": {
      "enabled": true,
      "scheme": "ML_DSA_44",
      "seed": "<64-hex-char (32-byte) seed>",
      "ratio": 30
    }
  }
}
```

- **Ratio (mixed signing).** `pq.ratio` is the percent of generated txs that are
  PQ-signed; the remaining `100 - ratio` percent are ECDSA-signed. It is an
  independent roll from the `txType` contract-type split, so each contract type
  gets roughly the same PQ/ECDSA mix. `ratio` defaults to `100` when omitted (all
  PQ); to turn PQ off entirely set `pq.enabled = false` rather than `ratio = 0`.
- **Two senders.** Every tx's owner address must match the key that signs it, so
  PQ txs are sent from the PQ-derived address and ECDSA txs from the `privateKey`
  address. When `ratio < 100`, **both** `privateKey` and `pq.seed` are required
  (when `ratio == 100`, `privateKey` is not needed). The PQ sender is derived from
  `pq.seed` as `0x41 ‖ Keccak-256(public_key)[12..32]`. `txgen generate` logs both
  sender addresses at startup.
- **Scheme.** Two schemes are available:
  - `ML_DSA_44` (FIPS 204 / CRYSTALS-Dilithium-2) — supported in the **default
    pure-Go build** (`make build-txgen`). No extra dependencies.
  - `FN_DSA_512` (Falcon-512) — supported only in the **falcon build**
    (`make build-txgen-falcon`, requires liboqs). The default build returns an
    error if `FN_DSA_512` is requested. See [Build variants](#build-variants) for
    details on the CGO/liboqs requirement and portability trade-offs.
- **Account provisioning (required).** The node only accepts a PQ signature when
  the PQ sender account's permission already contains this PQ public key with
  enough weight. Install it out-of-band (e.g. via `AccountPermissionUpdate`)
  before generating; txgen does not perform the permission update. The same
  `seed` always derives the same keypair/address, so provision once and reuse.

The config example above uses `ML_DSA_44` (seed-derived, default build). `FN_DSA_512`
is set up differently — see below.

### Falcon-512 (FN_DSA_512) setup

`FN_DSA_512` needs the falcon build and an explicit keypair (it is **not** seed-derived):

1. **Build with liboqs** (see [Build variants](#build-variants)):
   ```bash
   brew install liboqs          # macOS (Linux: build liboqs from source)
   make build-txgen-falcon      # → bin/txgen with Falcon-512
   ```
2. **Generate a keypair** with the `keygen` subcommand (falcon build only):
   ```bash
   txgen keygen --scheme FN_DSA_512 --out falcon-keypair.json
   ```
   It prints the derived TRON address plus a ready-to-paste `pq` block, and writes
   `privateKey` (1281-byte liboqs secret key, 2562 hex chars) and `publicKey`
   (896-byte h polynomial, 1792 hex chars) to the file.
3. **Configure** `generate.pq` with those keys (use `privateKey` + `publicKey`
   instead of `seed`):
   ```json
   "pq": {
     "enabled": true,
     "scheme": "FN_DSA_512",
     "privateKey": "<2562-hex liboqs SK from keygen>",
     "publicKey":  "<1792-hex h polynomial from keygen>",
     "ratio": 30
   }
   ```
4. **Provision the account**: fund the printed address with TRX (activation +
   transfer amounts) **and** register the Falcon public key on the account via
   `AccountPermissionUpdate` — `keygen` prints the exact steps.

> Recap: `ML_DSA_44` → `seed` (default build, no liboqs). `FN_DSA_512` → `privateKey`
> + `publicKey` from `keygen` (falcon build). `pq.ratio` and the two-senders rule
> apply to both schemes.

---

## Prerequisites

1. **Private chain node is reachable** at the `node` URL (default `http://127.0.0.1:8090`).
2. **Sender account is funded.** `generate.privateKey` must hold enough TRX for activation fees plus the transferred amount, plus any TRC10 / TRC20 balance you use.
3. **Receivers exist or get activated.** Fresh addresses generated inline by `txgen generate` are valid — the first transfer to each pays a one-time activation fee out of the sender's TRX.

Common helpers:
- `Toolkit.jar db fork` with `<outputDir>/receivers.csv` to pre-fund receivers in one shot.
- `generate.expirationMillis` sets how long a generated CSV stays
  broadcastable. It defaults to 60 s, matching java-tron; raise it for a
  longer window, but keep it well under the 24 h ceiling — see
  [Transaction expiration](#transaction-expiration).

---

## Subcommands (reference)

### `generate` — build receivers + sign synthetic txs

```bash
txgen generate -c txgen.json
```

Step 1: build `generate.receiverAddressCount` fresh secp256k1 keypairs and dump them to `<outputDir>/receivers.csv`. Receivers are valid TRON addresses — they get activated by the first transfer (pays a one-time activation fee out of the sender's TRX). Keys are included in the sidecar so the same addresses can be pre-funded via `Toolkit.jar db fork` if you'd rather skip the activation fees.

Step 2: for each of `generate.totalTxCount` transactions:

1. Pick a tx type by weighted random sample.
2. Pick a receiver at random from the in-memory list.
3. POST to the node (`/wallet/createtransaction`, `/wallet/transferasset`, or `/wallet/triggersmartcontract`) to get an unsigned tx + `raw_data_hex` + `txID`.
4. Sign `txID` locally with secp256k1 (canonical low-S; `[r || s || v]` 65-byte layout).
5. Attach the signature and append to a `generate-tx-NNNN.csv` file.

Output is sharded across multiple CSV files (task size auto-derived from `totalTxCount` and `concurrency`); `concurrency` workers consume the task queue.

### `broadcast` — replay CSV at a target TPS

```bash
txgen broadcast -c txgen.json
```

Streams CSVs to the node, throttled by a token bucket (100 ms refill). Failed broadcasts log the first 20 verbatim, then every 10,000th, so stderr stays readable without hiding structural issues (bad signature, dead node).

#### Transports

`broadcast.transport` picks the wire protocol.

| | `http` (default) | `grpc` |
|---|---|---|
| Endpoint | `POST /wallet/broadcasttransaction` on `node` (`:8090`) | `Wallet.BroadcastTransaction` on `grpc.endpoint` (`:50051`) |
| Payload | the CSV row verbatim | `raw_data_hex` + `signature`, decoded to protobuf |
| Concurrency | `workers` (default `tpsLimit / 50`, clamped to `[4, 256]`) | `grpc.connections` × `grpc.callsPerConnection` |
| PQ transactions | supported | **not** supported — see below |

**Concurrency is not the same axis on the two transports, which is why `grpc` is a separate mode rather than a flag.** Over HTTP, `workers` is effectively the number of connections: each worker blocks on its own request and `http.Client` opens sockets to match. Over gRPC, one `ClientConn` multiplexes *every* call onto a single HTTP/2 connection, and java-tron caps in-flight calls per connection (`node.rpc.maxConcurrentCallsPerConnection`). Past that cap the extra calls are not rejected — they queue inside the client transport. So raising a single `workers` number stops adding load at the cap while still *looking* like it applies more.

txgen therefore asks for the two axes separately, and refuses `workers` under `grpc` rather than guessing which one you meant:

```json
"broadcast": {
  "transport": "grpc",
  "grpc": {
    "connections": 4,
    "callsPerConnection": 100
  }
}
```

One worker runs per call slot, so `connections × callsPerConnection` is both the worker count and the exact in-flight shape — connection *k* carries `callsPerConnection` calls and the mapping never moves during a run. To probe a server's per-connection cap, hold `connections: 1` and raise `callsPerConnection` past it, then compare against the same total spread over more connections. Calls that queue behind the cap are not rejected; if they queue longer than the 5 s call timeout they surface as `GRPC_DeadlineExceeded`.

Measured against a private node with `maxConcurrentCallsPerConnection = 1`, 2000 transactions each:

| shape | calls in flight | elapsed |
|---|---|---|
| `connections: 1, callsPerConnection: 1` | 1 | 2.06 s |
| `connections: 1, callsPerConnection: 50` | 50 requested — capped to 1 | 2.02 s |
| `connections: 50, callsPerConnection: 1` | 50, one per connection | 1.37 s |

The first two rows are the point: asking for 50 in-flight calls on one connection performs exactly like asking for one, because the server's cap pins that connection and the other 49 workers queue in the client transport. Only spreading the same total across connections adds throughput. A single `workers`-style number cannot express that difference, and would report the middle row as "50 workers" while delivering the throughput of one.

(The third row's transactions had a higher duplicate rate, so treat its margin as directional rather than a clean 1.5×. The first two rows had matching accept/reject profiles.)

All connections are opened and driven to `READY` before the first transaction, so a wrong endpoint fails at startup instead of arriving as thousands of broadcast failures. Block lookups for the report stay on HTTP either way, so the on-chain TPS numbers are measured identically across transports.

**PQ transactions cannot use the gRPC transport.** `Transaction.pq_auth_sig` exists only on the PQ fork; the pinned upstream protocol these bindings are generated from has no such field, so it would be dropped silently and every transaction would fail signature verification at the node — for a reason pointing nowhere near the transport. txgen refuses the row instead (`PQ_UNSUPPORTED`). Use `transport: "http"` for PQ runs.

The transport also checks that re-encoding `raw_data` reproduces `raw_data_hex` byte for byte before sending. It always should — but if the node runs a protocol fork carrying fields these bindings lack, the round trip reorders them, which would change the txID and void the signature. That fails loudly as `RAW_DATA_DRIFT` rather than broadcasting a mutated transaction.

#### Transaction expiration

A node accepts a transaction only while

```
headBlockTime < raw_data.expiration <= headBlockTime + 86_400_000
```

(`Manager.validateCommon`, `Constant.MAXIMUM_TIME_UNTIL_EXPIRATION`).

txgen sets `expiration = raw_data.timestamp + expirationMillis`, but the node compares against the **head block's** timestamp, which trails `raw_data.timestamp` by up to one block interval. So at `expirationMillis = 86400000` the upper bound reduces to `raw_data.timestamp > headBlockTime`, which is true for most of every block interval — the transaction is expired the moment it is built, and only becomes acceptable once the chain produces a block past its creation time.

That was the shipped default, and it produced a memorable symptom: `generate` immediately followed by `broadcast` failed almost entirely, while the *same CSV* broadcast a few seconds later succeeded. Measured on a private chain: 0/200 accepted back-to-back, 200/200 after waiting two blocks.

The default is now `60000`, matching java-tron's own `TRANSACTION_DEFAULT_EXPIRATION_TIME`, and values at or above the ceiling are rejected when the config loads rather than by every broadcast.

### `statistic` — compute on-chain TPS for any range

```bash
txgen statistic -c txgen.json
```

Walks `[statistic.startBlock, statistic.endBlock]` on the node and emits a TPS report.

---

## Output files

All files land under `generate.outputDir` (default `txgen-output/`), except the broadcast/statistic reports which are written to the paths configured in the `broadcast` / `statistic` sections.

| File | Contents |
|---|---|
| `receivers.csv` | Inline-generated receiver addresses + private keys. Useful for `db fork` pre-funding. **Secret:** written `0600` inside a `0700` `outputDir`, and an existing file is re-tightened to `0600` on every run — never commit it or ship it in a CI artifact (`txgen-output/` is gitignored). On a filesystem with no POSIX modes (vfat/exFAT, some SMB mounts) the `chmod` fails and the run aborts rather than dumping keys world-readable; point `outputDir` at a real filesystem. |
| `generate-tx-NNNN.csv` | Signed transactions, `txID,signed_tx_json` per row. |
| `broadcast-txid.csv` | TxIDs the node accepted into its pool (one per line). |
| `broadcast-report.txt` | Final report (TPS, on-chain rate, block size stats). |
| `tps-statistic.txt` | `statistic` subcommand output. |

---

## Configuration reference

txgen reads a single JSON file (default `./txgen.json`, override with `-c` / `--config`).

### Full example

```json
{
  "node": "http://private_node_ip:8090",

  "generate": {
    "totalTxCount": 600000,
    "receiverAddressCount": 100000,
    "concurrency": 16,
    "privateKey": "aab926e86a17f0f46b4d22e61725edd5770a5b0fbdabb04b0f46ee499b1e34f2",
    "outputDir": "txgen-output",
    "txType": { "transfer": 60, "transferTrc10": 10, "transferTrc20": 30 },
    "trc10Id": "1000001",
    "trc20Address": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
    "transferAmount": 1,
    "expirationMillis": 60000,
    "trc20FeeLimit": 100000000
  },

  "broadcast": {
    "inputDir": "txgen-output",
    "tpsLimit": 3000,
    "saveTxId": true,
    "txIdFile": "broadcast-txid.csv",
    "reportFile": "broadcast-report.txt"
  },

  "statistic": {
    "startBlock": 0,
    "endBlock": 0,
    "outputFile": "tps-statistic.txt"
  }
}
```

### Schema

| Section | Key | Default | Description |
|---|---|---|---|
| top-level | `node` | `http://127.0.0.1:8090` | TRON HTTP API endpoint (used by every subcommand). |
| `generate` | `totalTxCount` | — (required) | Total signed txs to build. |
| `generate` | `receiverAddressCount` | `1000` | Fresh receiver addresses to generate in-memory and dump to `<outputDir>/receivers.csv`. |
| `generate` | `concurrency` | `8` | Worker goroutines (each issues node round-trips serially). Task size (rows per CSV file) is auto-derived so each worker gets ~4 tasks, clamped to [1000, 100000]. |
| `generate` | `privateKey` | — (required unless `pq.enabled` with `pq.ratio` 100) | Sender secp256k1 key, hex (64 chars). Signs the ECDSA share of txs. |
| `generate` | `outputDir` | `txgen-output` | Directory for `receivers.csv` and `generate-tx-NNNN.csv` files. |
| `generate` | `txType.transfer` | — | TRX share, in percent. |
| `generate` | `txType.transferTrc10` | — | TRC10 share, in percent. |
| `generate` | `txType.transferTrc20` | — | TRC20 share, in percent. The three weights must sum to 100. |
| `generate` | `trc10Id` | — | TRC10 token id (numeric string). Required iff `transferTrc10 > 0`. |
| `generate` | `trc20Address` | — | TRC20 contract address (base58 or hex). Required iff `transferTrc20 > 0`. |
| `generate` | `transferAmount` | `1` | Amount per tx in the smallest unit (SUN for TRX, raw token units otherwise). |
| `generate` | `expirationMillis` | `60000` | Transaction lifetime from `raw_data.timestamp`; txgen rewrites `raw_data.expiration`, `raw_data_hex`, and `txID` before signing. Must be **under** 86400000 — see [Transaction expiration](#transaction-expiration). |
| `generate` | `trc20FeeLimit` | `100000000` | Fee limit (SUN) on TRC20 calls. 100 TRX is plenty for a vanilla `transfer`. |
| `generate` | `pq.enabled` | `false` | Mix in post-quantum signing (`pq_auth_sig`). See [Post-quantum transactions](#post-quantum-pq--抗量子-transactions). |
| `generate` | `pq.scheme` | `ML_DSA_44` | PQ scheme: `ML_DSA_44` (default build) or `FN_DSA_512` (falcon build only, requires liboqs). |
| `generate` | `pq.seed` | — (required iff `pq.enabled`) | 32-byte hex (64 chars) seed; derives the PQ keypair and sender address. |
| `generate` | `pq.ratio` | `100` | Percent of txs PQ-signed (1–100); the rest are ECDSA-signed. Only used when `pq.enabled`. |
| `broadcast` | `inputDir` | = `generate.outputDir` | Where to find `generate-tx-*.csv`. |
| `broadcast` | `tpsLimit` | `1000` | Token-bucket cap, txs/second. |
| `broadcast` | `transport` | `http` | Wire protocol: `http` or `grpc`. See [Transports](#transports). |
| `broadcast` | `workers` | `tpsLimit / 50`, clamped to `[4, 256]` | Concurrent HTTP requests. **Rejected** under `transport: grpc` — use the two `grpc` axes instead. |
| `broadcast` | `grpc.endpoint` | host of `node` + `:50051` | `host:port` of the FullNode gRPC service. Plaintext; no TLS. |
| `broadcast` | `grpc.connections` | `1` | Independent HTTP/2 connections to open. |
| `broadcast` | `grpc.callsPerConnection` | `100` | In-flight calls on each connection. Matches java-tron's own default cap, so an unconfigured run sits exactly at the server's ceiling. |
| `broadcast` | `saveTxId` | `false` | Append accepted txIDs to `txIdFile`. |
| `broadcast` | `txIdFile` | `broadcast-txid.csv` | One txID per line. |
| `broadcast` | `reportFile` | `broadcast-report.txt` | Final report path. |
| `statistic` | `startBlock` | — | Block range start (inclusive). |
| `statistic` | `endBlock` | — | Block range end (inclusive). |
| `statistic` | `outputFile` | `tps-statistic.txt` | Report path. |

---

## Common failure modes

| Failure | Cause | Fix |
|---|---|---|
| `generate: probe node` failure | Node URL wrong or unreachable | Check `node` URL and that the HTTP port (8090 by default) is open. |
| `generate.privateKey is required` | `txType` weights set but no sender key | Set `generate.privateKey` to a funded account's secp256k1 hex. |
| `generate: balance is not sufficient` (in node logs) | Sender ran out of TRX mid-run | Top up the sender, or shrink `totalTxCount`. |
| `broadcast fail: SIGERROR: ...` | Signature didn't verify | Confirm the sender key matches the address that owns the source funds. Re-run `generate`. |
| `broadcast fail: DUP_TRANSACTION_ERROR` | Same CSV was broadcast twice | Normal on a re-run. Move the old CSV out of `inputDir`. |
| `broadcast fail: TRANSACTION_EXPIRATION_ERROR` | CSV is older than `generate.expirationMillis` — **or** `expirationMillis` is near 86400000, see [Transaction expiration](#transaction-expiration) | Re-run `generate`. Do **not** raise `expirationMillis` toward the ceiling; that is what causes the second case. |
| `statistic: fetch block X` | Block doesn't exist yet, or RPC is filtered | Tighten the range, or wait until the chain catches up. |
| `grpc connection N to ... not ready` | gRPC port wrong, closed, or the node is still booting | Check `broadcast.grpc.endpoint`; java-tron's `node.rpc.port` is 50051 (50061 is the SolidityNode, which does not accept broadcasts). |
| `broadcast fail: GRPC_DeadlineExceeded` | Calls queued behind the server's per-connection stream cap for longer than 5 s | Expected when `callsPerConnection` exceeds the node's `maxConcurrentCallsPerConnection`. Lower it, or raise `connections`. |
| `broadcast fail: GRPC_Unavailable` | Node restarted or the connection dropped mid-run | Check the node; the transport fails fast rather than blocking on reconnect. |
| `broadcast fail: PQ_UNSUPPORTED` | PQ-signed CSV sent over the gRPC transport | Set `broadcast.transport` to `http` — the pinned protocol has no `pq_auth_sig` field. |
| `broadcast fail: RAW_DATA_DRIFT` | Node speaks a protocol fork carrying `raw_data` fields these bindings lack | Use `transport: "http"`, and re-pin `internal/tronproto/upstream` to the fork's protocol. |

---

## Roadmap

- **Local proto-built unsigned txs.** Drop the per-tx HTTP round-trip in `generate` by serializing `Transaction.raw` locally. Would push generate-phase throughput up by ~10×.
- **Multi-target broadcast.** Round-robin across a list of `broadcastUrl`s instead of one `node`.
- **Synthetic blend.** Mix synthetic txs with replayed mainnet txs (paired with `tools/replay`).
- **Prometheus metrics.** Expose generate / broadcast counters for Grafana panels.
