package casegraph

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/jsvisa/bitracer/internal/btc"
)

// Dashboard theme colors.
var (
	colBG     = color.RGBA{0x1b, 0x1d, 0x21, 0xff}
	colCard   = color.RGBA{0x24, 0x26, 0x2b, 0xff}
	colBorder = color.RGBA{0x3d, 0x41, 0x48, 0xff}
	colText   = color.RGBA{0xe8, 0xea, 0xed, 0xff}
	colMuted  = color.RGBA{0x9a, 0xa0, 0xa6, 0xff}
	colOrange = color.RGBA{0xf3, 0x8a, 0x2f, 0xff}
	colChip   = color.RGBA{0x3d, 0x41, 0x48, 0xff}
)

const (
	charW   = 7 // basicfont.Face7x13 advance
	padX    = 12
	colGap  = 170
	rowGap  = 22
	margin  = 28
	cornerR = 9
	// maxNodes bounds the rendered flow; the rest collapses into a
	// "+N more" note so alerts stay readable.
	maxNodes = 80
)

type flowNode struct {
	id      string
	addr    bool
	label   string
	cexName string
	value   float64
	hot     bool
}

type flowEdge struct {
	from, to string
	value    float64
	height   int64
}

type txIO struct {
	peer   string
	value  float64
	height int64
}

// collapse folds tx nodes between their address inputs and outputs,
// mirroring the dashboard's GraphView collapse: a tx with both sides
// becomes address->address edges (change self-loops dropped).
func collapse(g *Graph) ([]flowNode, []flowEdge) {
	fn := map[string]*flowNode{}
	var order []string
	txIn := map[string][]txIO{}
	txOut := map[string][]txIO{}
	txSeen := []string{}
	txAdded := map[string]bool{}

	for _, n := range g.Nodes {
		switch n.Type {
		case "address":
			if _, ok := fn[n.ID]; !ok {
				fn[n.ID] = &flowNode{
					id: n.ID, addr: true, label: ascii(n.Label),
					cexName: ascii(n.CexName), value: n.Value,
					hot: n.CEX || n.Watched || n.Terminal != "",
				}
				order = append(order, n.ID)
			}
		case "tx":
			if !txAdded[n.ID] {
				txAdded[n.ID] = true
				txSeen = append(txSeen, n.ID)
			}
		}
	}

	dedupe := func(arr []txIO, io txIO) []txIO {
		for _, x := range arr {
			if x.peer == io.peer {
				return arr
			}
		}
		return append(arr, io)
	}
	for _, e := range g.Edges {
		if strings.HasPrefix(e.Source, "t:") {
			txOut[e.Source] = dedupe(txOut[e.Source], txIO{peer: e.Target, value: e.Value, height: e.Height})
		} else {
			txIn[e.Target] = dedupe(txIn[e.Target], txIO{peer: e.Source, value: e.Value, height: e.Height})
		}
	}
	for tx := range txIn {
		if !txAdded[tx] {
			txAdded[tx] = true
			txSeen = append(txSeen, tx)
		}
	}
	for tx := range txOut {
		if !txAdded[tx] {
			txAdded[tx] = true
			txSeen = append(txSeen, tx)
		}
	}

	rootSet := map[string]bool{}
	for _, t := range g.Txids {
		rootSet["t:"+t] = true
	}
	var fe []flowEdge
	seenEdge := map[string]bool{}
	add := func(e flowEdge) {
		k := e.from + "->" + e.to
		if seenEdge[k] {
			return
		}
		seenEdge[k] = true
		fe = append(fe, e)
	}
	addNode := func(id string, n *flowNode) {
		if _, ok := fn[id]; !ok {
			fn[id] = n
			order = append(order, id)
		}
	}
	for _, tx := range txSeen {
		txid := strings.TrimPrefix(tx, "t:")
		ins := txIn[tx]
		outs := txOut[tx]
		switch {
		case len(ins) == 0:
			// seed tx: keep the tx card as the flow's origin
			addNode(tx, &flowNode{id: tx, label: ascii(btc.ShortTxid(txid)), hot: rootSet[tx]})
			for _, o := range outs {
				add(flowEdge{from: tx, to: o.peer, value: o.value, height: o.height})
			}
		case len(outs) == 0:
			// dead end (all outputs pruned): keep the tx card
			addNode(tx, &flowNode{id: tx, label: ascii(btc.ShortTxid(txid))})
			for _, i := range ins {
				add(flowEdge{from: i.peer, to: tx, value: i.value, height: i.height})
			}
		default:
			for _, i := range ins {
				for _, o := range outs {
					// change returned to the spending address: skip the self-loop
					if o.peer == i.peer {
						continue
					}
					add(flowEdge{from: i.peer, to: o.peer, value: o.value, height: i.height})
				}
			}
		}
	}
	out := make([]flowNode, 0, len(order))
	for _, id := range order {
		out = append(out, *fn[id])
	}
	return out, fe
}

