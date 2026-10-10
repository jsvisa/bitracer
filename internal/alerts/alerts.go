package alerts

import (
	"context"
	"log/slog"

	"github.com/jsvisa/bitracer/internal/casegraph"
	"github.com/jsvisa/bitracer/internal/notify"
	"github.com/jsvisa/bitracer/internal/store"
)

// graphImageDepth bounds the walk used for the notify image so the chart
// stays readable regardless of the case's tracking depth cap.
const graphImageDepth = 4

func NotifyCase(ctx context.Context, st *store.Store, caseID int64, msg notify.Message) {
	chs, err := st.ListCaseChannels(ctx, caseID)
	if err != nil {
		slog.Error("load channels failed", "case", caseID, "err", err)
		return
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
	// image-capable channels also get the case's current fund-flow graph;
	// a failed build/render degrades to a text-only notification
	if len(msg.PNG) == 0 {
		msg.PNG = caseGraphPNG(ctx, st, caseID)
	}
	notify.SendAll(ctx, ns, msg)
}

// caseGraphPNG renders the case's current fund-flow graph for notify
// messages; any failure returns nil (text-only notification).
func caseGraphPNG(ctx context.Context, st *store.Store, caseID int64) []byte {
	c, err := st.GetCase(ctx, caseID)
	if err != nil {
		slog.Error("case graph: load case failed", "case", caseID, "err", err)
		return nil
	}
	txs, err := st.ListCaseTxs(ctx, caseID)
	if err != nil {
		slog.Error("case graph: load txs failed", "case", caseID, "err", err)
		return nil
	}
	if len(txs) == 0 {
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
	g, err := casegraph.Build(ctx, st, nil, roots, graphImageDepth, minSats)
	if err != nil {
		slog.Error("case graph: build failed", "case", caseID, "err", err)
		return nil
	}
	png, err := casegraph.Render(g)
	if err != nil {
		slog.Error("case graph: render failed", "case", caseID, "err", err)
		return nil
	}
	return png
}

func Emit(ctx context.Context, st *store.Store, a store.Alert, msg notify.Message) error {
	a.Message = msg.Plain()
	if err := st.AddAlert(ctx, a); err != nil {
		return err
	}
	NotifyCase(ctx, st, a.CaseID, msg)
	return nil
}
