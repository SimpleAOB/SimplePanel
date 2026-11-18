package store

import (
	"context"
	"database/sql"
)

// AppRun is one launch of a service: a single Swarm task, which is one
// container start. Its logs are copied into the database once it stops, so
// they can still be read after Docker has forgotten the task.
type AppRun struct {
	ID           int64  `json:"id"`
	AppID        int64  `json:"appId"`
	TaskID       string `json:"taskId"`
	Number       int64  `json:"number"` // 1, 2, 3... per service
	Slot         int    `json:"slot"`   // replica number
	DeploymentID *int64 `json:"deploymentId"`
	Image        string `json:"image"`
	State        string `json:"state"` // Swarm task state: running, failed, shutdown...
	Message      string `json:"message,omitempty"`
	ExitCode     *int   `json:"exitCode"`
	LogBytes     int64  `json:"logBytes"`
	Truncated    bool   `json:"truncated"`
	Archived     bool   `json:"archived"` // logs are stored; Docker is no longer asked
	StartedAt    int64  `json:"startedAt"`
	EndedAt      *int64 `json:"endedAt"`
	Log          string `json:"-"`
}

const runCols = `id,app_id,task_id,number,slot,deployment_id,image,state,message,exit_code,log_bytes,truncated,archived,started_at,ended_at`

func scanRun(r scanner, withLog bool) (*AppRun, error) {
	var x AppRun
	dest := []any{&x.ID, &x.AppID, &x.TaskID, &x.Number, &x.Slot, &x.DeploymentID, &x.Image, &x.State, &x.Message,
		&x.ExitCode, &x.LogBytes, &x.Truncated, &x.Archived, &x.StartedAt, &x.EndedAt}
	if withLog {
		dest = append(dest, &x.Log)
	}
	if err := r.Scan(dest...); err != nil {
		return nil, mapErr(err)
	}
	return &x, nil
}

// AppRuns lists a service's launches, newest first, without their logs.
func (s *Store) AppRuns(ctx context.Context, appID int64, limit int) ([]AppRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runCols+` FROM app_runs WHERE app_id=? ORDER BY number DESC LIMIT ?`, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AppRun{}
	for rows.Next() {
		r, err := scanRun(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// AppRun returns one launch of a service by its number, with its stored log.
func (s *Store) AppRun(ctx context.Context, appID, number int64) (*AppRun, error) {
	return scanRun(s.db.QueryRowContext(ctx, `SELECT `+runCols+`,log FROM app_runs WHERE app_id=? AND number=?`, appID, number), true)
}

// AppRunByTask returns the launch recorded for a Swarm task, without its log.
func (s *Store) AppRunByTask(ctx context.Context, taskID string) (*AppRun, error) {
	return scanRun(s.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM app_runs WHERE task_id=?`, taskID), false)
}

// CreateAppRun records a new launch and gives it the service's next number.
func (s *Store) CreateAppRun(ctx context.Context, r *AppRun) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number),0)+1 FROM app_runs WHERE app_id=?`, r.AppID).Scan(&r.Number); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO app_runs(app_id,task_id,number,slot,deployment_id,image,state,message,exit_code,started_at,ended_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, r.AppID, r.TaskID, r.Number, r.Slot, r.DeploymentID, r.Image, r.State, r.Message,
		r.ExitCode, r.StartedAt, r.EndedAt)
	if err != nil {
		return mapErr(err)
	}
	r.ID, _ = res.LastInsertId()
	return tx.Commit()
}

// UpdateAppRunState stores a launch's latest task state.
func (s *Store) UpdateAppRunState(ctx context.Context, r *AppRun) error {
	_, err := s.db.ExecContext(ctx, `UPDATE app_runs SET state=?,message=?,exit_code=?,ended_at=? WHERE id=?`,
		r.State, r.Message, r.ExitCode, r.EndedAt, r.ID)
	return err
}

// ArchiveAppRun stores a stopped launch's logs.
func (s *Store) ArchiveAppRun(ctx context.Context, id int64, log string, truncated bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE app_runs SET log=?,log_bytes=?,truncated=?,archived=1 WHERE id=?`,
		log, len(log), truncated, id)
	return err
}

// PruneAppRuns keeps a service's newest `keep` launches. Launches that are
// still running are never removed.
func (s *Store) PruneAppRuns(ctx context.Context, appID int64, keep int) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_runs WHERE app_id=? AND archived=1 AND number <= (
		SELECT COALESCE(MAX(number),0) - ? FROM app_runs WHERE app_id=?)`, appID, keep, appID)
	return err
}

// DeploymentBefore returns the id of the service's newest deployment that
// started at or before t, so a launch can say which deploy created it.
func (s *Store) DeploymentBefore(ctx context.Context, appID, t int64) (*int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM deployments WHERE app_id=? AND created_at<=? ORDER BY id DESC LIMIT 1`, appID, t).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}
