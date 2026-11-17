// Package backup runs scheduled and on-demand backups of databases, app
// volumes and SimplePanel's own configuration to S3-compatible storage,
// and restores them.
//
// Backups are streamed: a database dump (or a tar of an app's volumes)
// is gzip-compressed, optionally encrypted with age using the
// destination's passphrase, written to a temporary file and uploaded.
// Encrypted backups can be decrypted without SimplePanel using the age
// command-line tool (`age -d backup.sql.gz.age > backup.sql.gz`).
package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"filippo.io/age"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/notify"
	"github.com/SimpleAOB/SimplePanel/internal/s3"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Service runs backups and restores.
type Service struct {
	store  *store.Store
	deploy *deploy.Manager
	notify *notify.Notifier
	log    *slog.Logger
	tmpDir string

	sem   chan struct{} // limits concurrent backups and restores
	mu    sync.Mutex
	busy  map[int64]bool // app id (0 = panel) with a backup or restore running
	clock func() time.Time
}

func New(st *store.Store, dm *deploy.Manager, n *notify.Notifier, log *slog.Logger, tmpDir string) *Service {
	return &Service{store: st, deploy: dm, notify: n, log: log, tmpDir: tmpDir,
		sem: make(chan struct{}, 2), busy: map[int64]bool{}, clock: time.Now}
}

// Client returns an S3 client for a destination.
func Client(d *store.BackupDestination) (*s3.Client, error) {
	return s3.New(s3.Config{Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket,
		AccessKey: d.AccessKey, SecretKey: d.SecretKey, PathStyle: d.PathStyle})
}

func joinKey(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p = strings.Trim(p, "/"); p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, "/")
}

// Prefix is the folder a service's backups go to in a destination. app nil
// means SimplePanel's own configuration.
func Prefix(d *store.BackupDestination, app *store.App) string {
	if app == nil {
		return joinKey(d.Prefix, "_simplepanel") + "/"
	}
	return joinKey(d.Prefix, app.ProjectName, app.Name) + "/"
}

func extension(kind string) string {
	switch kind {
	case "volumes":
		return ".tar.gz"
	case "panel":
		return ".db.gz"
	}
	return ".sql.gz"
}

// KindFromKey reads the backup kind from an object name such as
// 20261117-030000-postgres.sql.gz.age.
func KindFromKey(key string) string {
	base := path.Base(key)
	base = strings.TrimSuffix(base, ".age")
	for _, k := range []string{"postgres", "mysql", "mariadb", "volumes", "panel"} {
		if strings.HasSuffix(base, "-"+k+extension(k)) {
			return k
		}
	}
	return ""
}

// Compatible reports whether a backup of kind can be restored into app.
func Compatible(kind string, app *store.App) bool {
	switch kind {
	case "postgres":
		return app.Kind == "postgres"
	case "mysql", "mariadb":
		return app.Kind == "mysql" || app.Kind == "mariadb"
	case "volumes":
		return !app.IsDatabase() && len(app.Volumes) > 0
	}
	return false
}

// NextRun returns the next run time of a cron schedule after t.
func NextRun(schedule string, t time.Time) (*int64, error) {
	s, err := ParseSchedule(schedule)
	if err != nil {
		return nil, err
	}
	n := s.Next(t)
	if n.IsZero() {
		return nil, errors.New("this schedule never runs")
	}
	u := n.Unix()
	return &u, nil
}

// logBuf collects a run's log.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuf) Printf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buf.Len() < 256<<10 {
		fmt.Fprintf(&l.buf, time.Now().UTC().Format("15:04:05")+"  "+format+"\n", a...)
	}
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buf.Len() < 256<<10 {
		l.buf.Write(p)
	}
	return len(p), nil
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (s *Service) lock(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[id] {
		return false
	}
	s.busy[id] = true
	return true
}

func (s *Service) unlock(id int64) {
	s.mu.Lock()
	delete(s.busy, id)
	s.mu.Unlock()
}

