package etl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jsvisa/bitracer/internal/alerts"
	"github.com/jsvisa/bitracer/internal/btc"
	"github.com/jsvisa/bitracer/internal/config"
	"github.com/jsvisa/bitracer/internal/labeler"
	"github.com/jsvisa/bitracer/internal/notify"
	"github.com/jsvisa/bitracer/internal/store"
)

type ETL struct {
	st         *store.Store
	rpc        *btc.Client
	cfg        config.Config
	startBlock int64
	minSats    int64

	mu        sync.Mutex
	backfills map[int64]bool
	seedSats  map[int64]int64
}

func New(st *store.Store, rpc *btc.Client, cfg config.Config, startBlock int64, minSats int64) *ETL {
	return &ETL{st: st, rpc: rpc, cfg: cfg, startBlock: startBlock, minSats: minSats, backfills: map[int64]bool{}, seedSats: map[int64]int64{}}
}

func (e *ETL) Run(ctx context.Context) error {
	if err := e.st.SetDefaultMinSats(ctx, e.minSats); err != nil {
		return err
	}
	if n, err := e.st.PruneBelow(ctx, e.minSats); err != nil {
		return err
	} else if n > 0 {
		slog.Info("pruned low-value index rows", "rows", n, "min_sats", e.minSats)
	}
	syncTick := time.NewTicker(e.cfg.SyncInterval)
	defer syncTick.Stop()
	seedTick := time.NewTicker(10 * time.Second)
	defer seedTick.Stop()

	if err := e.SyncBlocks(ctx); err != nil {
		slog.Error("initial sync failed", "err", err)
	}
	if err := e.ResumeBackfills(ctx); err != nil {
		slog.Error("backfill resume failed", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-syncTick.C:
			if err := e.SyncBlocks(ctx); err != nil {
				slog.Error("block sync failed", "err", err)
			}
		case <-seedTick.C:
			if err := e.SeedPendingCases(ctx); err != nil {
				slog.Error("case seeding failed", "err", err)
			}
			if err := e.ResumeBackfills(ctx); err != nil {
				slog.Error("backfill resume failed", "err", err)
			}
		}
	}
}

var errReorgParent = errors.New("parent hash mismatch")

func (e *ETL) SyncBlocks(ctx context.Context) error {
	info, err := e.rpc.Info(ctx)
	if err != nil {
		return err
	}
	last, err := e.st.LastHeight(ctx)
	if err != nil {
		return err
	}
	start := last + 1
	if e.startBlock > start {
		start = e.startBlock
	}
	for h := start; h <= info.Blocks; {
		hash, err := e.rpc.BlockHash(ctx, h)
		if err != nil {
			return err
		}
		stored, err := e.st.BlockHash(ctx, h)
		if err != nil {
			return err
		}
		if stored != "" && stored != hash {
			slog.Warn("reorg detected: stored hash mismatch, resetting", "height", h)
			if err := e.st.ResetFromHeight(ctx, h); err != nil {
				return err
			}
		}
		if err := e.processBlock(ctx, h, hash); err != nil {
			if errors.Is(err, errReorgParent) {
				if h < 2 {
					return fmt.Errorf("parent mismatch at genesis-adjacent height %d", h)
				}
				slog.Warn("reorg detected: parent mismatch, unwinding", "height", h)
				if err := e.st.ResetFromHeight(ctx, h-1); err != nil {
					return err
				}
				if err := e.st.SetLastHeight(ctx, h-2); err != nil {
					return err
				}
				h--
				continue
			}
			return fmt.Errorf("block %d: %w", h, err)
		}
		if err := e.st.SetLastHeight(ctx, h); err != nil {
			return err
		}
		if h%1000 == 0 {
			slog.Info("synced", "height", h)
		}
		h++
	}
	return nil
}

