// Package casegraph builds a case's fund-flow graph from the index and
// renders it as a PNG for image-capable notify channels. The API /api/graph
// handler shares the same walk.
package casegraph

import (
	"context"
	"errors"
	"strings"

	"github.com/jsvisa/bitracer/internal/btc"
	"github.com/jsvisa/bitracer/internal/store"
)

type Node struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	Label    string  `json:"label"`
	Value    float64 `json:"value_btc"`
	Watched  bool    `json:"watched"`
	CEX      bool    `json:"cex"`
	CexName  string  `json:"cex_name,omitempty"`
	Terminal string  `json:"terminal,omitempty"`
}

type Edge struct {
	ID     string  `json:"id"`
	Source string  `json:"source"`
	Target string  `json:"target"`
	Value  float64 `json:"value_btc"`
	Txid   string  `json:"txid"`
	Height int64   `json:"height"`
	Time   int64   `json:"time,omitempty"`
}

type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
	Txids []string `json:"txids"`
}

// RawTxSource is the bitcoind subset the live fallback needs; nil skips it.
type RawTxSource interface {
	RawTx(ctx context.Context, txid string) (*btc.Tx, error)
}

// rowCap bounds the whole walk (same cap the API walk has always used).
const rowCap = 20000

// Build walks the index forward from roots (seed txids) up to depth hops,
// pruning outputs below minSats. When no root was indexed and rpc is
// non-nil, it falls back to a live single-tx view per root.
func Build(ctx context.Context, st *store.Store, rpc RawTxSource, roots []string, depth int, minSats int64) (*Graph, error) {
	nodes := map[string]*Node{}
	edges := map[string]*Edge{}
	// attached tracks txids whose outputs are already merged into nodes/edges,
	// so a tx is never counted twice (attachOutputs at level N vs loadOutpoints
	// at level N+1).
	attached := map[string]bool{}
	total := 0
	indexed := 0
	curTxids := roots
	visitedTx := map[string]bool{}

	for d := 0; d <= depth && len(curTxids) > 0 && total < rowCap; d++ {
		spentTxids, spentVouts, n, ok := loadOutpoints(ctx, st, curTxids, nodes, edges, attached, minSats)
		if !ok {
			return nil, errGraphQuery
		}
		indexed += n
		total += len(spentTxids)
		if total >= rowCap {
			break
		}
		edgeRows, err := st.SpendersOf(ctx, spentTxids, spentVouts, rowCap-total)
		if err != nil {
			return nil, err
		}
		next := []string{}
		spenderSet := map[string]bool{}
		for _, e := range edgeRows {
			aNode := "a:" + addrKey(e.Address)
			tNode := "t:" + e.Spender
			eid := aNode + "->" + tNode
			// one address can feed a spender via several outpoints; aggregate
			if prev, ok := edges[eid]; ok {
				prev.Value += btc.SatsToBTC(e.ValueSats)
			} else {
				edges[eid] = &Edge{ID: eid, Source: aNode, Target: tNode, Value: btc.SatsToBTC(e.ValueSats), Txid: e.Spender, Height: e.Height}
			}
			if !spenderSet[e.Spender] && !visitedTx[e.Spender] {
				spenderSet[e.Spender] = true
				next = append(next, e.Spender)
			}
		}
		for _, t := range curTxids {
			visitedTx[t] = true
		}
		if len(next) > 0 {
			if err := attachOutputs(ctx, st, next, nodes, edges, attached, minSats); err != nil {
				return nil, err
			}
		}
		curTxids = next
	}

	// only fall back to the live RPC view when none of the roots were indexed;
	// an indexed-but-fully-pruned graph must stay empty.
	if indexed == 0 && rpc != nil {
		for _, txid := range roots {
			if err := liveTxGraph(ctx, rpc, txid, nodes, minSats); err != nil {
				return nil, err
			}
		}
	}
	if err := markCEX(ctx, st, nodes); err != nil {
		return nil, err
	}
	g := &Graph{
		Nodes: make([]Node, 0, len(nodes)),
		Edges: make([]Edge, 0, len(edges)),
		Txids: roots,
	}
	for _, n := range nodes {
		g.Nodes = append(g.Nodes, *n)
	}
	for _, e := range edges {
		g.Edges = append(g.Edges, *e)
	}
	if len(g.Edges) > 0 {
		seen := map[string]bool{}
		txset := make([]string, 0, len(g.Edges))
		for _, e := range g.Edges {
			if !seen[e.Txid] {
				seen[e.Txid] = true
				txset = append(txset, e.Txid)
			}
		}
		times, err := st.TxTimes(ctx, txset)
		if err != nil {
			return nil, err
		}
		for i := range g.Edges {
			if t, ok := times[g.Edges[i].Txid]; ok {
				g.Edges[i].Time = t.Ts
				if g.Edges[i].Height == 0 {
					g.Edges[i].Height = t.Height
				}
			}
		}
	}
	return g, nil
}

var errGraphQuery = errors.New("graph query failed")

func addrKey(addr string) string {
	if addr == "" {
		return "OP_RETURN"
	}
	return addr
}

