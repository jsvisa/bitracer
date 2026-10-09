package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

const migrateLockKey = 0x62697472

func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrateLockKey); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrateLockKey)
	for _, q := range migrateStmts {
		if _, err := conn.Exec(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

var migrateStmts = []string{
	`CREATE TABLE IF NOT EXISTS blocks (
			height BIGINT PRIMARY KEY,
			hash TEXT NOT NULL,
			ts BIGINT NOT NULL DEFAULT 0
		)`,
	`CREATE TABLE IF NOT EXISTS txs (
			txid TEXT PRIMARY KEY,
			height BIGINT NOT NULL,
			ts BIGINT NOT NULL DEFAULT 0
		)`,
	`CREATE TABLE IF NOT EXISTS tx_outputs (
			txid TEXT NOT NULL,
			vout INT NOT NULL,
			address TEXT NOT NULL DEFAULT '',
			value_sats BIGINT NOT NULL,
			PRIMARY KEY (txid, vout)
		)`,
	`CREATE INDEX IF NOT EXISTS idx_tx_outputs_address ON tx_outputs (address)`,
	`CREATE TABLE IF NOT EXISTS tx_inputs (
			txid TEXT NOT NULL,
			vin INT NOT NULL,
			spent_txid TEXT NOT NULL,
			spent_vout INT NOT NULL,
			height BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY (txid, vin)
		)`,
	`CREATE INDEX IF NOT EXISTS idx_tx_inputs_spent ON tx_inputs (spent_txid, spent_vout)`,
	`CREATE INDEX IF NOT EXISTS idx_tx_inputs_height ON tx_inputs (height)`,
	`CREATE TABLE IF NOT EXISTS cases (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			min_sats BIGINT,
			depth_cap INT NOT NULL DEFAULT 50,
			branch_cap INT NOT NULL DEFAULT 500,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	`CREATE TABLE IF NOT EXISTS case_txs (
			case_id BIGINT NOT NULL REFERENCES cases(id) ON DELETE CASCADE,
			txid TEXT NOT NULL,
			seeded BOOLEAN NOT NULL DEFAULT FALSE,
			PRIMARY KEY (case_id, txid)
		)`,
	`CREATE TABLE IF NOT EXISTS case_channels (
			id BIGSERIAL PRIMARY KEY,
			case_id BIGINT NOT NULL REFERENCES cases(id) ON DELETE CASCADE,
			type TEXT NOT NULL,
			config JSONB NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	`CREATE TABLE IF NOT EXISTS watched_outputs (
			case_id BIGINT NOT NULL,
			txid TEXT NOT NULL,
			vout INT NOT NULL,
			address TEXT NOT NULL DEFAULT '',
			value_sats BIGINT NOT NULL,
			depth INT NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'watching',
			spent_by_txid TEXT,
			spent_height BIGINT,
			height BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY (case_id, txid, vout)
		)`,
	`CREATE INDEX IF NOT EXISTS idx_watched_outpoint ON watched_outputs (txid, vout)`,
	`CREATE INDEX IF NOT EXISTS idx_watched_status ON watched_outputs (case_id, status)`,
	`CREATE INDEX IF NOT EXISTS idx_watched_parent ON watched_outputs (txid) WHERE height = 0`,
	`CREATE TABLE IF NOT EXISTS addresses (
			address TEXT PRIMARY KEY,
			label TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			is_cex BOOLEAN NOT NULL DEFAULT FALSE,
			checked_at TIMESTAMPTZ
		)`,
	`CREATE TABLE IF NOT EXISTS alerts (
			id BIGSERIAL PRIMARY KEY,
			case_id BIGINT NOT NULL,
			txid TEXT NOT NULL,
			address TEXT NOT NULL DEFAULT '',
			value_sats BIGINT NOT NULL DEFAULT 0,
			depth INT NOT NULL DEFAULT 0,
			kind TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	`CREATE TABLE IF NOT EXISTS sync_state (
			id INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			last_height BIGINT NOT NULL DEFAULT 0,
			default_min_sats BIGINT NOT NULL DEFAULT 10000000
		)`,
	`ALTER TABLE sync_state ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`,
	`INSERT INTO sync_state (id) VALUES (1) ON CONFLICT DO NOTHING`,
}