func (e *ETL) processBlock(ctx context.Context, height int64, hash string) error {
	blk, err := e.rpc.Block(ctx, hash)
	if err != nil {
		return err
	}
	if blk.PrevHash != "" {
		prevStored, err := e.st.BlockHash(ctx, height-1)
		if err != nil {
			return err
		}
		if prevStored != "" && prevStored != blk.PrevHash {
			return fmt.Errorf("%w at height %d", errReorgParent, height)
		}
	}
	txRows := make([]store.TxRow, 0, len(blk.Txs))
	outRows := make([]store.OutRow, 0, len(blk.Txs)*3)
	inRows := make([]store.InRow, 0, len(blk.Txs)*2)
	blockOuts := map[string][]store.IndexedOut{}
	spenderOf := map[string]string{}

	for _, tx := range blk.Txs {
		txRows = append(txRows, store.TxRow{Txid: tx.Txid, Height: height, Ts: blk.Time})
		outs := make([]store.IndexedOut, 0, len(tx.Vout))
		for _, vout := range tx.Vout {
			v := btc.Sats(vout.Value)
			// below the configured minimum: not indexed, keeps the DB lean
			if e.minSats > 0 && v < e.minSats {
				continue
			}
			outRows = append(outRows, store.OutRow{Txid: tx.Txid, Vout: int32(vout.N), Address: vout.ScriptPubKey.Address, ValueSats: v})
			outs = append(outs, store.IndexedOut{Txid: tx.Txid, Vout: int32(vout.N), Address: vout.ScriptPubKey.Address, ValueSats: v})
		}
		blockOuts[tx.Txid] = outs
		for _, vin := range tx.Vin {
			if vin.Txid == "" {
				continue
			}
			inRows = append(inRows, store.InRow{Txid: tx.Txid, Vin: int32(idxOf(tx, vin.Txid, vin.Vout)), SpentTxid: vin.Txid, SpentVout: int32(vin.Vout), Height: height})
			spenderOf[vin.Txid+":"+strconv.Itoa(int(vin.Vout))] = tx.Txid
		}
	}

	if err := e.st.InsertBlock(ctx, height, hash, blk.Time); err != nil {
		return err
	}
	if err := e.st.InsertTxs(ctx, txRows); err != nil {
		return err
	}
	if err := e.st.InsertOutputs(ctx, outRows); err != nil {
		return err
	}
	if err := e.st.InsertInputs(ctx, inRows); err != nil {
		return err
	}

	for i := 0; i < len(inRows); i += 500 {
		end := i + 500
		if end > len(inRows) {
			end = len(inRows)
		}
		chunk := inRows[i:end]
		spentTxids := make([]string, len(chunk))
		spentVouts := make([]int32, len(chunk))
		for j, r := range chunk {
			spentTxids[j], spentVouts[j] = r.SpentTxid, r.SpentVout
		}
		matches, err := e.st.MatchWatchedInputs(ctx, spentTxids, spentVouts)
		if err != nil {
			return err
		}
		for _, m := range matches {
			spender, ok := spenderOf[m.Txid+":"+strconv.Itoa(int(m.Vout))]
			if !ok {
				continue
			}
			marked, err := e.st.MarkWatchedSpent(ctx, m.CaseID, m.Txid, m.Vout, spender, height)
			if err != nil {
				return err
			}
			if !marked {
				continue
			}
			outs := blockOuts[spender]
			if outs == nil {
				fetched, err := e.st.OutputsForTxids(ctx, []string{spender})
				if err != nil {
					return err
				}
				outs = fetched
			}
			if err := e.afterSpend(ctx, m, spender, outs, height, "spend"); err != nil {
				return err
			}
		}
	}
	return nil
}

func idxOf(tx *btc.Tx, txid string, vout uint32) uint32 {
	for i, vin := range tx.Vin {
		if vin.Txid == txid && vin.Vout == vout {
			return uint32(i)
		}
	}
	return 0
}