func loadOutpoints(ctx context.Context, st *store.Store, txids []string, nodes map[string]*Node, edges map[string]*Edge, attached map[string]bool, minSats int64) ([]string, []int32, int, bool) {
	outs, err := st.OutputsForTxids(ctx, txids)
	if err != nil {
		return nil, nil, 0, false
	}
	spentTxids := make([]string, 0, len(outs))
	spentVouts := make([]int32, 0, len(outs))
	for _, o := range outs {
		// below the case threshold: prune the output and its downstream,
		// mirroring the case walker
		if minSats > 0 && o.ValueSats < minSats {
			continue
		}
		spentTxids = append(spentTxids, o.Txid)
		spentVouts = append(spentVouts, o.Vout)
		if !attached[o.Txid] {
			txNode := "t:" + o.Txid
			nodes[txNode] = &Node{ID: txNode, Type: "tx", Label: shortTxid(o.Txid)}
			aNode := "a:" + addrKey(o.Address)
			eid := txNode + "->" + aNode
			if prev, ok := edges[eid]; ok {
				prev.Value += btc.SatsToBTC(o.ValueSats)
			} else {
				edges[eid] = &Edge{ID: eid, Source: txNode, Target: aNode, Value: btc.SatsToBTC(o.ValueSats), Txid: o.Txid}
			}
			if n, ok := nodes[aNode]; ok {
				n.Value += btc.SatsToBTC(o.ValueSats)
			} else {
				nodes[aNode] = &Node{ID: aNode, Type: "address", Label: shortAddr(addrKey(o.Address)), Value: btc.SatsToBTC(o.ValueSats)}
			}
		}
	}
	for _, o := range outs {
		attached[o.Txid] = true
	}
	return spentTxids, spentVouts, len(outs), true
}

func attachOutputs(ctx context.Context, st *store.Store, txids []string, nodes map[string]*Node, edges map[string]*Edge, attached map[string]bool, minSats int64) error {
	outs, err := st.OutputsForTxids(ctx, txids)
	if err != nil {
		return err
	}
	for _, o := range outs {
		if !attached[o.Txid] {
			if minSats > 0 && o.ValueSats < minSats {
				continue
			}
			txNode := "t:" + o.Txid
			if _, ok := nodes[txNode]; !ok {
				nodes[txNode] = &Node{ID: txNode, Type: "tx", Label: shortTxid(o.Txid)}
			}
			aNode := "a:" + addrKey(o.Address)
			eid := txNode + "->" + aNode
			if prev, ok := edges[eid]; ok {
				prev.Value += btc.SatsToBTC(o.ValueSats)
			} else {
				edges[eid] = &Edge{ID: eid, Source: txNode, Target: aNode, Value: btc.SatsToBTC(o.ValueSats), Txid: o.Txid}
			}
			if n, ok := nodes[aNode]; ok {
				n.Value += btc.SatsToBTC(o.ValueSats)
			} else {
				nodes[aNode] = &Node{ID: aNode, Type: "address", Label: shortAddr(addrKey(o.Address)), Value: btc.SatsToBTC(o.ValueSats)}
			}
		}
	}
	for _, o := range outs {
		attached[o.Txid] = true
	}
	return nil
}

func markCEX(ctx context.Context, st *store.Store, nodes map[string]*Node) error {
	for id, n := range nodes {
		if n.Type != "address" || n.Label == "OP_RETURN" {
			continue
		}
		addr := strings.TrimPrefix(id, "a:")
		info, err := st.GetAddress(ctx, addr)
		if err != nil || info == nil {
			continue
		}
		if info.IsCEX {
			n.CEX = true
			n.CexName = info.Label
		}
		if info.IsTerminal {
			n.Terminal = info.TerminalKind
			if n.CexName == "" {
				n.CexName = info.Label
			}
		}
	}
	return nil
}

func liveTxGraph(ctx context.Context, rpc RawTxSource, txid string, nodes map[string]*Node, minSats int64) error {
	tx, err := rpc.RawTx(ctx, txid)
	if err != nil {
		return nil
	}
	tNode := "t:" + txid
	nodes[tNode] = &Node{ID: tNode, Type: "tx", Label: shortTxid(txid)}
	for _, vout := range tx.Vout {
		if minSats > 0 && btc.Sats(vout.Value) < minSats {
			continue
		}
		addr := addrKey(vout.ScriptPubKey.Address)
		aNode := "a:" + addr
		if n, ok := nodes[aNode]; ok {
			n.Value += btc.SatsToBTC(btc.Sats(vout.Value))
			continue
		}
		nodes[aNode] = &Node{ID: aNode, Type: "address", Label: shortAddr(addr), Value: btc.SatsToBTC(btc.Sats(vout.Value))}
	}
	return nil
}

func shortTxid(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:10] + "…"
}

func shortAddr(s string) string {
	if len(s) <= 14 {
		return s
	}
	if strings.HasPrefix(s, "bc1") {
		return s[:10] + "…" + s[len(s)-4:]
	}
	return s[:12] + "…"
}