// ErrBusy is returned when a backup or restore of the same service is running.
var ErrBusy = errors.New("a backup or restore of this service is already running; try again when it finishes")

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Run starts a backup of app (nil = SimplePanel's configuration) to dest
// in the background and returns its record.
func (s *Service) Run(ctx context.Context, app *store.App, dest *store.BackupDestination, jobID *int64, trigger string) (*store.Backup, error) {
	kind := "panel"
	var lockID int64
	b := &store.Backup{DestinationID: dest.ID, Destination: dest.Name, JobID: jobID, Trigger: trigger, Encrypted: dest.Passphrase != ""}
	if app != nil {
		kind = deploy.BackupKind(app)
		if kind == "" {
			return nil, errors.New("there's nothing to back up: this app has no volumes")
		}
		lockID = app.ID
		b.AppID, b.Project, b.Service = &app.ID, app.ProjectName, app.Name
	}
	b.Kind = kind
	if !s.lock(lockID) {
		return nil, ErrBusy
	}
	ts := s.clock().UTC()
	b.Key = Prefix(dest, app) + ts.Format("20060102-150405") + "-" + kind + extension(kind)
	if b.Encrypted {
		b.Key += ".age"
	}
	if err := s.store.CreateBackup(ctx, b); err != nil {
		s.unlock(lockID)
		return nil, err
	}
	go func() {
		defer s.unlock(lockID)
		s.sem <- struct{}{}
		defer func() { <-s.sem }()
		s.runBackup(app, dest, b)
	}()
	return b, nil
}

func (s *Service) runBackup(app *store.App, dest *store.BackupDestination, b *store.Backup) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	lb := &logBuf{}
	what := "SimplePanel's configuration"
	if app != nil {
		what = app.ProjectName + "/" + app.Name
	}
	lb.Printf("Backing up %s to %s (%s)", what, dest.Name, b.Key)
	size, err := s.backup(ctx, app, dest, b, lb)
	status := "succeeded"
	if err != nil {
		status = "failed"
		lb.Printf("Backup failed: %v", err)
	} else {
		lb.Printf("Done: %s uploaded", humanSize(size))
	}
	if ferr := s.store.FinishBackup(context.Background(), b.ID, status, b.Key, size, lb.String()); ferr != nil {
		s.log.Error("saving backup record", "err", ferr)
	}
	if err == nil && b.JobID != nil {
		s.prune(ctx, *b.JobID, dest, lb)
	}
	ev := notify.Event{Type: notify.BackupSucceeded, Level: "success", Title: "Backup of " + what + " succeeded",
		Message: fmt.Sprintf("%s uploaded to %s.", humanSize(size), dest.Name)}
	if err != nil {
		ev = notify.Event{Type: notify.BackupFailed, Level: "error", Title: "Backup of " + what + " failed", Message: err.Error()}
	}
	if app != nil {
		ev.ProjectID, ev.Project, ev.Service = app.ProjectID, app.ProjectName, app.Name
		ev.Link = fmt.Sprintf("/#/p/%d/a/%d/backups", app.ProjectID, app.ID)
	} else {
		ev.Link = "/#/settings/backups"
	}
	if s.notify != nil {
		s.notify.Send(ev)
	}
}

