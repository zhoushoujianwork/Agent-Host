package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const schemaOwnerAnsweringV17 = `
CREATE TABLE IF NOT EXISTS owner_answering(key TEXT PRIMARY KEY, revision INTEGER NOT NULL, payload BLOB NOT NULL);
PRAGMA user_version=17;
`

// OwnerAnsweringRecord keeps intake, execution and delivery claims in one durable
// revision. It is Host control state, not reusable memory or channel permission.
type OwnerAnsweringRecord struct {
	Key      string
	Revision int64
	Payload  json.RawMessage
}

func (s *Store) OwnerAnsweringRecord(ctx context.Context, key string) (OwnerAnsweringRecord, error) {
	r := OwnerAnsweringRecord{Key: key}
	err := s.db.QueryRowContext(ctx, "SELECT revision,payload FROM owner_answering WHERE key=?", key).Scan(&r.Revision, &r.Payload)
	return r, err
}

func (s *Store) OwnerAnsweringRecords(ctx context.Context) ([]OwnerAnsweringRecord, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key,revision,payload FROM owner_answering ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []OwnerAnsweringRecord
	for rows.Next() {
		var r OwnerAnsweringRecord
		if err := rows.Scan(&r.Key, &r.Revision, &r.Payload); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// CompareAndSaveOwnerAnswering reserves an effect before execution. Revision
// zero inserts only; competing callers cannot both claim a task or delivery.
func (s *Store) CompareAndSaveOwnerAnswering(ctx context.Context, r *OwnerAnsweringRecord) error {
	return s.CompareAndSaveOwnerAnsweringBatch(ctx, r)
}

// CompareAndSaveOwnerAnsweringBatch commits the source watermark, active link
// and question revision together before any model/tool execution.
func (s *Store) CompareAndSaveOwnerAnsweringBatch(ctx context.Context, records ...*OwnerAnsweringRecord) error {
	seen := map[string]bool{}
	for _, r := range records {
		if s.readOnly || r == nil || r.Key == "" || seen[r.Key] || !json.Valid(r.Payload) {
			return errors.New("invalid owner answering write")
		}
		seen[r.Key] = true
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		for _, r := range records {
			var result sql.Result
			var err error
			if r.Revision == 0 {
				result, err = tx.ExecContext(ctx, "INSERT INTO owner_answering VALUES(?,1,?) ON CONFLICT(key) DO NOTHING", r.Key, r.Payload)
			} else {
				result, err = tx.ExecContext(ctx, "UPDATE owner_answering SET revision=revision+1,payload=? WHERE key=? AND revision=?", r.Payload, r.Key, r.Revision)
			}
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return errors.New("owner answering revision conflict")
			}
		}
		return nil
	})
	if err == nil {
		for _, r := range records {
			r.Revision++
		}
	}
	return err
}