func (e *ETL) afterSpend(ctx context.Context, m store.WatchedMatch, spenderTxid string, outs []store.IndexedOut, height int64, kind string) error {
	if err := e.addWatchedFromTx(ctx, m.CaseID, m.MinSats, m.DepthCap, m.BranchCap, spenderTxid, outs, m.Depth+1, height); err != nil {
		return err
	}
	if e.isFanout(outs) {
		if err := e.markFanout(ctx, m, spenderTxid); err != nil {
			return err
		}
	}
	msg := notify.Message{
		Kind:      kind,
		CaseID:    m.CaseID,
		Headline: fmt.Sprintf("%.8f BTC moved %s:%d -> spent by %s",
			btc.SatsToBTC(m.ValueSats), m.Txid, m.Vout, spenderTxid),
		Txid:      spenderTxid,
		Address:   m.Address,
		ValueSats: m.ValueSats,
		Depth:     m.Depth + 1,
		Height:    heightLabel(height),
	}
	return alerts.Emit(ctx, e.st, store.Alert{
		CaseID: m.CaseID, Txid: spenderTxid, Address: m.Address,
		ValueSats: m.ValueSats, Depth: m.Depth + 1, Kind: kind,
	}, msg)
}

func (e *ETL) addWatchedFromTx(ctx context.Context, caseID int64, minSats int64, depthCap, branchCap int32, txid string, outs []store.IndexedOut, depth int32, height int64) error {
	if depth > depthCap {
		return nil
	}
	n, err := e.st.CountWatched(ctx, caseID)
	if err != nil {
		return err
	}
	if n >= int64(branchCap) {
		return nil
	}
	floor := int64(0)
	if depth > 0 && e.cfg.DecayPct > 0 {
		seed, err := e.caseSeedSats(ctx, caseID)
		if err != nil {
			return err
		}
		floor = seed * int64(e.cfg.DecayPct) / 100
	}
	rows := make([]store.WatchedRow, 0, len(outs))
	for _, o := range outs {
		if o.ValueSats < minSats {
			continue
		}
		if floor > 0 && o.ValueSats < floor {
			continue
		}
		rows = append(rows, store.WatchedRow{
			CaseID: caseID, Txid: txid, Vout: o.Vout, Address: o.Address,
			ValueSats: o.ValueSats, Depth: depth, Height: height,
		})
	}
	inserted, err := e.st.AddWatched(ctx, rows)
	if err != nil {
		return err
	}
	for _, r := range inserted {
		if r.Address == "" {
			continue
		}
		if err := e.checkTerminal(ctx, caseID, r.Txid, r.Vout, r.Address, r.ValueSats, r.Depth); err != nil {
			return err
		}
	}
	if e.cfg.FaninCount > 0 {
		if err := e.checkFanin(ctx, caseID, inserted); err != nil {
			return err
		}
	}
	return nil
}

// checkFanin stops addresses that many distinct flows converge on — the
// signature of an unlabeled service/exchange sink.
func (e *ETL) checkFanin(ctx context.Context, caseID int64, inserted []store.WatchedRow) error {
	seen := map[string]bool{}
	for _, r := range inserted {
		if r.Address == "" || seen[r.Address] {
			continue
		}
		seen[r.Address] = true
		if info, err := e.st.GetAddress(ctx, r.Address); err == nil && info != nil && info.IsTerminal {
			continue
		}
		parents, err := e.st.WatchedAddressParentCount(ctx, caseID, r.Address)
		if err != nil {
			return err
		}
		if parents < int64(e.cfg.FaninCount) {
			continue
		}
		slog.Info("fan-in convergence: stopping address", "case", caseID, "address", r.Address, "flows", parents)
		if err := e.st.SetAddressTerminal(ctx, r.Address, "fanin"); err != nil {
			return err
		}
		short := r.Address
		if len(short) > 22 {
			short = short[:10] + "…" + short[len(short)-6:]
		}
		labeler.FlipAddressTerminal(ctx, e.st, r.Address, "fanin",
			fmt.Sprintf("%s (%d distinct flows)", short, parents))
	}
	return nil
}

