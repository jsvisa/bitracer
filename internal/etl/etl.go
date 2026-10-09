package etl

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jsvisa/bitracer/internal/btc"
	"github.com/jsvisa/bitracer/internal/config"
	"github.com/jsvisa/bitracer/internal/labels"
	"github.com/jsvisa/bitracer/internal/notify"
	"github.com/jsvisa/bitracer/internal/store"
)

type ETL struct {
	st         *store.Store
	rpc        *btc.Client
	lp         labels.Provider
	cfg        config.Config
	startBlock int64
	minSats    int64
}

func New(st *store.Store, rpc *btc.Client, lp labels.Provider, cfg config.Config, startBlock int64, minSats int64) *ETL {
	return &ETL{st: st, rpc: rpc, lp: lp, cfg: cfg, startBlock: startBlock, minSats: minSats}
}

func (e *ETL) Run(ctx context.Context) error {
	if err := e.st.SetDefaultMinSats(ctx, e.minSats); err != nil {
		return err
	}
	blockDone := make(chan struct{}, 1)
	go func() {
		for {
			if err := e.rpc.WaitForNewBlock(ctx, int(e.cfg.WaitTimeout/time.Second)); err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Warn("waitfornewblock failed", "err", err)
				time.Sleep(5 * time.Second)
			}
			select {
			case blockDone <- struct{}{}:
			default:
			}
		}
	}()
	mempoolTick := time.NewTicker(e.cfg.MempoolEvery)
	defer mempoolTick.Stop()
	seedTick := time.NewTicker(10 * time.Second)
	defer seedTick.Stop()

	if err := e.SyncBlocks(ctx); err != nil {
		slog.Error("initial sync failed", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-blockDone:
			if err := e.SyncBlocks(ctx); err != nil {
				slog.Error("block sync failed", "err", err)
			}
		case <-mempoolTick.C:
			if err := e.PollMempool(ctx); err != nil {
				slog.Error("mempool poll failed", "err", err)
			}
		case <-seedTick.C:
			if err := e.SeedPendingCases(ctx); err != nil {
				slog.Error("case seeding failed", "err", err)
			}
		}
	}
}

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
	for h := start; h <= info.Blocks; h++ {
		hash, err := e.rpc.BlockHash(ctx, h)
		if err != nil {
			return err
		}
		stored, err := e.st.BlockHash(ctx, h)
		if err != nil {
			return err
		}
		if stored != "" && stored != hash {
			slog.Warn("reorg detected, resetting", "height", h)
			if err := e.st.ResetFromHeight(ctx, h); err != nil {
				return err
			}
		}
		if err := e.processBlock(ctx, h, hash); err != nil {
			return fmt.Errorf("block %d: %w", h, err)
		}
		if err := e.st.SetLastHeight(ctx, h); err != nil {
			return err
		}
		if h%1000 == 0 {
			slog.Info("synced", "height", h)
		}
	}
	return nil
}

