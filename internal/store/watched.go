package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("not found")

type WatchedRow struct {
	CaseID      int64
	Txid        string
	Vout        int32
	Address     string
	ValueSats   int64
	Depth       int32
	Status      string
	SpentByTxid *string
	SpentHeight *int64
	Height      int64
}

type WatchedMatch struct {
	CaseID    int64
	Txid      string
	Vout      int32
	Address   string
	ValueSats int64
	Depth     int32
	MinSats   int64
	DepthCap  int32
	BranchCap int32
}

func (s *Store) MatchWatchedInputs(ctx context.Context, spentTxids []string, spentVouts []int32) ([]WatchedMatch, error) {
	if len(spentTxids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT w.case_id, w.txid, w.vout, w.address, w.value_sats, w.depth,
		        COALESCE(c.min_sats, ss.default_min_sats), c.depth_cap, c.branch_cap
		 FROM unnest($1::text[], $2::int[]) AS u(txid, vout)
		 JOIN watched_outputs w ON w.txid = u.txid AND w.vout = u.vout AND w.status = 'watching'
		 JOIN cases c ON c.id = w.case_id AND c.status = 'active'
		 CROSS JOIN sync_state ss`,
		spentTxids, spentVouts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WatchedMatch
	for rows.Next() {
		var m WatchedMatch
		if err := rows.Scan(&m.CaseID, &m.Txid, &m.Vout, &m.Address, &m.ValueSats, &m.Depth, &m.MinSats, &m.DepthCap, &m.BranchCap); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) MarkWatchedSpent(ctx context.Context, caseID int64, txid string, vout int32, spenderTxid string, height int64) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE watched_outputs SET status = 'spent', spent_by_txid = $4, spent_height = $5
		 WHERE case_id = $1 AND txid = $2 AND vout = $3 AND status = 'watching'`,
		caseID, txid, vout, spenderTxid, height)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) ConfirmWatchedSpend(ctx context.Context, caseID int64, txid string, vout int32, spenderTxid string, height int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE watched_outputs SET spent_height = $5
		 WHERE case_id = $1 AND txid = $2 AND vout = $3 AND spent_by_txid = $4 AND spent_height = 0`,
		caseID, txid, vout, spenderTxid, height)
	return err
}

func (s *Store) RevertWatchedSpend(ctx context.Context, caseID int64, txid string, vout int32) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE watched_outputs SET status = 'watching', spent_by_txid = NULL, spent_height = NULL
		 WHERE case_id = $1 AND txid = $2 AND vout = $3 AND spent_height = 0`,
		caseID, txid, vout)
	return err
}

func (s *Store) UpdateWatchedHeights(ctx context.Context, txids []string, height int64) error {
	if len(txids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE watched_outputs SET height = $2 WHERE txid = ANY($1) AND height = 0`, txids, height)
	return err
}

func (s *Store) AddWatched(ctx context.Context, rows []WatchedRow) ([]WatchedRow, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	caseIDs := make([]int64, len(rows))
	txids := make([]string, len(rows))
	vouts := make([]int32, len(rows))
	addrs := make([]string, len(rows))
	vals := make([]int64, len(rows))
	depths := make([]int32, len(rows))
	heights := make([]int64, len(rows))
	for i, r := range rows {
		caseIDs[i], txids[i], vouts[i], addrs[i], vals[i], depths[i], heights[i] =
			r.CaseID, r.Txid, r.Vout, r.Address, r.ValueSats, r.Depth, r.Height
	}
	res, err := s.pool.Query(ctx,
		`INSERT INTO watched_outputs (case_id, txid, vout, address, value_sats, depth, status, height)
		 SELECT * FROM unnest($1::bigint[], $2::text[], $3::int[], $4::text[], $5::bigint[], $6::int[], $7::text[], $8::bigint[])
		 ON CONFLICT DO NOTHING
		 RETURNING case_id, txid, vout, address, value_sats, depth`,
		caseIDs, txids, vouts, addrs, vals, depths, "watching", heights)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	var inserted []WatchedRow
	for res.Next() {
		var r WatchedRow
		if err := res.Scan(&r.CaseID, &r.Txid, &r.Vout, &r.Address, &r.ValueSats, &r.Depth); err != nil {
			return nil, err
		}
		r.Status = "watching"
		inserted = append(inserted, r)
	}
	return inserted, res.Err()
}

func (s *Store) WatchingOutputs(ctx context.Context) ([]WatchedRow, error) {
	return s.queryWatched(ctx, `SELECT case_id, txid, vout, address, value_sats, depth, status, spent_by_txid, spent_height, height FROM watched_outputs WHERE status = 'watching'`)
}

func (s *Store) PendingMempoolSpends(ctx context.Context) ([]WatchedRow, error) {
	return s.queryWatched(ctx, `SELECT case_id, txid, vout, address, value_sats, depth, status, spent_by_txid, spent_height, height FROM watched_outputs WHERE spent_height = 0`)
}

func (s *Store) queryWatched(ctx context.Context, q string) ([]WatchedRow, error) {
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WatchedRow
	for rows.Next() {
		var r WatchedRow
		if err := rows.Scan(&r.CaseID, &r.Txid, &r.Vout, &r.Address, &r.ValueSats, &r.Depth, &r.Status, &r.SpentByTxid, &r.SpentHeight, &r.Height); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SetWatchedTerminal(ctx context.Context, caseID int64, txid string, vout int32) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE watched_outputs SET status = 'terminal' WHERE case_id = $1 AND txid = $2 AND vout = $3 AND status = 'watching'`,
		caseID, txid, vout)
	return err
}