// caseSeedSats caches the largest depth-0 output per case for the decay floor.
func (e *ETL) caseSeedSats(ctx context.Context, caseID int64) (int64, error) {
	e.mu.Lock()
	v, ok := e.seedSats[caseID]
	e.mu.Unlock()
	if ok {
		return v, nil
	}
	v, err := e.st.CaseSeedSats(ctx, caseID)
	if err != nil {
		return 0, err
	}
	e.mu.Lock()
	e.seedSats[caseID] = v
	e.mu.Unlock()
	return v, nil
}

// isFanout reports whether a spender tx's output set looks like a mixer or
// coinjoin: many outputs sharing one exact denomination, or (opt-in) a very
// wide address fan-out. Those outflows are mostly unrelated churn, so the
// walker stops instead of exploding into branches.
func (e *ETL) isFanout(outs []store.IndexedOut) bool {
	if len(outs) == 0 || (e.cfg.FanoutDenom <= 0 && e.cfg.FanoutAddrs <= 0) {
		return false
	}
	addrs := map[string]bool{}
	denoms := map[int64]int{}
	for _, o := range outs {
		if o.Address != "" {
			addrs[o.Address] = true
		}
		denoms[o.ValueSats]++
	}
	if e.cfg.FanoutAddrs > 0 && len(addrs) >= e.cfg.FanoutAddrs {
		return true
	}
	if e.cfg.FanoutDenom > 0 {
		for _, n := range denoms {
			if n >= e.cfg.FanoutDenom {
				return true
			}
		}
	}
	return false
}

func (e *ETL) markFanout(ctx context.Context, m store.WatchedMatch, spenderTxid string) error {
	flipped, err := e.st.SetTxTerminal(ctx, m.CaseID, spenderTxid)
	if err != nil {
		return err
	}
	if flipped == 0 {
		return nil
	}
	msg := notify.Message{
		Kind:      "fanout",
		CaseID:    m.CaseID,
		Headline: fmt.Sprintf("%.8f BTC spent into fan-out tx %s — STOP (suspected mixer/coinjoin payout, not tracked further)",
			btc.SatsToBTC(m.ValueSats), spenderTxid),
		Txid:      spenderTxid,
		Address:   m.Address,
		ValueSats: m.ValueSats,
		Depth:     m.Depth + 1,
	}
	return alerts.Emit(ctx, e.st, store.Alert{
		CaseID: m.CaseID, Txid: spenderTxid, Address: m.Address,
		ValueSats: m.ValueSats, Depth: m.Depth + 1, Kind: "fanout",
	}, msg)
}

func (e *ETL) checkTerminal(ctx context.Context, caseID int64, txid string, vout int32, addr string, sats int64, depth int32) error {
	if addr == "" {
		return nil
	}
	info, err := e.st.GetAddress(ctx, addr)
	if err != nil {
		return err
	}
	if info == nil || !info.IsTerminal {
		return nil
	}
	flipped, err := e.st.SetWatchedTerminal(ctx, caseID, txid, vout)
	if err != nil {
		return err
	}
	if !flipped {
		return nil
	}
	kind := info.TerminalKind
	if kind == "" {
		kind = "cex"
	}
	name := info.Label
	if name == "" {
		name = kind
	}
	msg := notify.Message{
		Kind:      kind,
		CaseID:    caseID,
		Headline: fmt.Sprintf("%.8f BTC reached %s — STOP", btc.SatsToBTC(sats), name),
		Entity:   name,
		Txid:     txid,
		Address:  addr,
		ValueSats: sats,
		Depth:    depth,
	}
	return alerts.Emit(ctx, e.st, store.Alert{
		CaseID: caseID, Txid: txid, Address: addr,
		ValueSats: sats, Depth: depth, Kind: kind,
	}, msg)
}

