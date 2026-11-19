package store

import (
	"context"
	"os"
)

// Snapshot writes a consistent copy of the database to path (which must not
// exist yet). It's taken before the panel updates itself, so a release
// whose migrations go wrong can be undone by hand.
func (s *Store) Snapshot(ctx context.Context, path string) error {
	_ = os.Remove(path)
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}

// ActiveJobs counts deployments, backups, restores and cron runs that are
// still running. Restarting the panel interrupts them.
func (s *Store) ActiveJobs(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM deployments WHERE status IN ('queued','running')) +
		(SELECT COUNT(*) FROM backups WHERE status='running') +
		(SELECT COUNT(*) FROM restores WHERE status='running') +
		(SELECT COUNT(*) FROM cron_runs WHERE status='running')`).Scan(&n)
	return n, err
}
