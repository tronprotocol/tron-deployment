# TRON proto bindings

Source of truth for the java-tron protobuf wire formats used across this
repo. Two consumers today, with different needs from the same protos:

- **`internal/dbfork`** — the capsule messages it reads from and writes
  to java-tron's on-disk stores (`Account`, `Witness`, `SmartContract`, …).
- **`tools/txgen`** — the same messages plus the `Wallet` gRPC service
  stubs its gRPC transport dials.

The package lived under `internal/dbfork/proto/` while dbfork was the
only consumer. It holds nothing dbfork-specific — the Go package has
always been called `tronpb` — so it moved out when the second consumer
arrived rather than leaving txgen importing a gRPC client from under the
DB-mutation engine.

## Layout

```
internal/tronproto/
├── upstream/                # git subtree of github.com/tronprotocol/protocol
│   ├── core/
│   │   ├── Tron.proto       # Account, Witness, Permission, Vote, AccountAsset
│   │   └── ...
│   └── api/                 # api.proto — Wallet gRPC service (+ messages)
├── thirdparty/              # google/api annotations, imported by api.proto
│   └── google/api/          #   NOT in upstream/: that is a subtree
├── pb/                      # generated *.pb.go (committed, FLAT)
│   ├── Tron.pb.go
│   ├── account.pb.go
│   ├── smart_contract.pb.go
│   └── ...                  # all in `package tronpb`, no subdirs
├── gen.go                   # `go generate` entry
└── README.md
```

`upstream/` is the unmodified protocol repo at a pinned tag. `pb/`
holds the generated Go bindings — these are committed so `go build`
works without protoc on every machine.

All generated files land FLAT in `pb/` (no `core/` or `core/contract/`
subdirs), declaring `package tronpb`. This is intentional — upstream
.proto files cross-reference each other across directory boundaries
(`Tron.proto` ↔ `core/contract/*.proto`), which only works inside a
single Go package without Go import cycles.

## Pinned upstream version

Last subtree pull: **GreatVoyage-v4.8.1** (Feb 2026).

The full pin history is in this directory's git log (look for
`Squashed '...' content from commit ...` messages from
`git subtree add/pull` operations).

> **The subtree was added before this directory moved.** Every existing
> squash commit carries the pre-move path in its tracking footer:
>
> ```
> git-subtree-dir: internal/dbfork/proto/upstream
> git-subtree-split: 4c726956542b8dff5a4bd5c54aa07cd9da257d08
> ```
>
> `git subtree pull --prefix=internal/tronproto/upstream` scans for a
> footer naming *that* prefix and will not find one, so it cannot locate
> the previous split and will not produce an incremental merge. Either
> pass the recorded split explicitly, or run the pull and check the
> resulting diff contains only the intended upstream delta before
> committing — do not assume an empty-looking merge means "already up to
> date". The first pull after this move re-establishes the footer under
> the new path; subsequent ones behave normally.

## Syncing upstream

When java-tron releases a new compatible version (typically a yearly
"GreatVoyage" tag), bump the proto subtree:

```bash
# From repo root
git subtree pull \
  --prefix=internal/tronproto/upstream \
  https://github.com/tronprotocol/protocol.git \
  GreatVoyage-v4.8.2 \
  --squash

# Regenerate bindings
go generate ./internal/tronproto/...

# Verify equivalence against java DbFork (release gate)
go test ./internal/dbfork/ -run TestEquivalenceVsJavaDbFork

# If green, commit both the subtree merge AND the regenerated pb/.
git add internal/tronproto/upstream internal/tronproto/pb
git commit -m "dbfork: bump proto to GreatVoyage-v4.8.2"
```

### When upstream drops a .proto we used to generate

Remove the file path from `PROTO_FILES` in
`scripts/gen-tron-protos.sh` and re-run `go generate`. The `pb/`
dir is wiped + regenerated on every run, so stale `.pb.go` files are
cleaned up automatically — no manual `git rm` needed.

### When upstream adds a new transitive import

If `Tron.proto` (or one of our generated files) starts importing a
.proto we don't yet list in `PROTO_FILES` (inside the gen script),
`go build` will fail with:

```
undefined: tronpb.<NewTypeName>
```

Fix by adding the new .proto path to `PROTO_FILES` in
`scripts/gen-tron-protos.sh` and re-running `go generate`. The
M-flag catch-all in the script already maps EVERY .proto in
upstream/ to our package, so we just need to tell protoc to emit
the .pb.go for it.

## What we generate (and why this subset)

`scripts/gen-tron-protos.sh` lists the .proto files we run through
protoc. The current subset covers the messages dbfork's mutation
engine reads + writes:

| Proto file | Messages used by dbfork |
|---|---|
| `core/Tron.proto` | `Account`, `Witness`, `Permission`, `Vote`, `AccountAsset` |
| `core/contract/asset_issue_contract.proto` | `AssetIssueContract` (TRC10 metadata) |
| `core/contract/smart_contract.proto` | `SmartContract` (TRC20 contract object) |
| `core/contract/account_contract.proto` | `AccountCreateContract` (permission updates) |
| transitive deps | autoload by protoc when imported |

Everything else stays in `upstream/` for future extension — add to
`PROTO_FILES` in the gen script when you need it.

## Why not just use `tronprotocol/grpc-gateway` Go bindings?

`tronprotocol/grpc-gateway` is a fork of the grpc-gateway *plugin*,
not a published set of TRON Go bindings — there is no `WalletClient`
in it, so it was never an option for the service stubs. Generating
from the vendored protos keeps trond's static binary small and our
dependency surface narrow.

Note the service stubs ARE generated now: `tools/txgen`'s gRPC
transport needs a real `WalletClient`, and hand-writing a trimmed
copy of the `Wallet` service would silently drift from upstream the
first time a signature changed. That is why `api/api.proto` and the
full set of contract protos its method signatures reference are in
`PROTO_FILES`, even though dbfork itself touches none of them.

## Tooling install

`protoc-gen-go` is pinned via the Go 1.24+ `tool` directive in
`go.mod` (single source of truth — kept automatically in step with
`google.golang.org/protobuf` runtime version). `go install tool`
reads that pin and installs into `$GOBIN`:

```bash
# macOS
brew install protobuf
go install tool

# Linux (debian/ubuntu)
sudo apt install protobuf-compiler
go install tool
```

`$GOPATH/bin` (or `$GOBIN`) must be on `$PATH` so protoc can find
`protoc-gen-go`.

After running `scripts/gen-tron-protos.sh`, `git diff
internal/tronproto/pb/` should be empty. If you see a diff, the
`go install tool` step was skipped or your `$PATH` is picking up a
different `protoc-gen-go` than the pinned one. The `proto-drift`
CI job (`.github/workflows/ci.yml`) installs from the same pin, so
any local-vs-CI mismatch points at your install, not the pin.

## Why protoc-gen-go and not gogo or buf?

- **protoc-gen-go** is the reference Go protobuf generator,
  maintained by the Go team. Mature, predictable.
- **gogo/protobuf** is faster + smaller but archived since 2022.
- **buf** is a higher-level toolchain that would add a buf.yaml +
  registry workflow; overkill for a single subtree.

Reference: https://protobuf.dev/reference/go/
