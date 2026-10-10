// Package casegraph builds a case's fund-flow graph from the index and
// renders it as a PNG for image-capable notify channels. The API /api/graph
// handler shares the same walk.
package casegraph

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

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

// PublicURL and ChromeBin are set once at startup (BITRACER_PUBLIC_URL /
// BITRACER_CHROME). When both resolve, CaseGraphPNG snapshots the real
// dashboard graph view in headless Chrome; the pure-Go renderer is the
// fallback.
var (
	PublicURL string
	ChromeBin string
)

// CaseGraphPNG renders the case's current fund-flow graph for notify
// messages (bounded depth so the chart stays readable); any failure
// returns nil and the notification degrades to text-only.
func CaseGraphPNG(ctx context.Context, st *store.Store, caseID int64) []byte {
	if PublicURL != "" {
		if png, err := SnapshotPNG(ctx, caseID); err != nil {
			slog.Warn("dashboard snapshot failed; rendering from index", "case", caseID, "err", err)
		} else if len(png) > 0 {
			return png
		}
	}
	return renderCasePNG(ctx, st, caseID)
}

// snapshotCtxTimeout bounds one headless-Chrome screenshot.
const snapshotCtxTimeout = 20 * time.Second

// SnapshotPNG screenshots the dashboard's graph tab for the case in
// headless Chrome (same dagre layout the web app shows).
func SnapshotPNG(ctx context.Context, caseID int64) ([]byte, error) {
	chrome, err := chromePath()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bitracer-shot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "shot.png")
	url := strings.TrimRight(PublicURL, "/") + "/#case=" + strconv.FormatInt(caseID, 10) + "&tab=graph&embed=1"
	base := []string{
		"--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage", "--hide-scrollbars",
		"--user-data-dir=" + filepath.Join(dir, "profile"),
		"--window-size=1500,850",
		"--force-device-scale-factor=2",
		// fast-forward the dashboard's data fetches + render before shooting
		"--virtual-time-budget=10000",
		"--screenshot=" + out,
		url,
	}
	ctx, cancel := context.WithTimeout(ctx, snapshotCtxTimeout)
	defer cancel()
	// newer Chrome uses --headless=new; fall back for older binaries
	err = exec.CommandContext(ctx, chrome, append([]string{"--headless=new"}, base...)...).Run()
	if err != nil {
		err = exec.CommandContext(ctx, chrome, append([]string{"--headless"}, base...)...).Run()
		if err != nil {
			return nil, err
		}
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	// chrome writes an empty/garbage file when it exits early
	if len(b) < 100 || b[0] != 0x89 || b[1] != 'P' {
		return nil, errors.New("screenshot did not produce a PNG")
	}
	return b, nil
}

var (
	chromeOnce sync.Once
	chromeFound string
)

func chromePath() (string, error) {
	chromeOnce.Do(func() {
		candidates := []string{ChromeBin}
		if runtime.GOOS == "darwin" {
			candidates = append(candidates,
				"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
				"/Applications/Chromium.app/Contents/MacOS/Chromium",
			)
		}
		candidates = append(candidates,
			"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
		)
		for _, c := range candidates {
			if c == "" {
				continue
			}
			if p, err := exec.LookPath(c); err == nil {
				chromeFound = p
				return
			}
		}
	})
	if chromeFound == "" {
		return "", errors.New("no chrome/chromium binary found (set BITRACER_CHROME)")
	}
	return chromeFound, nil
}

// renderCasePNG renders the case's current fund-flow graph straight from
// the index with the pure-Go renderer.
func renderCasePNG(ctx context.Context, st *store.Store, caseID int64) []byte {
	c, err := st.GetCase(ctx, caseID)
	if err != nil {
		return nil
	}
	txs, err := st.ListCaseTxs(ctx, caseID)
	if err != nil || len(txs) == 0 {
		return nil
	}
	roots := make([]string, 0, len(txs))
	for _, t := range txs {
		roots = append(roots, t.Txid)
	}
	var minSats int64
	if c.MinSats != nil {
		minSats = *c.MinSats
	}
	g, err := Build(ctx, st, nil, roots, caseImageDepth, minSats)
	if err != nil {
		return nil
	}
	png, err := Render(g)
	if err != nil {
		return nil
	}
	return png
}

// caseImageDepth bounds the walk used for the notify image so it stays
// readable regardless of the case's tracking depth cap.
const caseImageDepth = 4
