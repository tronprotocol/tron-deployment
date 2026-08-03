package main

import (
	"context"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// runGenerate builds N signed transactions and writes them to CSV files
// under cfg.Generate.OutputDir.
//
// Flow per tx:
//  1. Pick a contract type (TRX / TRC10 / TRC20) according to the
//     configured weighted distribution.
//  2. Pick a receiver address at random from the address list.
//  3. POST to the node's create*-style endpoint → raw_data_hex + txID.
//  4. SignTxID(txID, senderPrivateKey) → 65-byte signature hex.
//  5. AttachSignature(unsignedJSON, sig) → fully signed JSON.
//  6. Append (txID, signedTxJSON) to the current output CSV.
//
// Concurrency: `concurrency` workers pull tasks off a channel. Each
// task is one CSV file's worth of txs. Task size is auto-derived so
// every worker gets ~4 tasks (enough scheduling slack to absorb a slow
// worker), clamped to [1000, 100000] rows so individual files stay
// inspectable in a text editor.
//
// Note on expiration: the node assigns expiration = head_block_time +
// 60s by default for HTTP-built transactions. txgen rewrites raw_data
// before signing so generated transactions use cfg.Generate.ExpirationMillis.
func runGenerate(ctx context.Context, cfg *Config) error {
	// 0700: receivers.csv lands here with one cleartext secp256k1
	// private key per receiver, so the directory must not be traversable
	// by other local uids. The generate-tx-NNNN.csv files keep the
	// default mode — they hold only txIDs and signed transactions, which
	// are public once broadcast — but they inherit this directory's
	// protection anyway.
	if err := os.MkdirAll(cfg.Generate.OutputDir, 0o700); err != nil {
		return err
	}

	addrs, err := buildReceivers(cfg.Generate.ReceiverAddressCount)
	if err != nil {
		return fmt.Errorf("build receivers: %w", err)
	}
	sidecar := filepath.Join(cfg.Generate.OutputDir, "receivers.csv")
	if err := WriteAddressList(sidecar, addrs); err != nil {
		return fmt.Errorf("write receivers sidecar: %w", err)
	}
	log.Printf("generate: built %d receiver addresses → %s",
		len(addrs), filepath.Base(sidecar))

	signers, err := buildSigners(cfg)
	if err != nil {
		return err
	}
	defer signers.Close()

	node := NewNodeClient(cfg.Node, 10*time.Second)

	// Probe the node before spinning workers — fail fast on bad URL etc.
	if _, err := node.GetNowBlock(ctx); err != nil {
		return fmt.Errorf("probe node: %w", err)
	}

	total := cfg.Generate.TotalTxCount
	taskSize := deriveTaskSize(total, cfg.Generate.Concurrency)
	numTasks := (total + taskSize - 1) / taskSize
	log.Printf("generate: %d tx, %d workers, %d tasks of ~%d tx each",
		total, cfg.Generate.Concurrency, numTasks, taskSize)

	var (
		okCount   atomic.Int64
		failCount atomic.Int64
	)

	tasks := make(chan int, numTasks)
	for i := 0; i < numTasks; i++ {
		tasks <- i
	}
	close(tasks)

	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < cfg.Generate.Concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))
			for taskID := range tasks {
				if ctx.Err() != nil {
					return
				}
				remaining := total - taskID*taskSize
				batch := taskSize
				if remaining < batch {
					batch = remaining
				}
				outFile := filepath.Join(cfg.Generate.OutputDir,
					fmt.Sprintf("generate-tx-%04d.csv", taskID))
				ok, fail := generateBatch(ctx, node, cfg, signers, addrs, rng, batch, outFile)
				okCount.Add(int64(ok))
				failCount.Add(int64(fail))
				log.Printf("generate: task %d done (%d ok, %d fail) → %s",
					taskID, ok, fail, filepath.Base(outFile))
			}
		}(w)
	}
	wg.Wait()

	elapsed := time.Since(start)
	log.Printf("generate: done in %s — ok=%d fail=%d",
		elapsed.Round(time.Second), okCount.Load(), failCount.Load())
	return nil
}