// ascii maps glyphs basicfont cannot render to ASCII equivalents.
func ascii(s string) string {
	return strings.NewReplacer("…", "...", "₿", "B", "·", "-").Replace(s)
}

// rankNodes layers nodes into columns by longest-path rank (flows move
// left to right); within a column bigger amounts sort first. The bounded
// relaxation terminates even on unexpected cycles.
func rankNodes(nodes []flowNode, edges []flowEdge) [][]flowNode {
	rank := map[string]int{}
	for pass := 0; pass < len(nodes); pass++ {
		changed := false
		for _, e := range edges {
			if r := rank[e.from] + 1; r > rank[e.to] {
				rank[e.to] = r
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	byRank := map[int][]flowNode{}
	maxRank := 0
	for _, n := range nodes {
		r := rank[n.id]
		byRank[r] = append(byRank[r], n)
		if r > maxRank {
			maxRank = r
		}
	}
	cols := make([][]flowNode, maxRank+1)
	for r := range cols {
		col := byRank[r]
		sort.SliceStable(col, func(i, j int) bool { return col[i].value > col[j].value })
		cols[r] = col
	}
	return cols
}

func textW(s string) int { return len(s) * charW }

func cardSize(n flowNode) (int, int) {
	if n.addr {
		w := 30 + textW(n.label) + padX
		if w < 130 {
			w = 130
		}
		h := 36
		if n.cexName != "" {
			h = 52
		}
		return w, h
	}
	w := 40 + textW(n.label) + padX
	if w < 120 {
		w = 120
	}
	return w, 28
}

type placed struct {
	n    flowNode
	x, y int // center
	w, h int
}

// layout stacks each rank column top-down and returns positions plus the
// content bounding box (right/bottom edges inclusive of one margin).
func layout(cols [][]flowNode) (map[string]*placed, int, int) {
	pos := map[string]*placed{}
	x := margin
	maxRight, maxBottom := 0, 0
	for _, col := range cols {
		colW := 0
		y := margin
		for i := range col {
			w, h := cardSize(col[i])
			pos[col[i].id] = &placed{n: col[i], x: x + w/2, y: y + h/2, w: w, h: h}
			y += h + rowGap
			if w > colW {
				colW = w
			}
		}
		if y-rowGap > maxBottom {
			maxBottom = y - rowGap
		}
		if x+colW > maxRight {
			maxRight = x + colW
		}
		x += colW + colGap
	}
	return pos, maxRight + margin, maxBottom + margin
}

type canvas struct{ img *image.RGBA }

func (c canvas) fillRoundRect(x, y, w, h, r int, col color.Color) {
	for dy := 0; dy < h; dy++ {
		for dx := 0; dx < w; dx++ {
			px, py := x+dx, y+dy
			if dx < r && dy < r {
				if !inCorner(dx, dy, r) {
					continue
				}
			} else if dx >= w-r && dy < r {
				if !inCorner(w-1-dx, dy, r) {
					continue
				}
			} else if dx < r && dy >= h-r {
				if !inCorner(dx, h-1-dy, r) {
					continue
				}
			} else if dx >= w-r && dy >= h-r {
				if !inCorner(w-1-dx, h-1-dy, r) {
					continue
				}
			}
			c.img.Set(px, py, col)
		}
	}
}

func inCorner(dx, dy, r int) bool {
	ix, iy := r-dx, r-dy
	return ix*ix+iy*iy <= r*r
}

func (c canvas) card(x, y, w, h int, border, fill color.Color) {
	c.fillRoundRect(x, y, w, h, cornerR, border)
	c.fillRoundRect(x+1, y+1, w-2, h-2, cornerR-1, fill)
}

func (c canvas) disc(cx, cy int, r float64, col color.Color) {
	for dy := int(-r) - 1; dy <= int(r)+1; dy++ {
		for dx := int(-r) - 1; dx <= int(r)+1; dx++ {
			if float64(dx*dx+dy*dy) <= r*r {
				c.img.Set(cx+dx, cy+dy, col)
			}
		}
	}
}

func (c canvas) text(x, y int, s string, col color.Color) {
	d := &font.Drawer{Dst: c.img, Src: image.NewUniform(col), Face: basicfont.Face7x13, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

// textC draws s centered on (cx, cy).
func (c canvas) textC(cx, cy int, s string, col color.Color) {
	c.text(cx-textW(s)/2, cy+4, s, col)
}

// bezier strokes a horizontal S-curve (frontend-style control points) and
// returns the midpoint plus the unit direction at the end.
func (c canvas) bezier(x0, y0, x1, y1 float64, col color.Color) (mx, my, ex, ey float64) {
	mx = (x0 + x1) / 2
	pt := func(t float64) (float64, float64) {
		u := 1 - t
		a := u * u * u
		b := 3 * u * u * t
		d := 3 * u * t * t
		e := t * t * t
		return a*x0 + b*mx + d*mx + e*x1, a*y0 + b*y0 + d*y1 + e*y1
	}
	const steps = 48
	var lx, ly float64
	for i := 0; i <= steps; i++ {
		t := float64(i) / steps
		px, py := pt(t)
		c.disc(int(px), int(py), 1.5, col)
		if i == steps-1 {
			lx, ly = px, py
		}
	}
	ex, ey = x1-lx, y1-ly
	if n := math.Hypot(ex, ey); n > 0 {
		ex, ey = ex/n, ey/n
	}
	return mx, (y0 + y1) / 2, ex, ey
}

// arrow fills a triangle pointing along (dx, dy) with its tip at (x, y).
func (c canvas) arrow(x, y, dx, dy float64, col color.Color) {
	const L, W = 10.0, 5.0
	bx, by := x-dx*L, y-dy*L
	px, py := -dy, dx
	c.triangle(x, y, bx+px*W, by+py*W, bx-px*W, by-py*W, col)
}

func (c canvas) triangle(x0, y0, x1, y1, x2, y2 float64, col color.Color) {
	minX := int(math.Min(x0, math.Min(x1, x2))) - 1
	maxX := int(math.Max(x0, math.Max(x1, x2))) + 1
	minY := int(math.Min(y0, math.Min(y1, y2))) - 1
	maxY := int(math.Max(y0, math.Max(y1, y2))) + 1
	sign := func(ax, ay, bx, by, px, py float64) float64 {
		return (bx-ax)*(py-ay) - (by-ay)*(px-ax)
	}
	for py := minY; py <= maxY; py++ {
		for px := minX; px <= maxX; px++ {
			fx, fy := float64(px)+0.5, float64(py)+0.5
			d1 := sign(x0, y0, x1, y1, fx, fy)
			d2 := sign(x1, y1, x2, y2, fx, fy)
			d3 := sign(x2, y2, x0, y0, fx, fy)
			neg := d1 < 0 || d2 < 0 || d3 < 0
			pos := d1 > 0 || d2 > 0 || d3 > 0
			if !neg || !pos {
				c.img.Set(px, py, col)
			}
		}
	}
}

func fmtBTC(v float64) string {
	s := strconv.FormatFloat(v, 'f', 8, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" {
		s = "0"
	}
	return s + " BTC"
}

// Render draws the collapsed flow left-to-right (columns = depth ranks)
// and returns PNG bytes; a graph without renderable nodes returns nil.
func Render(g *Graph) ([]byte, error) {
	nodes, edges := collapse(g)
	if len(nodes) == 0 {
		return nil, nil
	}
	dropped := 0
	if len(nodes) > maxNodes {
		kept := make([]flowNode, 0, maxNodes)
		keptSet := map[string]bool{}
	cols:
		for _, col := range rankNodes(nodes, edges) {
			for _, n := range col {
				if len(kept) == maxNodes {
					break cols
				}
				kept = append(kept, n)
				keptSet[n.id] = true
			}
		}
		dropped = len(nodes) - len(kept)
		filtered := edges[:0]
		for _, e := range edges {
			if keptSet[e.from] && keptSet[e.to] {
				filtered = append(filtered, e)
			}
		}
		edges = filtered
		nodes = kept
	}

	cols := rankNodes(nodes, edges)
	pos, w, h := layout(cols)
	footerH := 26
	if dropped > 0 {
		footerH = 40
	}
	c := canvas{img: image.NewRGBA(image.Rect(0, 0, w, h+footerH))}
	draw.Draw(c.img, c.img.Bounds(), &image.Uniform{colBG}, image.Point{}, draw.Src)

	// occupied holds node cards plus placed edge-label boxes; labels
	// stepping on any of them get nudged vertically instead
	var occupied []image.Rectangle
	for _, p := range pos {
		occupied = append(occupied, image.Rect(p.x-p.w/2-2, p.y-p.h/2-2, p.x+p.w/2+2, p.y+p.h/2+2))
	}
	free := func(r image.Rectangle) bool {
		for _, o := range occupied {
			if r.Overlaps(o) {
				return false
			}
		}
		return true
	}

	type labelDraw struct {
		x, y, w int
		hp, val string
		muted   bool
	}
	var pending []labelDraw

	for _, e := range edges {
		s, t := pos[e.from], pos[e.to]
		if s == nil || t == nil {
			continue
		}
		x0 := float64(s.x + s.w/2)
		y0 := float64(s.y)
		x1 := float64(t.x - t.w/2 - 7)
		y1 := float64(t.y)
		if x1 <= x0+8 {
			continue
		}
		_, _, ex, ey := c.bezier(x0, y0, x1, y1, colOrange)
		c.arrow(x1+7, y1, ex, ey, colOrange)
		hp := ""
		if e.height > 0 {
			hp = "[" + strconv.FormatInt(e.height, 10) + "] "
		}
		val := fmtBTC(e.value)
		totalW := textW(hp) + textW(val)
		lx := int((x0+x1)/2) - totalW/2
		if lx < 4 {
			lx = 4
		}
		pending = append(pending, labelDraw{
			x: lx, y: int((y0+y1)/2) - 18, w: totalW,
			hp: hp, val: val, muted: hp == "",
		})
	}

	for _, p := range pos {
		drawCard(c, p.x-p.w/2, p.y-p.h/2, p.w, p.h, p.n)
	}

	// labels go on top of everything; nudge until on free space
	for i := range pending {
		l := &pending[i]
		box := image.Rect(l.x-3, l.y-2, l.x+l.w+3, l.y+15)
		for _, off := range [6]int{0, -18, 18, -36, 36, -54} {
			box = image.Rect(l.x-3, l.y+off-2, l.x+l.w+3, l.y+off+15)
			if free(box) {
				l.y += off
				break
			}
		}
		occupied = append(occupied, box)
		// dark halo keeps the label readable where curves cross it
		c.fillRoundRect(l.x-3, l.y-2, l.w+6, 17, 4, colBG)
		if l.hp != "" {
			c.text(l.x, l.y, l.hp, colOrange)
			c.text(l.x+textW(l.hp), l.y, l.val, colText)
		} else {
			c.text(l.x, l.y, l.val, colMuted)
		}
	}

	if dropped > 0 {
		c.text(margin, h+18, "+"+strconv.Itoa(dropped)+" more nodes (truncated)", colMuted)
	}
	c.text(w-margin-textW("bitracer"), h+10, "bitracer", colMuted)

	var buf bytes.Buffer
	if err := png.Encode(&buf, c.img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func drawCard(c canvas, x, y, w, h int, n flowNode) {
	border := colBorder
	if n.hot {
		border = colOrange
	}
	c.card(x, y, w, h, border, colCard)
	if n.addr {
		c.disc(x+16, y+h/2, 9, colOrange)
		c.textC(x+16, y+h/2, "B", color.RGBA{0xff, 0xff, 0xff, 0xff})
		if n.cexName != "" {
			c.text(x+30, y+h/2-15, n.label, colText)
			c.text(x+30, y+h/2+3, n.cexName, colOrange)
		} else {
			c.text(x+30, y+h/2-6, n.label, colText)
		}
		return
	}
	c.fillRoundRect(x+8, y+h/2-9, 22, 18, 5, colChip)
	c.textC(x+19, y+h/2, "TX", colMuted)
	c.text(x+38, y+h/2-6, n.label, colText)
}
