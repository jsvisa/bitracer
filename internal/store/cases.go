package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Case struct {
	ID                 int64     `json:"id"`
	Name               string    `json:"name"`
	MinSats            *int64    `json:"min_sats"`
	DepthCap           int32     `json:"depth_cap"`
	BranchCap          int32     `json:"branch_cap"`
	Status             string    `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
	BackfillCheckpoint int64     `json:"backfill_checkpoint"`
	BackfillTarget     int64     `json:"backfill_target"`
}

type CaseTx struct {
	CaseID int64  `json:"case_id"`
	Txid   string `json:"txid"`
	Seeded bool   `json:"seeded"`
}

type Channel struct {
	ID     int64           `json:"id"`
	CaseID int64           `json:"case_id"`
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

var ErrDuplicate = errors.New("already exists")

func (s *Store) CreateCase(ctx context.Context, name string, minSats *int64, depthCap, branchCap int32) (Case, error) {
	var c Case
	err := s.pool.QueryRow(ctx,
		`INSERT INTO cases (name, min_sats, depth_cap, branch_cap) VALUES ($1, $2, $3, $4)
		 RETURNING id, name, min_sats, depth_cap, branch_cap, status, created_at, backfill_checkpoint, backfill_target`,
		name, minSats, depthCap, branchCap).
		Scan(&c.ID, &c.Name, &c.MinSats, &c.DepthCap, &c.BranchCap, &c.Status, &c.CreatedAt, &c.BackfillCheckpoint, &c.BackfillTarget)
	return c, err
}

const caseCols = `id, name, min_sats, depth_cap, branch_cap, status, created_at, backfill_checkpoint, backfill_target`

func scanCase(row pgx.Row) (Case, error) {
	var c Case
	err := row.Scan(&c.ID, &c.Name, &c.MinSats, &c.DepthCap, &c.BranchCap, &c.Status, &c.CreatedAt, &c.BackfillCheckpoint, &c.BackfillTarget)
	return c, err
}

func (s *Store) GetCase(ctx context.Context, id int64) (Case, error) {
	c, err := scanCase(s.pool.QueryRow(ctx, `SELECT `+caseCols+` FROM cases WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) ListCases(ctx context.Context) ([]Case, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+caseCols+` FROM cases ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Case
	for rows.Next() {
		c, err := scanCase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) SetCaseStatus(ctx context.Context, id int64, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE cases SET status = $2 WHERE id = $1`, id, status)
	return err
}

func (s *Store) UpdateCase(ctx context.Context, id int64, name string, minSats *int64, depthCap, branchCap int32) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE cases SET name = $2, min_sats = $3, depth_cap = $4, branch_cap = $5 WHERE id = $1`,
		id, name, minSats, depthCap, branchCap)
	return err
}

func (s *Store) DeleteCase(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM cases WHERE id = $1`, id)
	return err
}

