package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type TxRow struct {
	Txid   string
	Height int64
	Ts     int64
}

type OutRow struct {
	Txid      string
	Vout      int32
	Address   string
	ValueSats int64
}

type InRow struct {
	Txid      string
	Vin       int32
	SpentTxid string
	SpentVout int32
	Height    int64
}

func (s *Store) InsertBlock(ctx context.Context, height int64, hash string, ts int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO blocks (height, hash, ts) VALUES ($1, $2, $3) ON CONFLICT (height) DO UPDATE SET hash = EXCLUDED.hash, ts = EXCLUDED.ts`,
		height, hash, ts)
	return err
}

func (s *Store) BlockHash(ctx context.Context, height int64) (string, error) {
	var h string
	err := s.pool.QueryRow(ctx, `SELECT hash FROM blocks WHERE height = $1`, height).Scan(&h)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return h, err
}

func (s *Store) LastHeight(ctx context.Context) (int64, error) {
	var h int64
	err := s.pool.QueryRow(ctx, `SELECT last_height FROM sync_state WHERE id = 1`).Scan(&h)
	return h, err
}

// MinBlockHeight is the earliest indexed block (0 when nothing indexed yet).
func (s *Store) MinBlockHeight(ctx context.Context) (int64, error) {
	var h int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(MIN(height), 0) FROM blocks`).Scan(&h)
	return h, err
}

func (s *Store) SetLastHeight(ctx context.Context, height int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE sync_state SET last_height = $1, updated_at = now() WHERE id = 1`, height)
	return err
}

type SyncState struct {
	LastHeight int64
	UpdatedAt  time.Time
}

func (s *Store) SyncState(ctx context.Context) (*SyncState, error) {
	var st SyncState
	err := s.pool.QueryRow(ctx, `SELECT last_height, updated_at FROM sync_state WHERE id = 1`).
		Scan(&st.LastHeight, &st.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) BlockTime(ctx context.Context, height int64) (int64, error) {
	var ts int64
	err := s.pool.QueryRow(ctx, `SELECT ts FROM blocks WHERE height = $1`, height).Scan(&ts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return ts, err
}

func (s *Store) DefaultMinSats(ctx context.Context) (int64, error) {
	var m int64
	err := s.pool.QueryRow(ctx, `SELECT default_min_sats FROM sync_state WHERE id = 1`).Scan(&m)
	return m, err
}

func (s *Store) SetDefaultMinSats(ctx context.Context, sats int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE sync_state SET default_min_sats = $1 WHERE id = 1`, sats)
	return err
}

func (s *Store) InsertTxs(ctx context.Context, rows []TxRow) error {
	if len(rows) == 0 {
		return nil
	}
	txids := make([]string, len(rows))
	heights := make([]int64, len(rows))
	tss := make([]int64, len(rows))
	for i, r := range rows {
		txids[i], heights[i], tss[i] = r.Txid, r.Height, r.Ts
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO txs (txid, height, ts)
		 SELECT * FROM unnest($1::text[], $2::bigint[], $3::bigint[])
		 ON CONFLICT (txid) DO UPDATE SET height = GREATEST(txs.height, EXCLUDED.height), ts = GREATEST(txs.ts, EXCLUDED.ts)`,
		txids, heights, tss)
	return err
}

func (s *Store) InsertOutputs(ctx context.Context, rows []OutRow) error {
	if len(rows) == 0 {
		return nil
	}
	txids := make([]string, len(rows))
	vouts := make([]int32, len(rows))
	addrs := make([]string, len(rows))
	vals := make([]int64, len(rows))
	for i, r := range rows {
		txids[i], vouts[i], addrs[i], vals[i] = r.Txid, r.Vout, r.Address, r.ValueSats
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tx_outputs (txid, vout, address, value_sats)
		 SELECT * FROM unnest($1::text[], $2::int[], $3::text[], $4::bigint[])
		 ON CONFLICT DO NOTHING`,
		txids, vouts, addrs, vals)
	return err
}

func (s *Store) InsertInputs(ctx context.Context, rows []InRow) error {
	if len(rows) == 0 {
		return nil
	}
	txids := make([]string, len(rows))
	vins := make([]int32, len(rows))
	spentTxids := make([]string, len(rows))
	spentVouts := make([]int32, len(rows))
	heights := make([]int64, len(rows))
	for i, r := range rows {
		txids[i], vins[i], spentTxids[i], spentVouts[i], heights[i] = r.Txid, r.Vin, r.SpentTxid, r.SpentVout, r.Height
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tx_inputs (txid, vin, spent_txid, spent_vout, height)
		 SELECT * FROM unnest($1::text[], $2::int[], $3::text[], $4::int[], $5::bigint[])
		 ON CONFLICT DO NOTHING`,
		txids, vins, spentTxids, spentVouts, heights)
	return err
}

type IndexedOut struct {
	Txid      string
	Vout      int32
	Address   string
	ValueSats int64
}

func (s *Store) OutputsForTxids(ctx context.Context, txids []string) ([]IndexedOut, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT txid, vout, address, value_sats FROM tx_outputs WHERE txid = ANY($1) ORDER BY txid, vout`, txids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexedOut
	for rows.Next() {
		var o IndexedOut
		if err := rows.Scan(&o.Txid, &o.Vout, &o.Address, &o.ValueSats); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

type SpendEdge struct {
	SpentTxid string
	SpentVout int32
	Address   string
	ValueSats int64
	Spender   string
	Height    int64
}

func (s *Store) SpendersOf(ctx context.Context, spentTxids []string, spentVouts []int32, limit int) ([]SpendEdge, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT u.txid, u.vout, o.address, o.value_sats, i.txid, i.height
		 FROM unnest($1::text[], $2::int[]) AS u(txid, vout)
		 JOIN tx_inputs i ON i.spent_txid = u.txid AND i.spent_vout = u.vout
		 JOIN tx_outputs o ON o.txid = u.txid AND o.vout = u.vout
		 LIMIT $3`,
		spentTxids, spentVouts, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpendEdge
	for rows.Next() {
		var e SpendEdge
		if err := rows.Scan(&e.SpentTxid, &e.SpentVout, &e.Address, &e.ValueSats, &e.Spender, &e.Height); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) TxHeight(ctx context.Context, txid string) (int64, error) {
	var h int64
	err := s.pool.QueryRow(ctx, `SELECT height FROM txs WHERE txid = $1`, txid).Scan(&h)
	return h, err
}

func (s *Store) ResetFromHeight(ctx context.Context, height int64) error {
	batch := &pgx.Batch{}
	batch.Queue(`DELETE FROM watched_outputs WHERE height >= $1`, height)
	batch.Queue(`UPDATE watched_outputs SET status = 'watching', spent_by_txid = NULL, spent_height = NULL WHERE spent_height >= $1`, height)
	batch.Queue(`DELETE FROM tx_inputs WHERE height >= $1`, height)
	batch.Queue(`DELETE FROM tx_outputs WHERE txid IN (SELECT txid FROM txs WHERE height >= $1)`, height)
	batch.Queue(`DELETE FROM txs WHERE height >= $1`, height)
	batch.Queue(`DELETE FROM blocks WHERE height >= $1`, height)
	return s.pool.SendBatch(ctx, batch).Close()
}
