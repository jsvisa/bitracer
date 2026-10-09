package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jsvisa/bitracer/internal/btc"
	"github.com/jsvisa/bitracer/internal/notify"
)

type graphNode struct {
	ID      string  `json:"id"`
	Type    string  `json:"type"`
	Label   string  `json:"label"`
	Value   float64 `json:"value_btc"`
	Watched bool    `json:"watched"`
	CEX     bool    `json:"cex"`
	CexName string  `json:"cex_name,omitempty"`
}

type graphEdge struct {
	ID     string  `json:"id"`
	Source string  `json:"source"`
	Target string  `json:"target"`
	Value  float64 `json:"value_btc"`
	Txid   string  `json:"txid"`
	Height int64   `json:"height"`
}

const graphRowCap = 20000

func (s *Server) graph(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	txid := strings.ToLower(q.Get("txid"))
	depth := 6
	if d, err := strconv.Atoi(q.Get("depth")); err == nil && d > 0 && d <= 30 {
		depth = d
	}
	if txid == "" {
		writeErr(w, http.StatusBadRequest, errors.New("txid required"))
		return
	}
	nodes := map[string]*graphNode{}
	edges := map[string]*graphEdge{}
	total := 0
	curTxids := []string{txid}
	visitedTx := map[string]bool{}

	for d := 0; d <= depth && len(curTxids) > 0 && total < graphRowCap; d++ {
		spentTxids, spentVouts, ok := s.loadOutpoints(r.Context(), curTxids, nodes, edges)
		if !ok {
			writeErr(w, http.StatusInternalServerError, errGraphQuery)
			return
		}
		total += len(spentTxids)
		if total >= graphRowCap {
			break
		}
		edgeRows, err := s.st.SpendersOf(r.Context(), spentTxids, spentVouts, graphRowCap-total)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		next := []string{}
		spenderSet := map[string]bool{}
		for _, e := range edgeRows {
			aNode := "a:" + addrKey(e.Address)
			tNode := "t:" + e.Spender
			eid := aNode + "->" + tNode
			edges[eid] = &graphEdge{ID: eid, Source: aNode, Target: tNode, Value: btc.SatsToBTC(e.ValueSats), Txid: e.Spender, Height: e.Height}
			if !spenderSet[e.Spender] && !visitedTx[e.Spender] {
				spenderSet[e.Spender] = true
				next = append(next, e.Spender)
			}
		}
		for _, t := range curTxids {
			visitedTx[t] = true
		}
		if len(next) > 0 {
			if err := s.attachOutputs(r.Context(), next, nodes, edges); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		curTxids = next
	}

	if len(nodes) == 0 {
		if err := s.liveTxGraph(r.Context(), txid, nodes); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := s.markCEX(r.Context(), nodes); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	nl := make([]graphNode, 0, len(nodes))
	for _, n := range nodes {
		nl = append(nl, *n)
	}
	el := make([]graphEdge, 0, len(edges))
	for _, e := range edges {
		el = append(el, *e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nl, "edges": el, "txid": txid})
}

var errGraphQuery = errors.New("graph query failed")

func addrKey(addr string) string {
	if addr == "" {
		return "OP_RETURN"
	}
	return addr
}

func (s *Server) loadOutpoints(ctx context.Context, txids []string, nodes map[string]*graphNode, edges map[string]*graphEdge) ([]string, []int32, bool) {
	outs, err := s.st.OutputsForTxids(ctx, txids)
	if err != nil {
		return nil, nil, false
	}
	spentTxids := make([]string, 0, len(outs))
	spentVouts := make([]int32, 0, len(outs))
	for _, o := range outs {
		spentTxids = append(spentTxids, o.Txid)
		spentVouts = append(spentVouts, o.Vout)
		txNode := "t:" + o.Txid
		nodes[txNode] = &graphNode{ID: txNode, Type: "tx", Label: shortTxid(o.Txid)}
		aNode := "a:" + addrKey(o.Address)
		eid := txNode + "->" + aNode
		if prev, ok := edges[eid]; ok {
			prev.Value += btc.SatsToBTC(o.ValueSats)
		} else {
			edges[eid] = &graphEdge{ID: eid, Source: txNode, Target: aNode, Value: btc.SatsToBTC(o.ValueSats), Txid: o.Txid}
		}
		if n, ok := nodes[aNode]; ok {
			n.Value += btc.SatsToBTC(o.ValueSats)
		} else {
			nodes[aNode] = &graphNode{ID: aNode, Type: "address", Label: shortAddr(addrKey(o.Address)), Value: btc.SatsToBTC(o.ValueSats)}
		}
	}
	return spentTxids, spentVouts, true
}

func (s *Server) attachOutputs(ctx context.Context, txids []string, nodes map[string]*graphNode, edges map[string]*graphEdge) error {
	outs, err := s.st.OutputsForTxids(ctx, txids)
	if err != nil {
		return err
	}
	for _, o := range outs {
		txNode := "t:" + o.Txid
		if _, ok := nodes[txNode]; !ok {
			nodes[txNode] = &graphNode{ID: txNode, Type: "tx", Label: shortTxid(o.Txid)}
		}
		aNode := "a:" + addrKey(o.Address)
		eid := txNode + "->" + aNode
		if prev, ok := edges[eid]; ok {
			prev.Value += btc.SatsToBTC(o.ValueSats)
		} else {
			edges[eid] = &graphEdge{ID: eid, Source: txNode, Target: aNode, Value: btc.SatsToBTC(o.ValueSats), Txid: o.Txid}
		}
		if n, ok := nodes[aNode]; ok {
			n.Value += btc.SatsToBTC(o.ValueSats)
		} else {
			nodes[aNode] = &graphNode{ID: aNode, Type: "address", Label: shortAddr(addrKey(o.Address)), Value: btc.SatsToBTC(o.ValueSats)}
		}
	}
	return nil
}

func (s *Server) markCEX(ctx context.Context, nodes map[string]*graphNode) error {
	for id, n := range nodes {
		if n.Type != "address" || n.Label == "OP_RETURN" {
			continue
		}
		addr := strings.TrimPrefix(id, "a:")
		if info, err := s.st.GetAddress(ctx, addr); err == nil && info != nil && info.IsCEX {
			n.CEX = true
			n.CexName = info.Label
		}
	}
	return nil
}

func (s *Server) liveTxGraph(ctx context.Context, txid string, nodes map[string]*graphNode) error {
	tx, err := s.rpc.RawTx(ctx, txid)
	if err != nil {
		return nil
	}
	tNode := "t:" + txid
	nodes[tNode] = &graphNode{ID: tNode, Type: "tx", Label: shortTxid(txid)}
	for _, vout := range tx.Vout {
		addr := addrKey(vout.ScriptPubKey.Address)
		aNode := "a:" + addr
		if n, ok := nodes[aNode]; ok {
			n.Value += btc.SatsToBTC(btc.Sats(vout.Value))
			continue
		}
		nodes[aNode] = &graphNode{ID: aNode, Type: "address", Label: shortAddr(addr), Value: btc.SatsToBTC(btc.Sats(vout.Value))}
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

func buildNotifier(typ string, cfg json.RawMessage) (notify.Notifier, error) {
	return notify.Build(typ, cfg)
}
