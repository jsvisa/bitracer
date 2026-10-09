package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// Channel is a global notification destination (slack / telegram / lark).
// Credentials are configured once here; cases subscribe via case_channel_subs
// and every alert a case emits fans out to its subscribed channels.
type Channel struct {
	ID     int64           `json:"id"`
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

func (s *Store) CreateChannel(ctx context.Context, name, typ string, cfg json.RawMessage) (Channel, error) {
	var ch Channel
	err := s.pool.QueryRow(ctx,
		`INSERT INTO channels (name, type, config) VALUES ($1, $2, $3)
		 RETURNING id, name, type, config`, name, typ, cfg).
		Scan(&ch.ID, &ch.Name, &ch.Type, &ch.Config)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ch, ErrDuplicate
		}
	}
	return ch, err
}

func (s *Store) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, type, config FROM channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var ch Channel
		if err := rows.Scan(&ch.ID, &ch.Name, &ch.Type, &ch.Config); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// DeleteChannel removes the channel and (via ON DELETE CASCADE) every case
// subscription pointing at it.
func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM channels WHERE id = $1`, id)
	return err
}

// ListCaseChannels returns the channels a case is subscribed to.
func (s *Store) ListCaseChannels(ctx context.Context, caseID int64) ([]Channel, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT ch.id, ch.name, ch.type, ch.config
		 FROM case_channel_subs sub
		 JOIN channels ch ON ch.id = sub.channel_id
		 WHERE sub.case_id = $1 ORDER BY ch.id`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var ch Channel
		if err := rows.Scan(&ch.ID, &ch.Name, &ch.Type, &ch.Config); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// ValidateChannelIDs returns ErrNotFound when any id does not refer to an
// existing channel.
func (s *Store) ValidateChannelIDs(ctx context.Context, channelIDs []int64) error {
	ids := dedupeIDs(channelIDs)
	if len(ids) == 0 {
		return nil
	}
	var found int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM channels WHERE id = ANY($1)`, ids).Scan(&found); err != nil {
		return err
	}
	if found != len(ids) {
		return ErrNotFound
	}
	return nil
}

// SetCaseChannels atomically replaces the case's channel subscriptions.
// Every id must refer to an existing channel (ErrNotFound otherwise).
func (s *Store) SetCaseChannels(ctx context.Context, caseID int64, channelIDs []int64) error {
	ids := dedupeIDs(channelIDs)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if len(ids) > 0 {
		var found int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM channels WHERE id = ANY($1)`, ids).Scan(&found); err != nil {
			return err
		}
		if found != len(ids) {
			return ErrNotFound
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM case_channel_subs WHERE case_id = $1`, caseID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx,
			`INSERT INTO case_channel_subs (case_id, channel_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			caseID, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func dedupeIDs(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := ids[:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