func (s *Store) AddCaseTx(ctx context.Context, caseID int64, txid string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO case_txs (case_id, txid) VALUES ($1, $2) ON CONFLICT DO NOTHING`, caseID, txid)
	return err
}

func (s *Store) ListCaseTxs(ctx context.Context, caseID int64) ([]CaseTx, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT case_id, txid, seeded FROM case_txs WHERE case_id = $1 ORDER BY txid`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CaseTx
	for rows.Next() {
		var t CaseTx
		if err := rows.Scan(&t.CaseID, &t.Txid, &t.Seeded); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCaseTx(ctx context.Context, caseID int64, txid string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM case_txs WHERE case_id = $1 AND txid = $2`, caseID, txid)
	return err
}

type CaseSeed struct {
	CaseID    int64
	Txid      string
	MinSats   int64
	DepthCap  int32
	BranchCap int32
}

func (s *Store) PendingCaseSeeds(ctx context.Context) ([]CaseSeed, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT ct.case_id, ct.txid, COALESCE(c.min_sats, ss.default_min_sats), c.depth_cap, c.branch_cap
		 FROM case_txs ct
		 JOIN cases c ON c.id = ct.case_id AND c.status = 'active'
		 CROSS JOIN sync_state ss
		 WHERE ct.seeded = FALSE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CaseSeed
	for rows.Next() {
		var cs CaseSeed
		if err := rows.Scan(&cs.CaseID, &cs.Txid, &cs.MinSats, &cs.DepthCap, &cs.BranchCap); err != nil {
			return nil, err
		}
		out = append(out, cs)
	}
	return out, rows.Err()
}

func (s *Store) SetCaseTxSeeded(ctx context.Context, caseID int64, txid string) error {
	_, err := s.pool.Exec(ctx, `UPDATE case_txs SET seeded = TRUE WHERE case_id = $1 AND txid = $2`, caseID, txid)
	return err
}

func (s *Store) AddChannel(ctx context.Context, caseID int64, name, typ string, cfg json.RawMessage) (Channel, error) {
	var ch Channel
	err := s.pool.QueryRow(ctx,
		`INSERT INTO case_channels (case_id, name, type, config) VALUES ($1, $2, $3, $4)
		 RETURNING id, case_id, name, type, config`, caseID, name, typ, cfg).
		Scan(&ch.ID, &ch.CaseID, &ch.Name, &ch.Type, &ch.Config)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ch, ErrDuplicate
		}
	}
	return ch, err
}

func (s *Store) ListChannels(ctx context.Context, caseID int64) ([]Channel, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, case_id, name, type, config FROM case_channels WHERE case_id = $1 ORDER BY id`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var ch Channel
		if err := rows.Scan(&ch.ID, &ch.CaseID, &ch.Name, &ch.Type, &ch.Config); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM case_channels WHERE id = $1`, id)
	return err
}

// SetCaseBackfill registers a gap backfill for the case: checkpoint is the next
// height to process, target is the exclusive upper bound (the db's first block
// at detection time). If a backfill is already in flight the range is extended:
// the checkpoint moves down and the target moves up.
func (s *Store) SetCaseBackfill(ctx context.Context, caseID, checkpoint, target int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE cases SET
			 backfill_checkpoint = CASE WHEN backfill_target > 0 AND backfill_checkpoint > 0 AND backfill_checkpoint < backfill_target
			                            THEN LEAST(backfill_checkpoint, $2) ELSE $2 END,
			 backfill_target = GREATEST(backfill_target, $3)
		 WHERE id = $1 AND status = 'active'`, caseID, checkpoint, target)
	return err
}

// AdvanceCaseBackfill persists progress after a gap block has been indexed.
func (s *Store) AdvanceCaseBackfill(ctx context.Context, caseID, checkpoint int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE cases SET backfill_checkpoint = $2 WHERE id = $1 AND backfill_target > 0`, caseID, checkpoint)
	return err
}

// ClearCaseBackfill marks the backfill as finished.
func (s *Store) ClearCaseBackfill(ctx context.Context, caseID int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE cases SET backfill_checkpoint = 0, backfill_target = 0 WHERE id = $1`, caseID)
	return err
}

type CaseBackfill struct {
	CaseID     int64
	Checkpoint int64
	Target     int64
}

// CaseBackfill returns the current backfill checkpoint for an active case
// (ErrNotFound when the case is missing, paused, or finished).
func (s *Store) CaseBackfill(ctx context.Context, caseID int64) (*CaseBackfill, error) {
	var b CaseBackfill
	err := s.pool.QueryRow(ctx,
		`SELECT id, backfill_checkpoint, backfill_target FROM cases WHERE id = $1 AND status = 'active'`, caseID).
		Scan(&b.CaseID, &b.Checkpoint, &b.Target)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// PendingCaseBackfills lists active cases with an unfinished backfill, so the
// worker can (re)spawn goroutines after a restart or crash.
func (s *Store) PendingCaseBackfills(ctx context.Context) ([]CaseBackfill, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, backfill_checkpoint, backfill_target FROM cases
		 WHERE status = 'active' AND backfill_target > 0 AND backfill_checkpoint > 0 AND backfill_checkpoint < backfill_target
		 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CaseBackfill
	for rows.Next() {
		var b CaseBackfill
		if err := rows.Scan(&b.CaseID, &b.Checkpoint, &b.Target); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