func (e *ETL) SeedPendingCases(ctx context.Context) error {
	seeds, err := e.st.PendingCaseSeeds(ctx)
	if err != nil {
		return err
	}
	for _, seed := range seeds {
		outs, err := e.st.OutputsForTxids(ctx, []string{seed.Txid})
		if err != nil {
			return err
		}
		height := int64(0)
		if len(outs) == 0 {
			tx, err := e.rpc.RawTx(ctx, seed.Txid)
			if err != nil {
				slog.Error("seed: source tx not found", "txid", seed.Txid, "err", err)
				continue
			}
			height = tx.BlockHeight
			if height == 0 && tx.BlockHash != "" {
				if h, err := e.rpc.BlockHeaderHeight(ctx, tx.BlockHash); err == nil {
					height = h
				}
			}
			for _, vout := range tx.Vout {
				outs = append(outs, store.IndexedOut{Txid: seed.Txid, Vout: int32(vout.N), Address: vout.ScriptPubKey.Address, ValueSats: btc.Sats(vout.Value)})
			}
		} else {
			if h, err := e.st.TxHeight(ctx, seed.Txid); err == nil {
				height = h
			}
		}
		if err := e.addWatchedFromTx(ctx, seed.CaseID, seed.MinSats, seed.DepthCap, seed.BranchCap, seed.Txid, outs, 0, height); err != nil {
			return err
		}
		if err := e.st.SetCaseTxSeeded(ctx, seed.CaseID, seed.Txid); err != nil {
			return err
		}
		msg := notify.Message{
			Kind:     "seed",
			CaseID:   seed.CaseID,
			Headline: "now tracking " + seed.Txid,
			Txid:     seed.Txid,
		}
		if err := alerts.Emit(ctx, e.st, store.Alert{CaseID: seed.CaseID, Txid: seed.Txid, Kind: "seed"}, msg); err != nil {
			return err
		}
		slog.Info("seeded case tx", "case", seed.CaseID, "txid", seed.Txid, "outputs", len(outs))
		if err := e.seedGapBackfill(ctx, seed.CaseID, height); err != nil {
			return err
		}
		if err := e.catchUpCase(ctx, seed.CaseID, seed.MinSats, seed.DepthCap, seed.BranchCap); err != nil {
			return err
		}
	}
	return nil
}

// seedGapBackfill checks whether a freshly seeded tx predates the db's first
// indexed block; if so it persists a case checkpoint and spawns a goroutine
// that backfills the ETL for the gap so spends before the indexed range are
// still traced.
func (e *ETL) seedGapBackfill(ctx context.Context, caseID, height int64) error {
	if height <= 0 {
		return nil
	}
	first, err := e.st.MinBlockHeight(ctx)
	if err != nil {
		return err
	}
	if first <= 0 || height >= first {
		return nil
	}
	if err := e.st.SetCaseBackfill(ctx, caseID, height, first); err != nil {
		return err
	}
	slog.Warn("case seed predates first indexed block, backfilling gap",
		"case", caseID, "from", height, "to", first)
	e.spawnBackfill(ctx, caseID)
	return nil
}

// ResumeBackfills respawns workers for cases whose persisted checkpoint is
// still behind their target (after a restart or a worker error).
func (e *ETL) ResumeBackfills(ctx context.Context) error {
	pending, err := e.st.PendingCaseBackfills(ctx)
	if err != nil {
		return err
	}
	for _, b := range pending {
		e.spawnBackfill(ctx, b.CaseID)
	}
	return nil
}

func (e *ETL) spawnBackfill(ctx context.Context, caseID int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.backfills[caseID] {
		return
	}
	e.backfills[caseID] = true
	go e.runBackfill(ctx, caseID)
}