func (e *ETL) processBlock(ctx context.Context, height int64, hash string) error {
	blk, err := e.rpc.Block(ctx, hash)
	if err != nil {
		return err
	}
	txRows := make([]store.TxRow, 0, len(blk.Txs))
	outRows := make([]store.OutRow, 0, len(blk.Txs)*3)
	inRows := make([]store.InRow, 0, len(blk.Txs)*2)
	blockOuts := map[string][]store.IndexedOut{}
	spenderOf := map[string]string{}
	txids := make([]string, 0, len(blk.Txs))

	for _, tx := range blk.Txs {
		txRows = append(txRows, store.TxRow{Txid: tx.Txid, Height: height, Ts: blk.Time})
		txids = append(txids, tx.Txid)
		outs := make([]store.IndexedOut, 0, len(tx.Vout))
		for _, vout := range tx.Vout {
			outRows = append(outRows, store.OutRow{Txid: tx.Txid, Vout: int32(vout.N), Address: vout.ScriptPubKey.Address, ValueSats: btc.Sats(vout.Value)})
			outs = append(outs, store.IndexedOut{Txid: tx.Txid, Vout: int32(vout.N), Address: vout.ScriptPubKey.Address, ValueSats: btc.Sats(vout.Value)})
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
	if err := e.st.UpdateWatchedHeights(ctx, txids, height); err != nil {
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

func (e *ETL) PollMempool(ctx context.Context) error {
	pending, err := e.st.PendingMempoolSpends(ctx)
	if err != nil {
		return err
	}
	watching, err := e.st.WatchingOutputs(ctx)
	if err != nil {
		return err
	}
	all := append(pending, watching...)
	seen := map[string]bool{}
	var outs []btc.Outpoint
	for _, o := range all {
		key := fmt.Sprintf("%s:%d", o.Txid, o.Vout)
		if seen[key] {
			continue
		}
		seen[key] = true
		outs = append(outs, btc.Outpoint{Txid: o.Txid, Vout: uint32(o.Vout)})
	}
	for i := 0; i < len(outs); i += 1000 {
		end := i + 1000
		if end > len(outs) {
			end = len(outs)
		}
		res, err := e.rpc.SpendingPrevout(ctx, outs[i:end])
		if err != nil {
			return err
		}
		byKey := map[string]*btc.SpentPrevout{}
		for j := range res {
			sp := &res[j]
			byKey[sp.Txid+":"+strconv.Itoa(int(sp.Vout))] = sp
		}
		for _, o := range all {
			key := o.Txid + ":" + strconv.Itoa(int(o.Vout))
			sp := byKey[key]
			if o.SpentHeight != nil && *o.SpentHeight == 0 && (sp == nil || sp.SpendingTxid == nil) {
				if err := e.st.RevertWatchedSpend(ctx, o.CaseID, o.Txid, o.Vout); err != nil {
					return err
				}
				if o.SpentByTxid != nil {
					if err := e.st.DeleteUnconfirmedFrom(ctx, o.CaseID, *o.SpentByTxid); err != nil {
						return err
					}
				}
				slog.Info("mempool spend evicted, reverted", "case", o.CaseID, "outpoint", key)
				continue
			}
			if sp == nil || sp.SpendingTxid == nil || o.Status != "watching" {
				continue
			}
			marked, err := e.st.MarkWatchedSpent(ctx, o.CaseID, o.Txid, o.Vout, *sp.SpendingTxid, 0)
			if err != nil {
				return err
			}
			if !marked {
				continue
			}
			m := store.WatchedMatch{CaseID: o.CaseID, Txid: o.Txid, Vout: o.Vout, Address: o.Address, ValueSats: o.ValueSats, Depth: o.Depth}
			spender, err := e.rpc.RawTx(ctx, *sp.SpendingTxid)
			if err != nil {
				return err
			}
			outs := make([]store.IndexedOut, 0, len(spender.Vout))
			for _, vout := range spender.Vout {
				outs = append(outs, store.IndexedOut{Txid: spender.Txid, Vout: int32(vout.N), Address: vout.ScriptPubKey.Address, ValueSats: btc.Sats(vout.Value)})
			}
			if err := e.afterSpend(ctx, m, spender.Txid, outs, 0, "mempool"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *ETL) channelsFor(ctx context.Context, caseID int64) []notify.Notifier {
	chs, err := e.st.ListChannels(ctx, caseID)
	if err != nil {
		slog.Error("load channels failed", "case", caseID, "err", err)
		return nil
	}
	var ns []notify.Notifier
	for _, ch := range chs {
		n, err := notify.Build(ch.Type, ch.Config)
		if err != nil {
			slog.Error("bad channel config", "case", caseID, "channel", ch.ID, "err", err)
			continue
		}
		ns = append(ns, n)
	}
	return ns
}

func (e *ETL) afterSpend(ctx context.Context, m store.WatchedMatch, spenderTxid string, outs []store.IndexedOut, height int64, kind string) error {
	if err := e.addWatchedFromTx(ctx, m.CaseID, m.MinSats, m.DepthCap, m.BranchCap, spenderTxid, outs, m.Depth+1, height); err != nil {
		return err
	}
	msg := fmt.Sprintf("[bitracer] case#%d: %.8f BTC moved %s:%d -> spent by %s (depth %d, %s)",
		m.CaseID, btc.SatsToBTC(m.ValueSats), short(m.Txid), m.Vout, short(spenderTxid), m.Depth+1, heightLabel(height))
	if err := e.st.AddAlert(ctx, store.Alert{CaseID: m.CaseID, Txid: spenderTxid, Address: m.Address, ValueSats: m.ValueSats, Depth: m.Depth + 1, Kind: kind, Message: msg}); err != nil {
		return err
	}
	notify.SendAll(ctx, e.channelsFor(ctx, m.CaseID), msg)
	return nil
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
	rows := make([]store.WatchedRow, 0, len(outs))
	for _, o := range outs {
		if o.ValueSats < minSats {
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
	return nil
}

func (e *ETL) checkTerminal(ctx context.Context, caseID int64, txid string, vout int32, addr string, sats int64, depth int32) error {
	info, err := e.st.GetAddress(ctx, addr)
	if err != nil {
		return err
	}
	if info == nil {
		lbl, err := e.lp.Lookup(ctx, addr)
		if err != nil {
			slog.Warn("label lookup failed", "address", addr, "err", err)
		}
		isCEX := false
		name := ""
		if lbl != nil {
			isCEX = lbl.IsCEX
			name = lbl.Name
		}
		src := ""
		if lbl != nil {
			src = lbl.Source
		}
		if err := e.st.UpsertAddress(ctx, store.AddressInfo{Address: addr, Label: name, Source: src, IsCEX: isCEX}); err != nil {
			return err
		}
		if !isCEX {
			return nil
		}
		if err := e.st.SetWatchedTerminal(ctx, caseID, txid, vout); err != nil {
			return err
		}
		msg := fmt.Sprintf("[bitracer] case#%d: %.8f BTC reached %s (%s) at %s:%d (depth %d) — STOP",
			caseID, btc.SatsToBTC(sats), name, addr, short(txid), vout, depth)
		if err := e.st.AddAlert(ctx, store.Alert{CaseID: caseID, Txid: txid, Address: addr, ValueSats: sats, Depth: depth, Kind: "cex", Message: msg}); err != nil {
			return err
		}
		notify.SendAll(ctx, e.channelsFor(ctx, caseID), msg)
		return nil
	}
	if info.IsCEX {
		return e.st.SetWatchedTerminal(ctx, caseID, txid, vout)
	}
	return nil
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
		msg := fmt.Sprintf("[bitracer] case#%d: now tracking %s", seed.CaseID, seed.Txid)
		if err := e.st.AddAlert(ctx, store.Alert{CaseID: seed.CaseID, Txid: seed.Txid, Kind: "seed", Message: msg}); err != nil {
			return err
		}
		notify.SendAll(ctx, e.channelsFor(ctx, seed.CaseID), msg)
		slog.Info("seeded case tx", "case", seed.CaseID, "txid", seed.Txid, "outputs", len(outs))
	}
	return nil
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:10] + "…"
}

func heightLabel(h int64) string {
	if h == 0 {
		return "unconfirmed"
	}
	return "height " + strconv.FormatInt(h, 10)
}
