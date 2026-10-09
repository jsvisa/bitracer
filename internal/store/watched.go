package store

import (
	"context"
	"errors"
	"strconv"

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
		 SELECT case_id, txid, vout, address, value_sats, depth, 'watching', height
		 FROM unnest($1::bigint[], $2::text[], $3::int[], $4::text[], $5::bigint[], $6::int[], $7::bigint[])
		 AS t(case_id, txid, vout, address, value_sats, depth, height)
		 ON CONFLICT DO NOTHING
		 RETURNING case_id, txid, vout, address, value_sats, depth`,
		caseIDs, txids, vouts, addrs, vals, depths, heights)
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

func (s *Store) WatchingByCase(ctx context.Context, caseID int64) ([]WatchedRow, error) {
	return s.queryWatched(ctx,
		`SELECT case_id, txid, vout, address, value_sats, depth, status, spent_by_txid, spent_height, height
		 FROM watched_outputs WHERE case_id = `+strconv.FormatInt(caseID, 10)+` AND status = 'watching'`)
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

func (s *Store) SetWatchedTerminal(ctx context.Context, caseID int64, txid string, vout int32) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE watched_outputs SET status = 'terminal' WHERE case_id = $1 AND txid = $2 AND vout = $3 AND status = 'watching'`,
		caseID, txid, vout)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) WatchingByAddress(ctx context.Context, addr string) ([]WatchedRow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT case_id, txid, vout, address, value_sats, depth, status, spent_by_txid, spent_height, height
		 FROM watched_outputs WHERE address = $1 AND status = 'watching'`, addr)
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

func (s *Store) LabelTargets(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT w.address FROM watched_outputs w
		 WHERE w.status = 'watching' AND w.address <> ''
		 AND NOT EXISTS (SELECT 1 FROM addresses a WHERE a.address = w.address)
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CaseLabelTargets(ctx context.Context, caseID int64, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT w.address FROM watched_outputs w
		 WHERE w.case_id = $1 AND w.status = 'watching' AND w.address <> ''
		 AND NOT EXISTS (SELECT 1 FROM addresses a WHERE a.address = w.address)
		 LIMIT $2`, caseID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CountWatched(ctx context.Context, caseID int64) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM watched_outputs WHERE case_id = $1`, caseID).Scan(&n)
	return n, err
}

// Holding is one address's share of a case's parked funds.
type Holding struct {
	Address string
	Sats    int64
}

// CaseHoldings sums the case's unspent tracked outputs per address — where
// the funds are parked right now. Covers watching and terminal rows (funds
// that reached a labeled endpoint still sit there); spent rows are gone.
func (s *Store) CaseHoldings(ctx context.Context, caseID int64) ([]Holding, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT address, SUM(value_sats) FROM watched_outputs
		 WHERE case_id = $1 AND address <> '' AND status IN ('watching', 'terminal')
		 GROUP BY address ORDER BY SUM(value_sats) DESC`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Holding
	for rows.Next() {
		var h Holding
		if err := rows.Scan(&h.Address, &h.Sats); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// CaseSeedSats returns the largest depth-0 output of a case — the reference
// for the relative value-decay floor.
func (s *Store) CaseSeedSats(ctx context.Context, caseID int64) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(value_sats), 0) FROM watched_outputs WHERE case_id = $1 AND depth = 0`, caseID).Scan(&n)
	return n, err
}

// WatchedAddressParentCount returns how many distinct txs paid to addr within
// a case — the fan-in convergence signal for unlabeled service sinks.
func (s *Store) WatchedAddressParentCount(ctx context.Context, caseID int64, addr string) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(DISTINCT txid) FROM watched_outputs WHERE case_id = $1 AND address = $2`, caseID, addr).Scan(&n)
	return n, err
}

type AddressInfo struct {
	Address      string
	Label        string
	Source       string
	IsCEX        bool
	IsTerminal   bool
	TerminalKind string
}

func (s *Store) GetAddress(ctx context.Context, addr string) (*AddressInfo, error) {
	var a AddressInfo
	err := s.pool.QueryRow(ctx,
		`SELECT address, label, source, is_cex, is_terminal, terminal_kind FROM addresses WHERE address = $1`, addr).
		Scan(&a.Address, &a.Label, &a.Source, &a.IsCEX, &a.IsTerminal, &a.TerminalKind)
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
		`INSERT INTO addresses (address, label, source, is_cex, is_terminal, terminal_kind, checked_at) VALUES ($1, $2, $3, $4, $5, $6, now())
		 ON CONFLICT (address) DO UPDATE SET label = EXCLUDED.label, source = EXCLUDED.source, is_cex = EXCLUDED.is_cex, is_terminal = EXCLUDED.is_terminal, terminal_kind = EXCLUDED.terminal_kind, checked_at = now()`,
		a.Address, a.Label, a.Source, a.IsCEX, a.IsTerminal, a.TerminalKind)
	return err
}

// SetAddressTerminal flags an address as a terminal entity without touching
// its label; inserts a bare row when the address is unknown so far.
func (s *Store) SetAddressTerminal(ctx context.Context, addr, kind string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE addresses SET is_terminal = TRUE, terminal_kind = $2, is_cex = is_cex OR $2 = 'cex', checked_at = now()
		 WHERE address = $1`, addr, kind)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO addresses (address, label, source, is_cex, is_terminal, terminal_kind, checked_at) VALUES ($1, '', '', $2 = 'cex', TRUE, $2, now())`,
		addr, kind)
	return err
}

func (s *Store) ClearAddressTerminal(ctx context.Context, addr string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE addresses SET is_terminal = FALSE, terminal_kind = '' WHERE address = $1`, addr)
	return err
}

// SetTxTerminal flips every still-watching output of one tx on a case to
// terminal; returns how many rows flipped.
func (s *Store) SetTxTerminal(ctx context.Context, caseID int64, txid string) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE watched_outputs SET status = 'terminal'
		 WHERE case_id = $1 AND txid = $2 AND status = 'watching'`, caseID, txid)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

type Alert struct {
	ID        int64  `json:"id"`
	CaseID    int64  `json:"case_id"`
	Txid      string `json:"txid"`
	Address   string `json:"address"`
	ValueSats int64  `json:"value_sats"`
	Depth     int32  `json:"depth"`
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
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
