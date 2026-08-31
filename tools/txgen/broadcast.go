package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// runBroadcast streams generate-tx*.csv files through the node's
// /wallet/broadcasttransaction endpoint, throttled to cfg.Broadcast.TpsLimit.
//
// Output:
//   - cfg.Broadcast.TxIDFile      one txID per line for successfully accepted txs
//   - cfg.Broadcast.ReportFile    stress-test report (TPS, on-chain rate, etc.)
//
// Acceptance ≠ on-chain. We capture the first block number visible at
// start + the first new block visible at end, then runStatistic-style
// math computes the actual on-chain TPS over that range.
func runBroadcast(ctx context.Context, cfg *Config) error {
	files, err := ListGeneratedTxFiles(cfg.Broadcast.InputDir)
	if err != nil {
		return err
	}
	log.Printf("broadcast: %d input files", len(files))

	// Block lookups stay on HTTP regardless of the broadcast transport, so
	// the report's on-chain TPS math is measured the same way either way.
	node := NewNodeClient(cfg.Node, 10*time.Second)
	startBlock, err := node.GetNowBlock(ctx)
	if err != nil {
		return fmt.Errorf("fetch start block: %w", err)
	}
	startBlockNum := startBlock.BlockHeader.RawData.Number
	log.Printf("broadcast: start block = %d", startBlockNum)

	tr, err := newTransport(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if err := tr.Close(); err != nil {
			log.Printf("broadcast: close transport: %v", err)
		}
	}()
	log.Printf("broadcast: transport = %s", tr.Describe())

	var txIDFile *os.File
	var txIDLock sync.Mutex
	if cfg.Broadcast.SaveTxID {
		txIDFile, err = os.Create(cfg.Broadcast.TxIDFile)
		if err != nil {
			return fmt.Errorf("open txid file: %w", err)
		}
		defer txIDFile.Close()
	}

	// Pace via a token bucket: refilled every 100ms with tpsLimit/10
	// tokens. Workers consume one token per broadcast. This avoids the
	// per-tx time.After footgun while still smoothing bursts.
	tokens := make(chan struct{}, cfg.Broadcast.TpsLimit)
	stopRefill := make(chan struct{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		perTick := cfg.Broadcast.TpsLimit / 10
		if perTick < 1 {
			perTick = 1
		}
		for {
			select {
			case <-stopRefill:
				return
			case <-ticker.C:
				for i := 0; i < perTick; i++ {
					select {
					case tokens <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	defer close(stopRefill)

	work := make(chan [2]string, 1024)
	var (
		okCount   atomic.Int64
		failCount atomic.Int64
		wg        sync.WaitGroup
	)
	// The transport owns the worker count: for grpc it *is* the
	// per-connection in-flight limit, so it is not the caller's to pick.
	workers := tr.Lanes()
	log.Printf("broadcast: workers=%d tpsLimit=%d", workers, cfg.Broadcast.TpsLimit)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(lane int) {
			defer wg.Done()
			for {
				var item [2]string
				var ok bool
				select {
				case item, ok = <-work:
				case <-ctx.Done():
					return
				}
				if !ok {
					return
				}
				if ctx.Err() != nil {
					return
				}
				select {
				case <-tokens:
				case <-ctx.Done():
					return
				}
				ok, msg := tr.Broadcast(ctx, lane, []byte(item[1]))
				if ok {
					okCount.Add(1)
					if txIDFile != nil {
						txIDLock.Lock()
						_, _ = txIDFile.WriteString(item[0] + "\n")
						txIDLock.Unlock()
					}
				} else {
					failCount.Add(1)
					// Spam-protection: only log the first 20 failures, then
					// every 10000th. This keeps stderr usable while still
					// revealing structural issues (wrong sig, dead node).
					f := failCount.Load()
					if f <= 20 || f%10_000 == 0 {
						log.Printf("broadcast fail #%d: %s", f, truncate(msg, 200))
					}
				}
			}
		}(w)
	}

	startTime := time.Now()
	total := 0
	for _, path := range files {
		n, err := streamCSVContext(ctx, path, work)
		if err != nil {
			log.Printf("broadcast: read %s: %v", path, err)
		}
		total += n
	}
	close(work)
	wg.Wait()
	elapsed := time.Since(startTime)

	endBlock, err := node.GetNowBlock(ctx)
	if err != nil {
		return fmt.Errorf("fetch end block: %w", err)
	}
	endBlockNum := endBlock.BlockHeader.RawData.Number
	log.Printf("broadcast: end block = %d", endBlockNum)

	// On-chain TPS over the actual covered block range.
	stat, err := computeTPS(ctx, node, startBlockNum, endBlockNum)
	if err != nil {
		log.Printf("broadcast: tps calc warning: %v", err)
		stat = &TPSStat{StartBlock: startBlockNum, EndBlock: endBlockNum}
	}

	report := formatReport(reportInput{
		TpsLimit:           cfg.Broadcast.TpsLimit,
		TotalGenerated:     total,
		TotalBroadcastOK:   int(okCount.Load()),
		TotalBroadcastFail: int(failCount.Load()),
		CostTime:           elapsed,
		Stat:               stat,
	})
	if err := os.WriteFile(cfg.Broadcast.ReportFile, []byte(report), 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	log.Printf("broadcast: report → %s", cfg.Broadcast.ReportFile)
	fmt.Println(report)
	return nil
}

func streamCSVContext(ctx context.Context, path string, out chan<- [2]string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	// Use bufio + csv with a large buffer because signed tx JSON rows
	// can easily run >64KB once contracts get fancy.
	br := bufio.NewReaderSize(f, 1<<20)
	r := csv.NewReader(br)
	r.FieldsPerRecord = -1
	if _, err := r.Read(); err != nil {
		return 0, err
	}
	count := 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, err
		}
		if len(rec) < 2 {
			continue
		}
		select {
		case out <- [2]string{rec[0], rec[1]}:
		case <-ctx.Done():
			return count, ctx.Err()
		}
		count++
	}
	return count, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
