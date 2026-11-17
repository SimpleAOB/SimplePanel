package store

import (
	"context"
	"database/sql"
)

// BackupDestination is an S3-compatible bucket backups are uploaded to.
// SecretKey and Passphrase never leave the server.
type BackupDestination struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Endpoint   string `json:"endpoint"` // empty = AWS S3
	Region     string `json:"region"`
	Bucket     string `json:"bucket"`
	Prefix     string `json:"prefix"`
	AccessKey  string `json:"accessKey"`
	SecretKey  string `json:"-"`
	PathStyle  bool   `json:"pathStyle"`
	Passphrase string `json:"-"` // non-empty: backups are encrypted with age
	Encrypted  bool   `json:"encrypted"`
	CreatedAt  int64  `json:"createdAt"`
}

const destCols = `id,name,endpoint,region,bucket,prefix,access_key,secret_key,path_style,passphrase,created_at`

func scanDest(r scanner) (*BackupDestination, error) {
	var d BackupDestination
	if err := r.Scan(&d.ID, &d.Name, &d.Endpoint, &d.Region, &d.Bucket, &d.Prefix, &d.AccessKey, &d.SecretKey,
		&d.PathStyle, &d.Passphrase, &d.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	d.Encrypted = d.Passphrase != ""
	return &d, nil
}

func (s *Store) BackupDestinations(ctx context.Context) ([]BackupDestination, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+destCols+` FROM backup_destinations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupDestination{}
	for rows.Next() {
		d, err := scanDest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *Store) BackupDestination(ctx context.Context, id int64) (*BackupDestination, error) {
	return scanDest(s.db.QueryRowContext(ctx, `SELECT `+destCols+` FROM backup_destinations WHERE id=?`, id))
}

func (s *Store) SaveBackupDestination(ctx context.Context, d *BackupDestination) error {
	d.Encrypted = d.Passphrase != ""
	if d.ID == 0 {
		d.CreatedAt = now()
		res, err := s.db.ExecContext(ctx, `INSERT INTO backup_destinations(name,endpoint,region,bucket,prefix,access_key,secret_key,path_style,passphrase,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, d.Name, d.Endpoint, d.Region, d.Bucket, d.Prefix, d.AccessKey, d.SecretKey, d.PathStyle, d.Passphrase, d.CreatedAt)
		if err != nil {
			return mapErr(err)
		}
		d.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE backup_destinations SET name=?,endpoint=?,region=?,bucket=?,prefix=?,access_key=?,secret_key=?,path_style=?,passphrase=?
		WHERE id=?`, d.Name, d.Endpoint, d.Region, d.Bucket, d.Prefix, d.AccessKey, d.SecretKey, d.PathStyle, d.Passphrase, d.ID)
	return mapErr(err)
}

func (s *Store) DeleteBackupDestination(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM backup_destinations WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// BackupJob is a schedule that backs up a service (or, with AppID nil,
// SimplePanel's own configuration) to a destination.
type BackupJob struct {
	ID            int64  `json:"id"`
	AppID         *int64 `json:"appId"`
	DestinationID int64  `json:"destinationId"`
	Destination   string `json:"destination"`
	Schedule      string `json:"schedule"` // cron expression
	Keep          int    `json:"keep"`     // scheduled backups to keep
	Enabled       bool   `json:"enabled"`
	NextRunAt     *int64 `json:"nextRunAt"`
	CreatedAt     int64  `json:"createdAt"`
}

const jobCols = `j.id,j.app_id,j.destination_id,d.name,j.schedule,j.keep,j.enabled,j.next_run_at,j.created_at`

func scanJob(r scanner) (*BackupJob, error) {
	var j BackupJob
	var app sql.NullInt64
	if err := r.Scan(&j.ID, &app, &j.DestinationID, &j.Destination, &j.Schedule, &j.Keep, &j.Enabled, &j.NextRunAt, &j.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	if app.Valid {
		j.AppID = &app.Int64
	}
	return &j, nil
}

func (s *Store) queryJobs(ctx context.Context, where string, args ...any) ([]BackupJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobCols+` FROM backup_jobs j JOIN backup_destinations d ON d.id=j.destination_id `+where+` ORDER BY j.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupJob{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// BackupJobs lists the jobs of an app; appID 0 lists the panel's own jobs.
func (s *Store) BackupJobs(ctx context.Context, appID int64) ([]BackupJob, error) {
	if appID == 0 {
		return s.queryJobs(ctx, `WHERE j.app_id IS NULL`)
	}
	return s.queryJobs(ctx, `WHERE j.app_id=?`, appID)
}

// AllBackupJobs lists every job.
func (s *Store) AllBackupJobs(ctx context.Context) ([]BackupJob, error) {
	return s.queryJobs(ctx, ``)
}

// DueBackupJobs lists enabled jobs whose next run is at or before t.
func (s *Store) DueBackupJobs(ctx context.Context, t int64) ([]BackupJob, error) {
	return s.queryJobs(ctx, `WHERE j.enabled=1 AND j.next_run_at IS NOT NULL AND j.next_run_at<=?`, t)
}

func (s *Store) BackupJob(ctx context.Context, id int64) (*BackupJob, error) {
	return scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM backup_jobs j JOIN backup_destinations d ON d.id=j.destination_id WHERE j.id=?`, id))
}

func (s *Store) SaveBackupJob(ctx context.Context, j *BackupJob) error {
	if j.ID == 0 {
		j.CreatedAt = now()
		res, err := s.db.ExecContext(ctx, `INSERT INTO backup_jobs(app_id,destination_id,schedule,keep,enabled,next_run_at,created_at) VALUES (?,?,?,?,?,?,?)`,
			j.AppID, j.DestinationID, j.Schedule, j.Keep, j.Enabled, j.NextRunAt, j.CreatedAt)
		if err != nil {
			return mapErr(err)
		}
		j.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE backup_jobs SET destination_id=?,schedule=?,keep=?,enabled=?,next_run_at=? WHERE id=?`,
		j.DestinationID, j.Schedule, j.Keep, j.Enabled, j.NextRunAt, j.ID)
	return mapErr(err)
}

func (s *Store) SetJobNextRun(ctx context.Context, id int64, next *int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backup_jobs SET next_run_at=? WHERE id=?`, next, id)
	return err
}

func (s *Store) DeleteBackupJob(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM backup_jobs WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Backup is one backup run and the object it produced.
type Backup struct {
	ID            int64  `json:"id"`
	AppID         *int64 `json:"appId"`
	JobID         *int64 `json:"jobId"`
	DestinationID int64  `json:"destinationId"`
	Destination   string `json:"destination"`
	Project       string `json:"project"`
	Service       string `json:"service"`
	Kind          string `json:"kind"` // postgres|mysql|mariadb|volumes|panel
	Key           string `json:"key"`
	Size          int64  `json:"size"`
	Encrypted     bool   `json:"encrypted"`
	Status        string `json:"status"` // running|succeeded|failed
	Trigger       string `json:"trigger"`
	Log           string `json:"log,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
	FinishedAt    *int64 `json:"finishedAt"`
}

const backupCols = `b.id,b.app_id,b.job_id,b.destination_id,COALESCE(d.name,''),b.project,b.service,b.kind,b.object_key,b.size,b.encrypted,
	b.status,b.trigger,b.created_at,b.finished_at`

func scanBackup(r scanner, withLog bool) (*Backup, error) {
	var b Backup
	var app, job sql.NullInt64
	dst := []any{&b.ID, &app, &job, &b.DestinationID, &b.Destination, &b.Project, &b.Service, &b.Kind, &b.Key, &b.Size, &b.Encrypted,
		&b.Status, &b.Trigger, &b.CreatedAt, &b.FinishedAt}
	if withLog {
		dst = append(dst, &b.Log)
	}
	if err := r.Scan(dst...); err != nil {
		return nil, mapErr(err)
	}
	if app.Valid {
		b.AppID = &app.Int64
	}
	if job.Valid {
		b.JobID = &job.Int64
	}
	return &b, nil
}

func (s *Store) CreateBackup(ctx context.Context, b *Backup) error {
	b.CreatedAt = now()
	b.Status = "running"
	res, err := s.db.ExecContext(ctx, `INSERT INTO backups(app_id,job_id,destination_id,project,service,kind,object_key,encrypted,status,trigger,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, b.AppID, b.JobID, b.DestinationID, b.Project, b.Service, b.Kind, b.Key, b.Encrypted, b.Status, b.Trigger, b.CreatedAt)
	if err != nil {
		return err
	}
	b.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) FinishBackup(ctx context.Context, id int64, status, key string, size int64, log string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backups SET status=?,object_key=?,size=?,log=?,finished_at=? WHERE id=?`, status, key, size, log, now(), id)
	return err
}

func (s *Store) UpdateBackupLog(ctx context.Context, id int64, log string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backups SET log=? WHERE id=?`, log, id)
	return err
}

// Backups lists an app's backups, newest first; appID 0 lists the panel's.
func (s *Store) Backups(ctx context.Context, appID int64, limit int) ([]Backup, error) {
	q := `SELECT ` + backupCols + ` FROM backups b LEFT JOIN backup_destinations d ON d.id=b.destination_id `
	var rows *sql.Rows
	var err error
	if appID == 0 {
		rows, err = s.db.QueryContext(ctx, q+`WHERE b.kind='panel' ORDER BY b.id DESC LIMIT ?`, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, q+`WHERE b.app_id=? ORDER BY b.id DESC LIMIT ?`, appID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Backup{}
	for rows.Next() {
		b, err := scanBackup(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// RecentBackups lists the latest backups of every service.
func (s *Store) RecentBackups(ctx context.Context, limit int) ([]Backup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+backupCols+` FROM backups b LEFT JOIN backup_destinations d ON d.id=b.destination_id
		ORDER BY b.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Backup{}
	for rows.Next() {
		b, err := scanBackup(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func (s *Store) Backup(ctx context.Context, id int64) (*Backup, error) {
	return scanBackup(s.db.QueryRowContext(ctx, `SELECT `+backupCols+`,b.log FROM backups b LEFT JOIN backup_destinations d ON d.id=b.destination_id
		WHERE b.id=?`, id), true)
}

// ExpiredJobBackups returns a job's successful backups beyond the newest keep.
func (s *Store) ExpiredJobBackups(ctx context.Context, jobID int64, keep int) ([]Backup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+backupCols+` FROM backups b LEFT JOIN backup_destinations d ON d.id=b.destination_id
		WHERE b.job_id=? AND b.status='succeeded' ORDER BY b.id DESC LIMIT -1 OFFSET ?`, jobID, keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Backup{}
	for rows.Next() {
		b, err := scanBackup(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func (s *Store) DeleteBackupRecord(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM backups WHERE id=?`, id)
	return err
}

// FailStaleBackups marks backups and restores interrupted by a restart.
func (s *Store) FailStaleBackups(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE backups SET status='failed', log=log||char(10)||'[interrupted: SimplePanel restarted]'||char(10),
		finished_at=? WHERE status='running'`, now()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE restores SET status='failed', log=log||char(10)||'[interrupted: SimplePanel restarted]'||char(10),
		finished_at=? WHERE status='running'`, now())
	return err
}

// Restore is one restore run.
type Restore struct {
	ID            int64  `json:"id"`
	AppID         *int64 `json:"appId"`
	TargetAppID   *int64 `json:"targetAppId"`
	DestinationID *int64 `json:"destinationId"`
	Key           string `json:"key"`
	Status        string `json:"status"`
	Log           string `json:"log,omitempty"`
	Username      string `json:"username"`
	CreatedAt     int64  `json:"createdAt"`
	FinishedAt    *int64 `json:"finishedAt"`
}

func (s *Store) CreateRestore(ctx context.Context, r *Restore) error {
	r.CreatedAt = now()
	r.Status = "running"
	res, err := s.db.ExecContext(ctx, `INSERT INTO restores(app_id,target_app_id,destination_id,object_key,status,username,created_at) VALUES (?,?,?,?,?,?,?)`,
		r.AppID, r.TargetAppID, r.DestinationID, r.Key, r.Status, r.Username, r.CreatedAt)
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateRestore(ctx context.Context, id int64, status, log string, done bool) error {
	var fin any
	if done {
		fin = now()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE restores SET status=?,log=?,finished_at=COALESCE(?,finished_at) WHERE id=?`, status, log, fin, id)
	return err
}

func (s *Store) SetRestoreTarget(ctx context.Context, id, appID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE restores SET target_app_id=? WHERE id=?`, appID, id)
	return err
}

const restoreCols = `id,app_id,target_app_id,destination_id,object_key,status,username,created_at,finished_at`

func scanRestore(r scanner, withLog bool) (*Restore, error) {
	var x Restore
	var a, t, d sql.NullInt64
	dst := []any{&x.ID, &a, &t, &d, &x.Key, &x.Status, &x.Username, &x.CreatedAt, &x.FinishedAt}
	if withLog {
		dst = append(dst, &x.Log)
	}
	if err := r.Scan(dst...); err != nil {
		return nil, mapErr(err)
	}
	if a.Valid {
		x.AppID = &a.Int64
	}
	if t.Valid {
		x.TargetAppID = &t.Int64
	}
	if d.Valid {
		x.DestinationID = &d.Int64
	}
	return &x, nil
}

func (s *Store) Restore(ctx context.Context, id int64) (*Restore, error) {
	return scanRestore(s.db.QueryRowContext(ctx, `SELECT `+restoreCols+`,log FROM restores WHERE id=?`, id), true)
}

// Restores lists restores that read from or wrote to an app.
func (s *Store) Restores(ctx context.Context, appID int64, limit int) ([]Restore, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+restoreCols+` FROM restores WHERE app_id=? OR target_app_id=? ORDER BY id DESC LIMIT ?`, appID, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Restore{}
	for rows.Next() {
		r, err := scanRestore(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// SnapshotTo writes a consistent copy of the database to path (for the
// panel's own configuration backup).
func (s *Store) SnapshotTo(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}
