package labeler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jsvisa/bitracer/internal/alerts"
	"github.com/jsvisa/bitracer/internal/btc"
	"github.com/jsvisa/bitracer/internal/labels"
	"github.com/jsvisa/bitracer/internal/notify"
	"github.com/jsvisa/bitracer/internal/store"
)

type Service struct {
	st       *store.Store
	reg      *labels.Registry
	interval time.Duration
}

func New(st *store.Store, reg *labels.Registry, interval time.Duration) *Service {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &Service{st: st, reg: reg, interval: interval}
}

func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := s.Pass(ctx, 0, 50); err != nil {
				slog.Error("labeler pass failed", "err", err)
			}
		}
	}
}

func (s *Service) Pass(ctx context.Context, caseID int64, limit int) error {
	if s.reg == nil || len(s.reg.Providers()) == 0 {
		return nil
	}
	var addrs []string
	var err error
	if caseID > 0 {
		addrs, err = s.st.CaseLabelTargets(ctx, caseID, limit)
	} else {
		addrs, err = s.st.LabelTargets(ctx, limit)
	}
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		if _, err := s.ResolveAddress(ctx, addr); err != nil {
			slog.Warn("label resolve failed", "address", addr, "err", err)
		}
	}
	return nil
}

func (s *Service) ResolveAddress(ctx context.Context, addr string) (*labels.Label, error) {
	cached, err := s.st.GetAddress(ctx, addr)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		return &labels.Label{Name: cached.Label, Source: cached.Source, IsCEX: cached.IsCEX}, nil
	}
	if s.reg == nil {
		return nil, nil
	}
	lbl, err := s.reg.Lookup(ctx, addr)
	if err != nil {
		return nil, err
	}
	info := store.AddressInfo{Address: addr}
	if lbl != nil {
		info = store.AddressInfo{Address: addr, Label: lbl.Name, Source: lbl.Source, IsCEX: lbl.IsCEX}
	}
	if err := s.st.UpsertAddress(ctx, info); err != nil {
		return nil, err
	}
	if lbl != nil && lbl.IsCEX {
		s.markTerminal(ctx, addr, lbl)
	}
	return lbl, nil
}

func (s *Service) markTerminal(ctx context.Context, addr string, lbl *labels.Label) {
	rows, err := s.st.WatchingByAddress(ctx, addr)
	if err != nil {
		slog.Error("watching lookup failed", "address", addr, "err", err)
		return
	}
	for _, r := range rows {
		flipped, err := s.st.SetWatchedTerminal(ctx, r.CaseID, r.Txid, r.Vout)
		if err != nil {
			slog.Error("terminal flip failed", "case", r.CaseID, "err", err)
			continue
		}
		if !flipped {
			continue
		}
		msg := notify.Message{
			Kind:      "cex",
			CaseID:    r.CaseID,
			Headline:  fmt.Sprintf("%.8f BTC reached %s — STOP", btc.SatsToBTC(r.ValueSats), lbl.Name),
			Entity:    lbl.Name,
			Txid:      r.Txid,
			Address:   addr,
			ValueSats: r.ValueSats,
			Depth:     r.Depth,
		}
		if err := alerts.Emit(ctx, s.st, store.Alert{
			CaseID: r.CaseID, Txid: r.Txid, Address: addr,
			ValueSats: r.ValueSats, Depth: r.Depth, Kind: "cex",
		}, msg); err != nil {
			slog.Error("cex alert failed", "case", r.CaseID, "err", err)
		}
	}
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:10] + "…"
}
