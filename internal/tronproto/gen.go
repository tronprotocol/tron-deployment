// Package proto holds the protobuf bindings trond's dbfork engine
// uses to read + write java-tron's on-disk capsule formats.
//
// Pinned upstream version: GreatVoyage-v4.8.1
// (last subtree pull; bump this tag literal AND run `git subtree pull`
// per proto/README.md together — keeping them in lockstep lets a
// future reader grep the source instead of digging through git log.)
//
// Layout:
//
//	upstream/  — git subtree of github.com/tronprotocol/protocol at the
//	             pinned tag above. Source-of-truth .proto files. Sync
//	             via subtree pull when bumping java-tron compatibility
//	             — see README.md.
//	pb/        — generated *.pb.go files. Committed to the repo so
//	             `go build` doesn't require protoc on every dev
//	             machine. Regenerate via `go generate ./...` from
//	             repo root after a subtree pull.
//
// Scope: we only generate Go bindings for the subset of messages
// dbfork actually reads / writes (Account, Witness, Permission,
// SmartContract, AssetIssueContract + their transitive deps).
// upstream/ stays fully populated for future extension; pb/ stays
// narrow so binary size + compile time don't bloat.
//
// Generation requires protoc + protoc-gen-go:
//
//	brew install protobuf
//	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
//
// Then from repo root:
//
//	go generate ./internal/tronproto/...
//
// Platform: bash-only (Linux + macOS). Windows contributors regenerate
// via WSL — gen-tron-protos.sh is a bash 3.2+ script and the
// go-generate directive below invokes it via `bash -c`.
//
//go:generate bash -c "../../scripts/gen-tron-protos.sh"
package proto
