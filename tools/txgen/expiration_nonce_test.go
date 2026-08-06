package main

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	tronpb "github.com/tronprotocol/tron-deployment/internal/tronproto/pb"
)

// The node stamps raw_data.timestamp with its own clock, so transactions
// built in the same millisecond with the same sender, receiver and amount
// have byte-identical raw_data and therefore one txID. The node accepts
// the first and rejects the rest with DUP_TRANSACTION_ERROR — while the
// run reports the full generated count, so the applied load is quietly
// smaller than the number on screen.
//
// generate gives each transaction a distinct expiration offset. This
// pins the property that relies on: same transaction, different offset,
// different txID.

func identicalUnsigned(t *testing.T) *UnsignedTx {
	t.Helper()
	param, err := anypb.New(&tronpb.TransferContract{
		OwnerAddress: mustHex(t, "41e552f6487585c2b58bc2c9bb4492bc1f17132cd0"),
		ToAddress:    mustHex(t, "41d1e7a6bc354106cb410e65ff8b181c600ff14292"),
		Amount:       1,
	})
	if err != nil {
		t.Fatalf("pack contract: %v", err)
	}
	raw := &tronpb.TransactionRaw{
		RefBlockBytes: mustHex(t, "1234"),
		RefBlockHash:  mustHex(t, "0102030405060708"),
		Timestamp:     1_700_000_000_000,
		Expiration:    1_700_000_060_000,
		Contract: []*tronpb.Transaction_Contract{{
			Type: tronpb.Transaction_Contract_TransferContract, Parameter: param,
		}},
	}
	rawBytes, err := proto.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rawHex := hex.EncodeToString(rawBytes)
	body, err := json.Marshal(map[string]any{
		"txID":         "00",
		"raw_data_hex": rawHex,
		"raw_data":     map[string]any{"timestamp": raw.Timestamp},
	})
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return &UnsignedTx{TxID: "00", RawDataHex: rawHex, Raw: body}
}

func TestExtendExpiration_DistinctOffsetsYieldDistinctTxIDs(t *testing.T) {
	base := identicalUnsigned(t)
	same := identicalUnsigned(t)
	if base.RawDataHex != same.RawDataHex {
		t.Fatal("fixture is not identical; the test would prove nothing")
	}

	a := identicalUnsigned(t)
	b := identicalUnsigned(t)
	if err := a.ExtendExpiration(60_000 + 1); err != nil {
		t.Fatalf("ExtendExpiration: %v", err)
	}
	if err := b.ExtendExpiration(60_000 + 2); err != nil {
		t.Fatalf("ExtendExpiration: %v", err)
	}
	if a.TxID == b.TxID {
		t.Errorf("byte-identical transactions kept the same txID (%s) across different "+
			"expiration offsets; the node would reject one as DUP_TRANSACTION_ERROR", a.TxID)
	}

	// And the control: the SAME offset must still be deterministic, or
	// the fix would be hiding collisions behind randomness.
	c := identicalUnsigned(t)
	d := identicalUnsigned(t)
	if err := c.ExtendExpiration(60_000 + 7); err != nil {
		t.Fatalf("ExtendExpiration: %v", err)
	}
	if err := d.ExtendExpiration(60_000 + 7); err != nil {
		t.Fatalf("ExtendExpiration: %v", err)
	}
	if c.TxID != d.TxID {
		t.Errorf("the same input and offset produced different txIDs (%s vs %s); "+
			"generation must stay deterministic", c.TxID, d.TxID)
	}
}

// TestExpirationNonce_DistinctWithinSpread pins that the counter hands out
// distinct offsets, and that they stay inside the bound so expiration
// cannot walk toward java-tron's 24h ceiling.
func TestExpirationNonce_DistinctWithinSpread(t *testing.T) {
	seen := map[int64]bool{}
	for i := 0; i < 1000; i++ {
		n := expirationNonce.Add(1) % expirationSpreadMillis
		if seen[n] {
			t.Fatalf("offset %d handed out twice within %d draws", n, i+1)
		}
		seen[n] = true
		if n < 0 || n >= expirationSpreadMillis {
			t.Fatalf("offset %d outside [0, %d)", n, expirationSpreadMillis)
		}
	}
	if expirationSpreadMillis >= maxExpirationMillis {
		t.Errorf("the spread (%d) is not comfortably below the ceiling (%d)",
			expirationSpreadMillis, maxExpirationMillis)
	}
}
