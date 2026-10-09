package labeler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
		return &labels.Label{Name: cached.Label, Source: cached.Source, IsCEX: cached.IsCEX, Kind: cached.TerminalKind}, nil
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
		info = store.AddressInfo{
			Address: addr, Label: lbl.Name, Source: lbl.Source,
			IsCEX: lbl.IsCEX, IsTerminal: lbl.Kind != "", TerminalKind: lbl.Kind,
		}
	}
	if err := s.st.UpsertAddress(ctx, info); err != nil {
		return nil, err
	}
	if lbl != nil && lbl.Kind != "" {
		s.markTerminal(ctx, addr, lbl)
	}
	return lbl, nil
}

// TerminalKinds are the kinds accepted by the manual pin endpoint.
var TerminalKinds = []string{"cex", "mixer", "service", "gambling", "darknet", "manual"}

func ValidTerminalKind(kind string) bool {
	for _, k := range TerminalKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// PinTerminal marks an address as a stop point by hand (for entities the
// vendor does not label) and stops any case output already watching it.
func (s *Service) PinTerminal(ctx context.Context, addr, kind string) error {
	if kind == "" {
		kind = "manual"
	}
	if !ValidTerminalKind(kind) {
		return fmt.Errorf("kind must be one of %s", strings.Join(TerminalKinds, ", "))
	}
	if err := s.st.SetAddressTerminal(ctx, addr, kind); err != nil {
		return err
	}
	s.markTerminal(ctx, addr, &labels.Label{Name: addr, Kind: kind})
	return nil
}

func (s *Service) UnpinTerminal(ctx context.Context, addr string) error {
	return s.st.ClearAddressTerminal(ctx, addr)
}

func (s *Service) markTerminal(ctx context.Context, addr string, lbl *labels.Label) {
	kind := lbl.Kind
	if kind == "" {
		kind = "cex"
	}
	entity := lbl.Name
	if entity == "" {
		entity = kind
	}
	FlipAddressTerminal(ctx, s.st, addr, kind, entity)
}

// FlipAddressTerminal stops every case still watching outputs at addr and
// emits one aggregated alert per case. Shared by vendor-label hits, manual
// pins and the heuristic stops.
func FlipAddressTerminal(ctx context.Context, st *store.Store, addr, kind, entity string) {
	rows, err := st.WatchingByAddress(ctx, addr)
	if err != nil {
		slog.Error("watching lookup failed", "address", addr, "err", err)
		return
	}
	byCase := map[int64][]store.WatchedRow{}
	for _, r := range rows {
		byCase[r.CaseID] = append(byCase[r.CaseID], r)
	}
	for caseID, rs := range byCase {
		var sum int64
		var depth int32
		txid := rs[0].Txid
		flipped := 0
		for _, r := range rs {
			ok, err := st.SetWatchedTerminal(ctx, caseID, r.Txid, r.Vout)
			if err != nil {
				slog.Error("terminal flip failed", "case", caseID, "err", err)
				continue
			}
			if !ok {
				continue
			}
			flipped++
			sum += r.ValueSats
			if r.Depth > depth {
				depth = r.Depth
			}
		}
		if flipped == 0 {
			continue
		}
		msg := notify.Message{
			Kind:      kind,
			CaseID:    caseID,
			Headline:  fmt.Sprintf("%.2f BTC reached %s — STOP", btc.SatsToBTC(sum), entity),
			Entity:    entity,
			Txid:      txid,
			Address:   addr,
			ValueSats: sum,
			Depth:     depth,
		}
		msg.Parking = holdings(ctx, st, caseID)
		if err := alerts.Emit(ctx, st, store.Alert{
			CaseID: caseID, Txid: txid, Address: addr,
			ValueSats: sum, Depth: depth, Kind: kind,
		}, msg); err != nil {
			slog.Error("terminal alert failed", "case", caseID, "err", err)
		}
	}
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:10] + "…"
}

// holdings maps the store's per-address fund summary into the message's
// parking field; errors degrade to no parking line.
func holdings(ctx context.Context, st *store.Store, caseID int64) []notify.Holding {
	hs, err := st.CaseHoldings(ctx, caseID)
	if err != nil {
		slog.Error("holdings query failed", "case", caseID, "err", err)
		return nil
	}
	out := make([]notify.Holding, len(hs))
	for i, h := range hs {
		out[i] = notify.Holding{Address: h.Address, Sats: h.Sats}
	}
	return out
}
