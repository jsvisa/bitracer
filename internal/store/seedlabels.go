package store

import (
	"context"
	"encoding/json"
	"os"
)

type SeedLabel struct {
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// UpsertSeedLabels inserts known-entity entries without clobbering rows that
// already exist (vendor labels and manual pins win); returns how many new
// rows were inserted.
func (s *Store) UpsertSeedLabels(ctx context.Context, entries []AddressInfo) (int64, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	addrs := make([]string, len(entries))
	labels := make([]string, len(entries))
	sources := make([]string, len(entries))
	kinds := make([]string, len(entries))
	for i, e := range entries {
		addrs[i], labels[i], sources[i], kinds[i] = e.Address, e.Label, e.Source, e.TerminalKind
	}
	res, err := s.pool.Query(ctx,
		`INSERT INTO addresses (address, label, source, is_cex, is_terminal, terminal_kind, checked_at)
		 SELECT a, l, src, kind = 'cex', kind <> '', kind
		 FROM unnest($1::text[], $2::text[], $3::text[], $4::text[]) AS t(a, l, src, kind)
		 ON CONFLICT (address) DO NOTHING
		 RETURNING address`, addrs, labels, sources, kinds)
	if err != nil {
		return 0, err
	}
	defer res.Close()
	var n int64
	for res.Next() {
		n++
	}
	return n, res.Err()
}

// LoadSeedLabelsFile reads a JSON map of address -> {label, kind} and inserts
// the unknown ones. Entries with a kind are terminal stops; kind "" is
// attribution only.
func (s *Store) LoadSeedLabelsFile(ctx context.Context, path string) (int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var m map[string]SeedLabel
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, err
	}
	entries := make([]AddressInfo, 0, len(m))
	for addr, e := range m {
		entries = append(entries, AddressInfo{
			Address: addr, Label: e.Label, Source: "seed",
			IsCEX: e.Kind == "cex", IsTerminal: e.Kind != "", TerminalKind: e.Kind,
		})
	}
	return s.UpsertSeedLabels(ctx, entries)
}