// signFunc turns an unsigned tx (its raw JSON + txID) into fully signed
// broadcast JSON. The ECDSA and PQ paths differ only in how the txID is
// signed and which field the result is attached to.
type signFunc func(unsigned *UnsignedTx) ([]byte, error)

// signer pairs a sender address with the function that signs txs sent from
// it. A tx's owner address must match the key that signs it, so the PQ and
// ECDSA paths each carry their own sender.
type signer struct {
	senderHex string
	sign      signFunc
	close     func() // optional cleanup (e.g. liboqs native memory)
}

// signerSet selects a signer per transaction. When pqRatio is in (0,100)
// generation mixes PQ-signed txs (from the PQ-derived sender) with
// ECDSA-signed txs (from the ECDSA sender); pqRatio == 100 means all PQ and
// pqRatio == 0 means all ECDSA.
type signerSet struct {
	ecdsa   *signer // nil when pqRatio == 100
	pq      *signer // nil when PQ is disabled
	pqRatio int     // percent of txs PQ-signed, [0,100]
}

// pick returns the signer for one tx given an independent roll in [0,100).
func (s *signerSet) pick(roll int) *signer {
	if s.pq != nil && roll < s.pqRatio {
		return s.pq
	}
	return s.ecdsa
}

// Close releases any native resources held by the signers (e.g. liboqs
// OQS_SIG contexts). It is safe to call on a zero-value signerSet.
func (s *signerSet) Close() {
	if s == nil {
		return
	}
	if s.ecdsa != nil && s.ecdsa.close != nil {
		s.ecdsa.close()
	}
	if s.pq != nil && s.pq.close != nil {
		s.pq.close()
	}
}

// buildSigners wires up the signer(s) from config. The ECDSA signer is built
// whenever any tx is ECDSA-signed (PQ disabled, or PQ ratio < 100); the PQ
// signer is built when PQ is enabled.
func buildSigners(cfg *Config) (*signerSet, error) {
	pqcfg := cfg.Generate.PQ
	set := &signerSet{}

	if pqcfg.Enabled {
		type pqSigner interface {
			SchemeName() string
			PublicKeyHex() string
			HexAddress() string
			Base58Address() string
			Sign(txIDHex string) ([]byte, error)
			Close()
		}

		var ps pqSigner
		switch pqcfg.Scheme {
		case SchemeMLDSA44:
			s, err := NewPQSigner(pqcfg.Scheme, pqcfg.Seed)
			if err != nil {
				return nil, fmt.Errorf("init pq signer: %w", err)
			}
			ps = s
		case SchemeFNDSA512:
			s, err := NewFalconSigner(pqcfg.PrivateKey, pqcfg.PublicKey)
			if err != nil {
				return nil, fmt.Errorf("init falcon signer: %w", err)
			}
			ps = s
		default:
			return nil, fmt.Errorf("unsupported pq scheme %q", pqcfg.Scheme)
		}

		set.pqRatio = pqcfg.Ratio
		set.pq = &signer{
			senderHex: ps.HexAddress(),
			sign: func(u *UnsignedTx) ([]byte, error) {
				sig, err := ps.Sign(u.TxID)
				if err != nil {
					return nil, err
				}
				return AttachPQSignature(u.Raw, ps.SchemeName(),
					ps.PublicKeyHex(), hex.EncodeToString(sig))
			},
			close: ps.Close,
		}
		log.Printf("generate: pq scheme = %s, ratio = %d%%, pq sender = %s (%s)",
			ps.SchemeName(), set.pqRatio, ps.Base58Address(), ps.HexAddress())
	}

	if !pqcfg.Enabled || set.pqRatio < 100 {
		senderHex, b58, err := AddressFromPrivateKey(cfg.Generate.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("derive sender address: %w", err)
		}
		set.ecdsa = &signer{
			senderHex: senderHex,
			sign: func(u *UnsignedTx) ([]byte, error) {
				sigHex, err := SignTxID(cfg.Generate.PrivateKey, u.TxID)
				if err != nil {
					return nil, err
				}
				return AttachSignature(u.Raw, sigHex)
			},
		}
		log.Printf("generate: ecdsa sender = %s (%s)", b58, senderHex)
	}

	return set, nil
}