func (s *Service) backup(ctx context.Context, app *store.App, dest *store.BackupDestination, b *store.Backup, lb *logBuf) (int64, error) {
	client, err := Client(dest)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(s.tmpDir, 0o700); err != nil {
		return 0, err
	}
	f, err := os.CreateTemp(s.tmpDir, "backup-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()

	var w io.WriteCloser = nopCloser{f}
	if dest.Passphrase != "" {
		r, err := age.NewScryptRecipient(dest.Passphrase)
		if err != nil {
			return 0, err
		}
		if w, err = age.Encrypt(f, r); err != nil {
			return 0, err
		}
		lb.Printf("Encrypting with the destination's passphrase (age)")
	}
	gz, _ := gzip.NewWriterLevel(w, gzip.DefaultCompression)
	switch b.Kind {
	case "panel":
		snap := filepath.Join(s.tmpDir, fmt.Sprintf("snapshot-%d.db", b.ID))
		defer os.Remove(snap)
		if err := s.store.SnapshotTo(ctx, snap); err != nil {
			return 0, fmt.Errorf("snapshot of the configuration: %w", err)
		}
		sf, err := os.Open(snap)
		if err != nil {
			return 0, err
		}
		_, err = io.Copy(gz, sf)
		sf.Close()
		if err != nil {
			return 0, err
		}
	case "volumes":
		var names []string
		for _, v := range app.Volumes {
			names = append(names, v.Name)
		}
		lb.Printf("Archiving volumes: %s", strings.Join(names, ", "))
		if err := s.deploy.DumpVolumes(ctx, app, gz); err != nil {
			return 0, err
		}
	default:
		lb.Printf("Dumping the %s database", app.Kind)
		if err := s.deploy.DumpDatabase(ctx, app, gz); err != nil {
			return 0, err
		}
	}
	if err := gz.Close(); err != nil {
		return 0, err
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	if err := f.Sync(); err != nil {
		return 0, err
	}
	fi, _ := f.Stat()
	lb.Printf("Uploading %s", humanSize(fi.Size()))
	return client.UploadFile(ctx, b.Key, f.Name())
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// prune deletes a job's scheduled backups beyond its retention count.
func (s *Service) prune(ctx context.Context, jobID int64, dest *store.BackupDestination, lb *logBuf) {
	job, err := s.store.BackupJob(ctx, jobID)
	if err != nil || job.Keep <= 0 {
		return
	}
	old, err := s.store.ExpiredJobBackups(ctx, jobID, job.Keep)
	if err != nil || len(old) == 0 {
		return
	}
	client, err := Client(dest)
	if err != nil {
		return
	}
	for _, b := range old {
		if err := client.Delete(ctx, b.Key); err != nil {
			s.log.Warn("pruning old backup", "key", b.Key, "err", err)
			continue
		}
		_ = s.store.DeleteBackupRecord(ctx, b.ID)
	}
	s.log.Info("pruned old backups", "job", jobID, "deleted", len(old))
}

// Delete removes a backup's object and its record.
func (s *Service) Delete(ctx context.Context, b *store.Backup) error {
	dest, err := s.store.BackupDestination(ctx, b.DestinationID)
	if err != nil {
		return err
	}
	if b.Key != "" && b.Status == "succeeded" {
		client, err := Client(dest)
		if err != nil {
			return err
		}
		if err := client.Delete(ctx, b.Key); err != nil {
			return err
		}
	}
	return s.store.DeleteBackupRecord(ctx, b.ID)
}

// RemoteBackup is a backup object found in a destination's bucket.
type RemoteBackup struct {
	DestinationID int64     `json:"destinationId"`
	Destination   string    `json:"destination"`
	Key           string    `json:"key"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	Size          int64     `json:"size"`
	Encrypted     bool      `json:"encrypted"`
	Time          time.Time `json:"time"`
	BackupID      int64     `json:"backupId,omitempty"` // known to this panel
}

// ListRemote lists the backups of app (nil = SimplePanel's configuration)
// that exist in dest's bucket, newest first. It finds backups made by
// another SimplePanel too, as long as the project and service names match.
func (s *Service) ListRemote(ctx context.Context, dest *store.BackupDestination, app *store.App) ([]RemoteBackup, error) {
	client, err := Client(dest)
	if err != nil {
		return nil, err
	}
	objs, err := client.List(ctx, Prefix(dest, app), 1000)
	if err != nil {
		return nil, err
	}
	known := map[string]int64{}
	var appID int64
	if app != nil {
		appID = app.ID
	}
	if recs, err := s.store.Backups(ctx, appID, 1000); err == nil {
		for _, r := range recs {
			if r.DestinationID == dest.ID {
				known[r.Key] = r.ID
			}
		}
	}
	out := []RemoteBackup{}
	for _, o := range objs {
		kind := KindFromKey(o.Key)
		if kind == "" {
			continue // not a SimplePanel backup
		}
		out = append(out, RemoteBackup{DestinationID: dest.ID, Destination: dest.Name, Key: o.Key, Name: path.Base(o.Key),
			Kind: kind, Size: o.Size, Encrypted: strings.HasSuffix(o.Key, ".age"), Time: o.LastModified, BackupID: known[o.Key]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key > out[j].Key })
	return out, nil
}

// Open downloads a backup object and returns its gzip-compressed content,
// decrypting it if needed.
func (s *Service) Open(ctx context.Context, dest *store.BackupDestination, key string) (io.ReadCloser, error) {
	client, err := Client(dest)
	if err != nil {
		return nil, err
	}
	body, _, err := client.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(key, ".age") {
		return body, nil
	}
	if dest.Passphrase == "" {
		body.Close()
		return nil, errors.New("this backup is encrypted, but the destination has no passphrase; add the passphrase it was made with")
	}
	id, err := age.NewScryptIdentity(dest.Passphrase)
	if err != nil {
		body.Close()
		return nil, err
	}
	r, err := age.Decrypt(body, id)
	if err != nil {
		body.Close()
		return nil, fmt.Errorf("couldn't decrypt the backup (was it made with a different passphrase?): %w", err)
	}
	return struct {
		io.Reader
		io.Closer
	}{r, body}, nil
}

// RestoreRequest says what to restore where.
type RestoreRequest struct {
	App     *store.App // the service the backup belongs to (and the target, unless NewName is set)
	Dest    *store.BackupDestination
	Key     string
	NewName string // restore into a new database with this name in the same project
	User    string
}

// Restore starts a restore in the background and returns its record.
func (s *Service) Restore(ctx context.Context, req RestoreRequest) (*store.Restore, error) {
	app := req.App
	kind := KindFromKey(req.Key)
	if kind == "" || kind == "panel" {
		return nil, errors.New("this isn't a database or volume backup made by SimplePanel")
	}
	if !Compatible(kind, app) {
		return nil, fmt.Errorf("a %s backup can't be restored into %s", kind, app.Name)
	}
	if req.NewName != "" && !app.IsDatabase() {
		return nil, errors.New("only database backups can be restored into a new database")
	}
	if req.NewName != "" {
		if err := deploy.ValidName(req.NewName); err != nil {
			return nil, fmt.Errorf("name: %w", err)
		}
		apps, err := s.store.Apps(ctx, app.ProjectID)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(apps, func(a store.App) bool { return a.Name == req.NewName }) {
			return nil, errors.New("a service with that name already exists in this project")
		}
	}
	if !s.lock(app.ID) {
		return nil, ErrBusy
	}
	r := &store.Restore{AppID: &app.ID, DestinationID: &req.Dest.ID, Key: req.Key, Username: req.User}
	if req.NewName == "" {
		r.TargetAppID = &app.ID
	}
	if err := s.store.CreateRestore(ctx, r); err != nil {
		s.unlock(app.ID)
		return nil, err
	}
	go func() {
		defer s.unlock(app.ID)
		s.sem <- struct{}{}
		defer func() { <-s.sem }()
		s.runRestore(r, req)
	}()
	return r, nil
}

func (s *Service) runRestore(r *store.Restore, req RestoreRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	lb := &logBuf{}
	save := func(status string, done bool) {
		if err := s.store.UpdateRestore(context.Background(), r.ID, status, lb.String(), done); err != nil {
			s.log.Error("saving restore record", "err", err)
		}
	}
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				save("running", false)
			}
		}
	}()
	target, err := s.restore(ctx, r, req, lb)
	close(stop)
	status, level, title := "succeeded", "success", "Restore finished"
	msg := fmt.Sprintf("%s was restored into %s.", path.Base(req.Key), "")
	if target != nil {
		msg = fmt.Sprintf("%s was restored into %s/%s.", path.Base(req.Key), target.ProjectName, target.Name)
	}
	if err != nil {
		status, level, title, msg = "failed", "error", "Restore failed", err.Error()
		lb.Printf("Restore failed: %v", err)
	} else {
		lb.Printf("Restore finished")
	}
	save(status, true)
	if s.notify != nil {
		ev := notify.Event{Type: notify.RestoreFinished, Level: level, Title: title + ": " + req.App.ProjectName + "/" + req.App.Name,
			Message: msg, ProjectID: req.App.ProjectID, Project: req.App.ProjectName, Service: req.App.Name,
			Link: fmt.Sprintf("/#/p/%d/a/%d/backups", req.App.ProjectID, req.App.ID)}
		s.notify.Send(ev)
	}
}

func (s *Service) restore(ctx context.Context, r *store.Restore, req RestoreRequest, lb *logBuf) (*store.App, error) {
	target := req.App
	if req.NewName != "" {
		e := deploy.EngineByKey(req.App.Kind)
		if e == nil {
			return nil, errors.New("unknown database engine")
		}
		lb.Printf("Creating database %s/%s (%s)", req.App.ProjectName, req.NewName, e.Label)
		target = deploy.NewDatabaseApp(e, req.App.ProjectID, req.App.ProjectName, req.NewName, deploy.DBCredentials{}, store.RandomToken)
		target.Image = req.App.Image
		if err := s.store.CreateApp(ctx, target); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil, errors.New("a service with that name already exists in this project")
			}
			return nil, err
		}
		_ = s.store.SetRestoreTarget(ctx, r.ID, target.ID)
		d, err := s.deploy.Deploy(ctx, target.ID, "restore")
		if err != nil {
			return target, err
		}
		lb.Printf("Deploying it (deployment #%d)", d.ID)
		d, err = s.deploy.WaitDeployment(ctx, d.ID)
		if err != nil {
			return target, err
		}
		if d.Status != "succeeded" {
			return target, errors.New("the new database failed to deploy; see its deployments")
		}
	}
	if target.IsDatabase() {
		lb.Printf("Waiting for %s to accept connections", target.Name)
		if err := s.deploy.WaitDatabaseReady(ctx, target, 3*time.Minute); err != nil {
			return target, err
		}
	}
	lb.Printf("Downloading %s from %s", req.Key, req.Dest.Name)
	body, err := s.Open(ctx, req.Dest, req.Key)
	if err != nil {
		return target, err
	}
	defer body.Close()
	gz, err := gzip.NewReader(body)
	if err != nil {
		return target, fmt.Errorf("the backup isn't valid gzip data: %w", err)
	}
	defer gz.Close()
	if target.IsDatabase() {
		lb.Printf("Restoring into %s/%s; existing tables in the backup are replaced", target.ProjectName, target.Name)
		return target, s.deploy.RestoreDatabase(ctx, target, gz, lb)
	}
	lb.Printf("Restoring the volumes of %s/%s; their current files are replaced", target.ProjectName, target.Name)
	return target, s.deploy.RestoreVolumes(ctx, target, gz, lb.Printf)
}

// Start runs the scheduler until ctx ends.
func (s *Service) Start(ctx context.Context) {
	go func() {
		// Jobs without a next run time (e.g. after a restart) get one.
		if jobs, err := s.store.AllBackupJobs(ctx); err == nil {
			for _, j := range jobs {
				if j.Enabled && j.NextRunAt == nil {
					if next, err := NextRun(j.Schedule, s.clock()); err == nil {
						_ = s.store.SetJobNextRun(ctx, j.ID, next)
					}
				}
			}
		}
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			s.runDue(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (s *Service) runDue(ctx context.Context) {
	now := s.clock()
	jobs, err := s.store.DueBackupJobs(ctx, now.Unix())
	if err != nil {
		s.log.Error("backup scheduler", "err", err)
		return
	}
	for _, j := range jobs {
		next, err := NextRun(j.Schedule, now)
		if err != nil {
			next = nil
		}
		_ = s.store.SetJobNextRun(ctx, j.ID, next)
		dest, err := s.store.BackupDestination(ctx, j.DestinationID)
		if err != nil {
			continue
		}
		var app *store.App
		if j.AppID != nil {
			if app, err = s.store.App(ctx, *j.AppID); err != nil {
				continue
			}
		}
		jobID := j.ID
		if _, err := s.Run(ctx, app, dest, &jobID, "schedule"); err != nil {
			s.log.Warn("scheduled backup not started", "job", j.ID, "err", err)
		}
	}
}

// KindOf is what a backup of app would contain ("" if nothing).
func KindOf(app *store.App) string { return deploy.BackupKind(app) }