func (s *Store) CountWatched(ctx context.Context, caseID int64) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM watched_outputs WHERE case_id = $1`, caseID).Scan(&n)
	return n, err
}

func (s *Store) DeleteUnconfirmedFrom(ctx context.Context, caseID int64, txid string) error {
	frontier := []string{txid}
	seen := map[string]bool{txid: true}
	for len(frontier) > 0 {
		var next []string
		for _, t := range frontier {
			_, err := s.pool.Exec(ctx,
				`DELETE FROM watched_outputs WHERE case_id = $1 AND height = 0 AND spent_by_txid = $2`,
				caseID, t)
			if err != nil {
				return err
			}
			rows, err := s.pool.Query(ctx,
				`SELECT DISTINCT txid FROM watched_outputs WHERE case_id = $1 AND height = 0 AND spent_by_txid = $2`, caseID, t)
			if err != nil {
				return err
			}
			for rows.Next() {
				var child string
				if err := rows.Scan(&child); err != nil {
					rows.Close()
					return err
				}
				if !seen[child] {
					seen[child] = true
					next = append(next, child)
				}
			}
			rows.Close()
		}
		frontier = next
	}
	return nil
}

type AddressInfo struct {
	Address string
	Label   string
	Source  string
	IsCEX   bool
}

func (s *Store) GetAddress(ctx context.Context, addr string) (*AddressInfo, error) {
	var a AddressInfo
	err := s.pool.QueryRow(ctx,
		`SELECT address, label, source, is_cex FROM addresses WHERE address = $1`, addr).
		Scan(&a.Address, &a.Label, &a.Source, &a.IsCEX)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) UpsertAddress(ctx context.Context, a AddressInfo) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO addresses (address, label, source, is_cex, checked_at) VALUES ($1, $2, $3, $4, now())
		 ON CONFLICT (address) DO UPDATE SET label = EXCLUDED.label, source = EXCLUDED.source, is_cex = EXCLUDED.is_cex, checked_at = now()`,
		a.Address, a.Label, a.Source, a.IsCEX)
	return err
}

type Alert struct {
	ID        int64
	CaseID    int64
	Txid      string
	Address   string
	ValueSats int64
	Depth     int32
	Kind      string
	Message   string
	CreatedAt string
}

func (s *Store) AddAlert(ctx context.Context, a Alert) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO alerts (case_id, txid, address, value_sats, depth, kind, message) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		a.CaseID, a.Txid, a.Address, a.ValueSats, a.Depth, a.Kind, a.Message)
	return err
}

func (s *Store) ListAlerts(ctx context.Context, caseID int64, limit int) ([]Alert, error) {
	q := `SELECT id, case_id, txid, address, value_sats, depth, kind, message, created_at::text FROM alerts`
	args := []any{limit}
	if caseID > 0 {
		q += ` WHERE case_id = $2`
		args = append(args, caseID)
	}
	q += ` ORDER BY id DESC LIMIT $1`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.CaseID, &a.Txid, &a.Address, &a.ValueSats, &a.Depth, &a.Kind, &a.Message, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
