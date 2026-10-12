package state

import "context"

// DatabaseSchemaVersion reads the existing schema without starting migration.
func (s *Store) DatabaseSchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	return version, err
}