// runBackfill indexes the case's gap blocks [checkpoint, target) block by
// block. It never touches sync_state.last_height — that cursor tracks the
// forward sync tip, and the gap sits entirely below it.
func (e *ETL) runBackfill(ctx context.Context, caseID int64) {
	defer func() {
		e.mu.Lock()
		delete(e.backfills, caseID)
		e.mu.Unlock()
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		b, err := e.st.CaseBackfill(ctx, caseID)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				slog.Error("backfill: read checkpoint failed", "case", caseID, "err", err)
			}
			return
		}
		if b.Checkpoint < 1 || b.Checkpoint >= b.Target {
			if b.Target > 0 && b.Checkpoint >= b.Target {
				if err := e.st.ClearCaseBackfill(ctx, caseID); err != nil {
					slog.Error("backfill: clear checkpoint failed", "case", caseID, "err", err)
				}
				slog.Info("backfill complete", "case", caseID, "through", b.Target-1)
			}
			return
		}
		h := b.Checkpoint
		hash, err := e.rpc.BlockHash(ctx, h)
		if err != nil {
			slog.Error("backfill: block hash failed", "case", caseID, "height", h, "err", err)
			return
		}
		if stored, err := e.st.BlockHash(ctx, h); err == nil && stored != "" && stored != hash {
			// A reorg under the forward-sync range: never ResetFromHeight here —
			// that would wipe the indexed range above the gap. Overwrite instead;
			// all block inserts are idempotent upserts.
			slog.Warn("backfill: stored hash mismatch, overwriting", "case", caseID, "height", h)
		}
		if err := e.processBlock(ctx, h, hash); err != nil {
			slog.Error("backfill: block failed", "case", caseID, "height", h, "err", err)
			return
		}
		if err := e.st.AdvanceCaseBackfill(ctx, caseID, h+1); err != nil {
			slog.Error("backfill: advance checkpoint failed", "case", caseID, "height", h, "err", err)
			return
		}
		if h%100 == 0 {
			slog.Info("backfill progress", "case", caseID, "height", h, "target", b.Target)
		}
	}
}

func (e *ETL) catchUpCase(ctx context.Context, caseID int64, minSats int64, depthCap, branchCap int32) error {
	for hop := 0; hop < int(depthCap); hop++ {
		rows, err := e.st.WatchingByCase(ctx, caseID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		depthOf := map[string]int32{}
		var txids []string
		var vouts []int32
		seen := map[string]bool{}
		for _, r := range rows {
			key := fmt.Sprintf("%s:%d", r.Txid, r.Vout)
			depthOf[key] = r.Depth
			if seen[key] {
				continue
			}
			seen[key] = true
			txids = append(txids, r.Txid)
			vouts = append(vouts, r.Vout)
		}
		edges, err := e.st.SpendersOf(ctx, txids, vouts, 5000)
		if err != nil {
			return err
		}
		if len(edges) == 0 {
			return nil
		}
		spenderSet := map[string]bool{}
		for _, edge := range edges {
			spenderSet[edge.Spender] = true
		}
		spenderList := make([]string, 0, len(spenderSet))
		for txid := range spenderSet {
			spenderList = append(spenderList, txid)
		}
		spenderOuts, err := e.st.OutputsForTxids(ctx, spenderList)
		if err != nil {
			return err
		}
		outsByTx := map[string][]store.IndexedOut{}
		for _, o := range spenderOuts {
			outsByTx[o.Txid] = append(outsByTx[o.Txid], o)
		}
		progress := false
		for _, edge := range edges {
			m := store.WatchedMatch{
				CaseID: caseID, Txid: edge.SpentTxid, Vout: edge.SpentVout,
				Address: edge.Address, ValueSats: edge.ValueSats,
				Depth:   depthOf[fmt.Sprintf("%s:%d", edge.SpentTxid, edge.SpentVout)],
				MinSats: minSats, DepthCap: depthCap, BranchCap: branchCap,
			}
			marked, err := e.st.MarkWatchedSpent(ctx, caseID, edge.SpentTxid, edge.SpentVout, edge.Spender, edge.Height)
			if err != nil {
				return err
			}
			if !marked {
				continue
			}
			progress = true
			if err := e.afterSpend(ctx, m, edge.Spender, outsByTx[edge.Spender], edge.Height, "spend"); err != nil {
				return err
			}
		}
		if !progress {
			return nil
		}
	}
	return nil
}

func heightLabel(h int64) string {
	if h == 0 {
		return "unconfirmed"
	}
	return "height " + strconv.FormatInt(h, 10)
}
