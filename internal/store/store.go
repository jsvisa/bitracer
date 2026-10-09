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
	`ALTER TABLE sync_state ADD COLUMN IF NOT EXISTS pruned_min_sats BIGINT NOT NULL DEFAULT 0`,
	`INSERT INTO sync_state (id) VALUES (1) ON CONFLICT DO NOTHING`,
	`ALTER TABLE cases ADD COLUMN IF NOT EXISTS backfill_checkpoint BIGINT NOT NULL DEFAULT 0`,
	`ALTER TABLE cases ADD COLUMN IF NOT EXISTS backfill_target BIGINT NOT NULL DEFAULT 0`,
	`ALTER TABLE addresses ADD COLUMN IF NOT EXISTS is_terminal BOOLEAN NOT NULL DEFAULT FALSE`,
	`ALTER TABLE addresses ADD COLUMN IF NOT EXISTS terminal_kind TEXT NOT NULL DEFAULT ''`,
	`UPDATE addresses SET is_terminal = TRUE, terminal_kind = 'cex' WHERE is_cex AND NOT is_terminal`,
	`CREATE TABLE IF NOT EXISTS channels (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			config JSONB NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS uq_channels_name ON channels (name)`,
	`CREATE TABLE IF NOT EXISTS case_channel_subs (
			case_id BIGINT NOT NULL REFERENCES cases(id) ON DELETE CASCADE,
			channel_id BIGINT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
			PRIMARY KEY (case_id, channel_id)
		)`,
	// One-shot migration: fold the old per-case case_channels rows into the
	// global channels table and preserve each case's subscriptions.
	`DO $$
	 BEGIN
	   IF EXISTS (SELECT 1 FROM information_schema.columns
	              WHERE table_name = 'case_channels' AND column_name = 'type') THEN
	     INSERT INTO channels (name, type, config)
	     SELECT DISTINCT ON (name) name, type, config FROM case_channels ORDER BY name, id
	     ON CONFLICT (name) DO NOTHING;
	     INSERT INTO case_channel_subs (case_id, channel_id)
	     SELECT DISTINCT cc.case_id, ch.id FROM case_channels cc
	     JOIN channels ch ON ch.name = cc.name AND ch.type = cc.type
	     ON CONFLICT DO NOTHING;
	     DROP TABLE case_channels;
	   END IF;
	 END $$`,
}
