package state

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ForegroundActivity is an unpartitioned, read-only view used only to admit
// optional background work. It never claims work or changes recovery state.
type ForegroundActivity struct {
	Busy           bool
	LastForeground time.Time
}

// OpenForActivityInspection opens an existing schema-19 or current-schema
// database without migration, Host locking, or file-memory initialization.
// Schema 20 only adds Dream tables; foreground activity uses unchanged tables.
func OpenForActivityInspection(home string) (*Store, error) {
	return open(home, true, false, false, false, true)
}

// ForegroundActivity reports queued/claimed channel work and running Host runs
// across every Agent and audience in this state home.
func (s *Store) ForegroundActivity(ctx context.Context) (ForegroundActivity, error) {
	var result ForegroundActivity
	var queued, running int
	if err := s.db.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM channel_messages WHERE status IN ('queued','claimed')),
		EXISTS(SELECT 1 FROM runs WHERE execution_status='running')`).Scan(&queued, &running); err != nil {
		return result, fmt.Errorf("inspect foreground work: %w", err)
	}
	result.Busy = queued != 0 || running != 0
	var received, created, completed string
	if err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT MAX(received_at) FROM channel_messages),''),
		COALESCE((SELECT MAX(created_at) FROM runs),''),
		COALESCE((SELECT MAX(json_extract(CAST(payload AS TEXT),'$.completed_at')) FROM runs),'')`).Scan(&received, &created, &completed); err != nil {
		return result, fmt.Errorf("inspect foreground time: %w", err)
	}
	for _, value := range []string{received, created, completed} {
		if value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return result, errors.New("foreground activity has an invalid timestamp")
		}
		if parsed.After(result.LastForeground) {
			result.LastForeground = parsed.UTC()
		}
	}
	return result, nil
}
