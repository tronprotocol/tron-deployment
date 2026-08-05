package dbfork

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/proto"

	"github.com/tronprotocol/tron-deployment/internal/dbfork/db"
	"github.com/tronprotocol/tron-deployment/internal/dbfork/stores"
	pb "github.com/tronprotocol/tron-deployment/internal/tronproto/pb"
)

// WitnessSpec is one entry from fork.conf's `witnesses:` list. Mirrors
// java DbFork's parsed Config of:
//
//	{ address = "T..."; url = "http://..."; voteCount = 100 }
type WitnessSpec struct {
	// Address is the Base58Check-encoded TRON address (e.g.
	// "TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5W"). DecodeAddress
	// converts to the 21-byte witnessStore key form.
	Address string `yaml:"address"`

	// URL is the optional witness profile URL. java DbFork only
	// sets it when present in fork.conf.
	URL string `yaml:"url"`

	// VoteCount is the witness's initial vote count. Used both for
	// the Witness.vote_count proto field AND for active-witness
	// sort order (descending). java DbFork only sets it when > 0.
	VoteCount int64 `yaml:"voteCount"`
}

// MutateWitnesses applies the fork.conf witnesses block:
//
//   - If retainExisting is false (the default), erases EVERY entry
//     in witnessStore + clears the active-witness key in
//     witnessScheduleStore. Matches java DbFork's iterator-driven
//     wipe.
//   - For each spec, encodes a Witness proto (IsJobs=true, plus
//     optional URL and VoteCount) and writes it under the 21-byte
//     address key in witnessStore.
//   - Replaces the active-witness slate in witnessScheduleStore
//     with the spec addresses concatenated (21 bytes each), sorted
//     by VoteCount descending. Capped at MaxActiveWitnessNum (27)
//     entries per TRON's witness scheduling rules.
//
// Both stores' mutations land in a single batch each (atomic per
// store; cross-store atomicity isn't a guarantee, same as java
// DbFork). Returns the number of witnesses written + number of
// active entries set.
//
// Known divergence from java DbFork: the active-witness sort
// tie-breaker. Java sorts by `voteCount DESC, then ByteString.
// hashCode() DESC` — the latter is JVM-specific. We tie-break by
// raw byte order of the address (deterministic, cross-platform).
// As long as fork.conf has distinct vote counts (the recommended
// case), the active-witness slate is byte-equivalent across both
// implementations.
//
// Recommendation for fork.conf authors: assign unique vote counts
// to every entry. The equivalence test (Task #152) enforces this by
// fixturing only distinct-vote-count inputs; mixed-tie inputs will
// pass both tools individually but diverge byte-wise on the active
// slate.
func MutateWitnesses(
	witnessEng, scheduleEng db.Engine,
	specs []WitnessSpec,
	retainExisting bool,
) (witnessesWritten, activeSet int, err error) {
	witnessBatch := witnessEng.NewBatch()
	defer witnessBatch.Close()
	scheduleBatch := scheduleEng.NewBatch()
	defer scheduleBatch.Close()

	if !retainExisting {
		// Erase all existing witnesses by iterating and queueing
		// deletes into the batch. Then clear the active-witness
		// key from the schedule store.
		it := witnessEng.NewIterator()
		for it.Next() {
			witnessBatch.Delete(it.Key())
		}
		if iterErr := it.Error(); iterErr != nil {
			it.Close()
			return 0, 0, fmt.Errorf("dbfork: iterate witnessStore: %w", iterErr)
		}
		it.Close()
		scheduleBatch.Delete([]byte(stores.KeyActiveWitnesses))
	}

	if len(specs) == 0 {
		// Nothing to add — commit the wipe (if any) and return.
		if err := witnessBatch.Write(); err != nil {
			return 0, 0, fmt.Errorf("dbfork: write witness erasures: %w", err)
		}
		if err := scheduleBatch.Write(); err != nil {
			return 0, 0, fmt.Errorf("dbfork: write schedule erasures: %w", err)
		}
		return 0, 0, nil
	}

	// Decode addresses + build Witness protos + queue puts.
	type pending struct {
		address []byte // 21-byte witnessStore key
		spec    WitnessSpec
	}
	pendings := make([]pending, 0, len(specs))
	for _, s := range specs {
		addr, decodeErr := DecodeAddress(s.Address)
		if decodeErr != nil {
			return 0, 0, fmt.Errorf("dbfork: witness %q: %w", s.Address, decodeErr)
		}
		w := &pb.Witness{
			Address: addr,
			IsJobs:  true,
		}
		if s.VoteCount > 0 {
			w.VoteCount = s.VoteCount
		}
		if s.URL != "" {
			w.Url = s.URL
		}
		data, marshalErr := proto.Marshal(w)
		if marshalErr != nil {
			return 0, 0, fmt.Errorf("dbfork: marshal witness %x: %w", addr, marshalErr)
		}
		witnessBatch.Put(addr, data)
		pendings = append(pendings, pending{address: addr, spec: s})
	}

	// Build the active-witness slate: VoteCount descending,
	// tie-break by byte order ascending (see godoc on MutateWitnesses
	// for the java-tron divergence note). The byte-order tiebreaker
	// makes the comparator a total order, so a stable sort would be
	// redundant — sort.Slice is enough and avoids the stable-sort's
	// extra allocation.
	sort.Slice(pendings, func(i, j int) bool {
		if pendings[i].spec.VoteCount != pendings[j].spec.VoteCount {
			return pendings[i].spec.VoteCount > pendings[j].spec.VoteCount
		}
		// Compare by address bytes — deterministic + cross-platform.
		return string(pendings[i].address) < string(pendings[j].address)
	})

	activeCount := min(len(pendings), stores.MaxActiveWitnessNum)
	// active = concat of 21-byte addresses, mirroring java DbFork's
	// getActiveWitness helper.
	active := make([]byte, 0, activeCount*stores.AddressLength)
	for _, p := range pendings[:activeCount] {
		active = append(active, p.address...)
	}
	scheduleBatch.Put([]byte(stores.KeyActiveWitnesses), active)

	if err := witnessBatch.Write(); err != nil {
		return 0, 0, fmt.Errorf("dbfork: write witness batch: %w", err)
	}
	if err := scheduleBatch.Write(); err != nil {
		return 0, 0, fmt.Errorf("dbfork: write schedule batch: %w", err)
	}
	return len(pendings), activeCount, nil
}