// buildReceivers produces n fresh secp256k1 keypairs to use as transfer
// targets. The keys are kept (and dumped to a sidecar CSV) so the
// operator can pre-fund the same addresses via `Toolkit.jar db fork`
// out-of-band if they want to skip the per-receiver activation fee.
func buildReceivers(n int) ([]AddressRow, error) {
	rows := make([]AddressRow, 0, n)
	for i := 0; i < n; i++ {
		priv, hexAddr, b58, err := NewRandomAddress()
		if err != nil {
			return nil, fmt.Errorf("generate receiver %d: %w", i, err)
		}
		rows = append(rows, AddressRow{Base58: b58, HexAddress: hexAddr, PrivateKey: priv})
	}
	return rows, nil
}

// deriveTaskSize splits totalTxCount into chunks sized so each worker
// gets ~4 tasks. Clamped to [1000, 100000] rows: above 100k the CSV
// gets unwieldy to inspect; below 1000 the per-file overhead dominates.
func deriveTaskSize(total, concurrency int) int {
	const (
		minSize = 1000
		maxSize = 100_000
	)
	if concurrency < 1 {
		concurrency = 1
	}
	size := total / (concurrency * 4)
	if size < minSize {
		size = minSize
	}
	if size > maxSize {
		size = maxSize
	}
	if size > total {
		size = total
	}
	return size
}

// generateBatch builds `n` txs and writes them to outFile. Returns
// (ok, fail). A "fail" here is a per-tx error from the node — most
// commonly insufficient balance once the sender's TRX runs out.
func generateBatch(
	ctx context.Context, node *NodeClient, cfg *Config,
	signers *signerSet, addrs []AddressRow, rng *rand.Rand,
	n int, outFile string,
) (int, int) {
	f, err := os.Create(outFile)
	if err != nil {
		log.Printf("generate: open %s: %v", outFile, err)
		return 0, n
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"txID", "signed_tx_json"}); err != nil {
		return 0, n
	}

	tt := cfg.Generate.TxType
	thrTRX := tt.Transfer
	thrTRC10 := tt.Transfer + tt.TransferTRC10

	ok, fail := 0, 0
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			return ok, fail
		}
		receiver := addrs[rng.Intn(len(addrs))].HexAddress
		// Independent rolls: one picks the signer (ECDSA vs PQ), one picks
		// the contract type. The owner of each tx is its signer's sender.
		sgn := signers.pick(rng.Intn(100))
		roll := rng.Intn(100)

		var unsigned *UnsignedTx
		var buildErr error
		switch {
		case roll < thrTRX:
			unsigned, buildErr = node.CreateTRXTransfer(ctx, sgn.senderHex, receiver, cfg.Generate.TransferAmount)
		case roll < thrTRC10:
			unsigned, buildErr = node.CreateTRC10Transfer(ctx, sgn.senderHex, receiver, cfg.Generate.TRC10ID, cfg.Generate.TransferAmount)
		default:
			c20, err := NormalizeAddress(cfg.Generate.TRC20Address)
			if err != nil {
				fail++
				continue
			}
			unsigned, buildErr = node.CreateTRC20Transfer(ctx, sgn.senderHex, c20, receiver, cfg.Generate.TransferAmount, cfg.Generate.TRC20FeeLimit)
		}
		if buildErr != nil {
			fail++
			continue
		}
		if err := unsigned.ExtendExpiration(cfg.Generate.ExpirationMillis); err != nil {
			fail++
			continue
		}

		signed, err := sgn.sign(unsigned)
		if err != nil {
			fail++
			continue
		}
		if err := w.Write([]string{unsigned.TxID, string(signed)}); err != nil {
			fail++
			continue
		}
		ok++
	}
	return ok, fail
}
