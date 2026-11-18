package store

import (
	"context"
	"database/sql"
)

// CronJob runs a command in a service's container on a schedule.
type CronJob struct {
	ID         int64  `json:"id"`
	AppID      int64  `json:"appId"`
	Name       string `json:"name"`
	Schedule   string `json:"schedule"`
	Command    string `json:"command"`
	Timeout    int    `json:"timeout"` // seconds
	Enabled    bool   `json:"enabled"`
	NextRunAt  *int64 `json:"nextRunAt"`
	CreatedAt  int64  `json:"createdAt"`
	LastStatus string `json:"lastStatus"`
	LastRunAt  *int64 `json:"lastRunAt"`
	LastRunID  int64  `json:"lastRunId"`
}

// CronRun is one run of a cron job.
type CronRun struct {
	ID         int64  `json:"id"`
	JobID      int64  `json:"jobId"`
	Status     string `json:"status"`  // running|succeeded|failed|skipped
	Trigger    string `json:"trigger"` // schedule|manual|api
	ExitCode   *int   `json:"exitCode"`
	Output     string `json:"output,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	FinishedAt *int64 `json:"finishedAt"`
}

const cronCols = `j.id,j.app_id,j.name,j.schedule,j.command,j.timeout,j.enabled,j.next_run_at,j.created_at,
	COALESCE(r.status,''),r.created_at,COALESCE(r.id,0)`

const cronFrom = ` FROM cron_jobs j LEFT JOIN cron_runs r ON r.id=(SELECT MAX(id) FROM cron_runs WHERE job_id=j.id)`

func scanCronJob(r scanner) (*CronJob, error) {
	var j CronJob
	err := r.Scan(&j.ID, &j.AppID, &j.Name, &j.Schedule, &j.Command, &j.Timeout, &j.Enabled, &j.NextRunAt, &j.CreatedAt,
		&j.LastStatus, &j.LastRunAt, &j.LastRunID)
	if err != nil {
		return nil, mapErr(err)
	}
	return &j, nil
}

func (s *Store) queryCronJobs(ctx context.Context, where string, args ...any) ([]CronJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cronCols+cronFrom+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CronJob{}
	for rows.Next() {
		j, err := scanCronJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// CronJobs lists a service's jobs.
func (s *Store) CronJobs(ctx context.Context, appID int64) ([]CronJob, error) {
	return s.queryCronJobs(ctx, ` WHERE j.app_id=? ORDER BY j.name, j.id`, appID)
}

// DueCronJobs lists enabled jobs whose next run is at or before t.
func (s *Store) DueCronJobs(ctx context.Context, t int64) ([]CronJob, error) {
	return s.queryCronJobs(ctx, ` WHERE j.enabled=1 AND (j.next_run_at IS NULL OR j.next_run_at<=?) ORDER BY j.next_run_at`, t)
}

func (s *Store) CronJob(ctx context.Context, id int64) (*CronJob, error) {
	return scanCronJob(s.db.QueryRowContext(ctx, `SELECT `+cronCols+cronFrom+` WHERE j.id=?`, id))
}

func (s *Store) CreateCronJob(ctx context.Context, j *CronJob) error {
	j.CreatedAt = now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO cron_jobs(app_id,name,schedule,command,timeout,enabled,next_run_at,created_at)
		VALUES (?,?,?,?,?,?,?,?)`, j.AppID, j.Name, j.Schedule, j.Command, j.Timeout, j.Enabled, j.NextRunAt, j.CreatedAt)
	if err != nil {
		return mapErr(err)
	}
	j.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateCronJob(ctx context.Context, j *CronJob) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cron_jobs SET name=?,schedule=?,command=?,timeout=?,enabled=?,next_run_at=? WHERE id=?`,
		j.Name, j.Schedule, j.Command, j.Timeout, j.Enabled, j.NextRunAt, j.ID)
	return mapErr(err)
}

// SetCronNext stores a job's next run time (nil when it never runs again).
func (s *Store) SetCronNext(ctx context.Context, id int64, next *int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cron_jobs SET next_run_at=? WHERE id=?`, next, id)
	return err
}

func (s *Store) DeleteCronJob(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM cron_jobs WHERE id=?`, id)
	return err
}

// CountCronJobs counts a service's jobs.
func (s *Store) CountCronJobs(ctx context.Context, appID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cron_jobs WHERE app_id=?`, appID).Scan(&n)
	return n, err
}

func (s *Store) CreateCronRun(ctx context.Context, jobID int64, status, trigger string) (*CronRun, error) {
	t := now()
	r := &CronRun{JobID: jobID, Status: status, Trigger: trigger, CreatedAt: t}
	var fin any
	if status != "running" {
		fin = t
		r.FinishedAt = &t
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO cron_runs(job_id,status,trigger,created_at,finished_at) VALUES (?,?,?,?,?)`,
		jobID, status, trigger, t, fin)
	if err != nil {
		return nil, err
	}
	r.ID, _ = res.LastInsertId()
	return r, nil
}

// cronRunsKeep is how many runs are kept per job.
const cronRunsKeep = 50

func (s *Store) FinishCronRun(ctx context.Context, id int64, status string, exitCode *int, output string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cron_runs SET status=?,exit_code=?,output=?,finished_at=? WHERE id=?`,
		status, exitCode, output, now(), id)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM cron_runs WHERE job_id=(SELECT job_id FROM cron_runs WHERE id=?)
		AND id NOT IN (SELECT id FROM cron_runs WHERE job_id=(SELECT job_id FROM cron_runs WHERE id=?) ORDER BY id DESC LIMIT ?)`,
		id, id, cronRunsKeep)
	return err
}

// UpdateCronOutput saves the output so far of a running job.
func (s *Store) UpdateCronOutput(ctx context.Context, id int64, output string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cron_runs SET output=? WHERE id=? AND status='running'`, output, id)
	return err
}

func (s *Store) CronRuns(ctx context.Context, jobID int64, limit int) ([]CronRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,job_id,status,trigger,exit_code,created_at,finished_at
		FROM cron_runs WHERE job_id=? ORDER BY id DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CronRun{}
	for rows.Next() {
		var r CronRun
		var code sql.NullInt64
		if err := rows.Scan(&r.ID, &r.JobID, &r.Status, &r.Trigger, &code, &r.CreatedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		if code.Valid {
			c := int(code.Int64)
			r.ExitCode = &c
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) CronRun(ctx context.Context, id int64) (*CronRun, error) {
	var r CronRun
	var code sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id,job_id,status,trigger,exit_code,output,created_at,finished_at FROM cron_runs WHERE id=?`, id).
		Scan(&r.ID, &r.JobID, &r.Status, &r.Trigger, &code, &r.Output, &r.CreatedAt, &r.FinishedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	if code.Valid {
		c := int(code.Int64)
		r.ExitCode = &c
	}
	return &r, nil
}

// FailStaleCronRuns marks runs interrupted by a restart as failed.
func (s *Store) FailStaleCronRuns(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cron_runs SET status='failed', output=output||char(10)||'[interrupted: SimplePanel restarted]',
		finished_at=? WHERE status='running'`, now())
	return err
}
