package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jsvisa/bitracer/internal/casegraph"
	"github.com/jsvisa/bitracer/internal/notify"
)

func (s *Server) graph(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	depth := 6
	if d, err := strconv.Atoi(q.Get("depth")); err == nil && d > 0 && d <= 30 {
		depth = d
	}
	roots, err := s.graphRoots(r.Context(), q)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var minSats int64
	if m, err := strconv.ParseInt(q.Get("min_sats"), 10, 64); err == nil && m > 0 {
		minSats = m
	}
	g, err := casegraph.Build(r.Context(), s.st, s.rpc, roots, depth, minSats)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// graphRoots resolves the seed txids for a graph walk: either every txhash
// tracked on a case (case_id) or a single explicit txhash (txid).
func (s *Server) graphRoots(ctx context.Context, q url.Values) ([]string, error) {
	if cs := q.Get("case_id"); cs != "" {
		id, err := strconv.ParseInt(cs, 10, 64)
		if err != nil {
			return nil, err
		}
		txs, err := s.st.ListCaseTxs(ctx, id)
		if err != nil {
			return nil, err
		}
		roots := make([]string, 0, len(txs))
		for _, t := range txs {
			roots = append(roots, t.Txid)
		}
		if len(roots) == 0 {
			return nil, errors.New("case has no tracked txhashes")
		}
		return roots, nil
	}
	txid := strings.ToLower(q.Get("txid"))
	if txid == "" {
		return nil, errors.New("case_id or txid required")
	}
	if !isTxid(txid) {
		return nil, errors.New("txid must be 64 hex chars")
	}
	return []string{txid}, nil
}

func buildNotifier(typ string, cfg json.RawMessage) (notify.Notifier, error) {
	return notify.Build(typ, cfg)
}
