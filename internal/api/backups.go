package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/backup"
	"github.com/SimpleAOB/SimplePanel/internal/s3"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// ---- Destinations (owners and admins) ----

type destInput struct {
	Name            string `json:"name"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKey       string `json:"accessKey"`
	SecretKey       string `json:"secretKey"` // empty keeps the saved key
	PathStyle       bool   `json:"pathStyle"`
	Passphrase      string `json:"passphrase"` // empty keeps the saved one
	ClearPassphrase bool   `json:"clearPassphrase"`
}

func (in destInput) apply(d *store.BackupDestination) error {
	d.Name = strings.TrimSpace(in.Name)
	if d.Name == "" || len(d.Name) > 60 {
		return errors.New("give the destination a name of up to 60 characters")
	}
	d.Endpoint = strings.TrimRight(strings.TrimSpace(in.Endpoint), "/")
	d.Region = strings.TrimSpace(in.Region)
	d.Bucket = strings.TrimSpace(in.Bucket)
	d.Prefix = strings.Trim(strings.TrimSpace(in.Prefix), "/")
	d.AccessKey = strings.TrimSpace(in.AccessKey)
	d.PathStyle = in.PathStyle
	if sk := strings.TrimSpace(in.SecretKey); sk != "" {
		d.SecretKey = sk
	}
	switch {
	case in.ClearPassphrase:
		d.Passphrase = ""
	case in.Passphrase != "":
		if len(in.Passphrase) < 12 || len(in.Passphrase) > 200 {
			return errors.New("use an encryption passphrase of 12 to 200 characters")
		}
		d.Passphrase = in.Passphrase
	}
	for _, v := range []string{d.Endpoint, d.Region, d.Bucket, d.Prefix, d.AccessKey, d.SecretKey} {
		if len(v) > 500 || strings.ContainsAny(v, "\r\n\t") {
			return errors.New("a field is too long or has line breaks")
		}
	}
	if strings.Contains(d.Prefix, "..") || strings.HasPrefix(d.Prefix, ".") {
		return errors.New("folder can't contain ..")
	}
	_, err := backup.Client(d)
	return err
}

// destView hides keys from users who aren't admins.
func destView(d store.BackupDestination, admin bool) map[string]any {
	out := map[string]any{"id": d.ID, "name": d.Name, "encrypted": d.Encrypted}
	if admin {
		out["endpoint"], out["region"], out["bucket"], out["prefix"] = d.Endpoint, d.Region, d.Bucket, d.Prefix
		out["accessKey"], out["pathStyle"], out["createdAt"] = d.AccessKey, d.PathStyle, d.CreatedAt
	}
	return out
}

func (s *Server) listDestinations(w http.ResponseWriter, r *http.Request, u *store.User) {
	ds, err := s.store.BackupDestinations(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := []map[string]any{}
	for _, d := range ds {
		out = append(out, destView(d, u.IsAdmin()))
	}
	writeJSON(w, 200, out)
}

func (s *Server) saveDestination(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	d := &store.BackupDestination{}
	if r.PathValue("id") != "" {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, 400, "bad id")
			return
		}
		var err error
		if d, err = s.store.BackupDestination(r.Context(), id); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	var in destInput
	if !readJSON(w, r, &in) {
		return
	}
	if err := in.apply(d); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	client, _ := backup.Client(d)
	if err := client.Check(ctx, d.Prefix+"/"); err != nil {
		writeErr(w, 400, "couldn't use the bucket: "+err.Error())
		return
	}
	created := d.ID == 0
	if err := s.store.SaveBackupDestination(r.Context(), d); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, 409, "a destination with that name already exists")
			return
		}
		s.storeErr(w, err)
		return
	}
	code := 200
	if created {
		code = 201
	}
	writeJSON(w, code, destView(*d, true))
}

// testDestination checks unsaved settings (secrets fall back to the saved
// destination's when an id is given).
func (s *Server) testDestination(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	var in struct {
		destInput
		ID int64 `json:"id"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	d := &store.BackupDestination{}
	if in.ID > 0 {
		var err error
		if d, err = s.store.BackupDestination(r.Context(), in.ID); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	if in.Name == "" {
		in.Name = "test"
	}
	if err := in.destInput.apply(d); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	client, _ := backup.Client(d)
	if err := client.Check(ctx, d.Prefix+"/"); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteDestination(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if err := s.store.DeleteBackupDestination(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// revealPassphrase shows a destination's encryption passphrase, so the
// owner can store it somewhere safe: without it, encrypted backups can't
// be restored on a new server.
func (s *Server) revealPassphrase(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	d, err := s.store.BackupDestination(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"passphrase": d.Passphrase})
}

// ---- Schedules ----

type jobInput struct {
	DestinationID int64  `json:"destinationId"`
	Schedule      string `json:"schedule"`
	Keep          int    `json:"keep"`
	Enabled       *bool  `json:"enabled"`
}

func (s *Server) applyJob(r *http.Request, in jobInput, j *store.BackupJob) error {
	if _, err := s.store.BackupDestination(r.Context(), in.DestinationID); err != nil {
		return errors.New("pick a backup destination")
	}
	j.DestinationID = in.DestinationID
	j.Schedule = strings.TrimSpace(in.Schedule)
	if in.Keep < 1 || in.Keep > 1000 {
		return errors.New("keep between 1 and 1000 backups")
	}
	j.Keep = in.Keep
	if in.Enabled != nil {
		j.Enabled = *in.Enabled
	} else if j.ID == 0 {
		j.Enabled = true
	}
	next, err := backup.NextRun(j.Schedule, time.Now())
	if err != nil {
		return err
	}
	j.NextRunAt = nil
	if j.Enabled {
		j.NextRunAt = next
	}
	return nil
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request, u *store.User) {
	var appID *int64
	if r.PathValue("id") != "" {
		a, ok := s.loadApp(w, r, u, store.LevelDeploy)
		if !ok {
			return
		}
		if backup.KindOf(a) == "" {
			writeErr(w, 400, "this app has no volumes, so there's nothing to back up")
			return
		}
		appID = &a.ID
	} else if !requireAdmin(w, u) {
		return
	}
	var in jobInput
	if !readJSON(w, r, &in) {
		return
	}
	j := &store.BackupJob{AppID: appID}
	if err := s.applyJob(r, in, j); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.store.SaveBackupJob(r.Context(), j); err != nil {
		s.storeErr(w, err)
		return
	}
	j, _ = s.store.BackupJob(r.Context(), j.ID)
	writeJSON(w, 201, j)
}

// loadJob loads the {id} job and checks access to what it backs up.
func (s *Server) loadJob(w http.ResponseWriter, r *http.Request, u *store.User, need int) (*store.BackupJob, bool) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return nil, false
	}
	j, err := s.store.BackupJob(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	if j.AppID == nil {
		return j, requireAdmin(w, u)
	}
	if _, ok := s.loadAppID(w, r, u, *j.AppID, need); !ok {
		return nil, false
	}
	return j, true
}

func (s *Server) updateJob(w http.ResponseWriter, r *http.Request, u *store.User) {
	j, ok := s.loadJob(w, r, u, store.LevelDeploy)
	if !ok {
		return
	}
	var in jobInput
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.applyJob(r, in, j); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.store.SaveBackupJob(r.Context(), j); err != nil {
		s.storeErr(w, err)
		return
	}
	j, _ = s.store.BackupJob(r.Context(), j.ID)
	writeJSON(w, 200, j)
}

func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request, u *store.User) {
	j, ok := s.loadJob(w, r, u, store.LevelDeploy)
	if !ok {
		return
	}
	if err := s.store.DeleteBackupJob(r.Context(), j.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- Backups of a service ----

func (s *Server) appBackups(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelView)
	if !ok {
		return
	}
	s.writeBackupOverview(w, r, u, a.ID, backup.KindOf(a))
}

func (s *Server) panelBackups(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	s.writeBackupOverview(w, r, u, 0, "panel")
}

func (s *Server) writeBackupOverview(w http.ResponseWriter, r *http.Request, u *store.User, appID int64, kind string) {
	ctx := r.Context()
	bs, err := s.store.Backups(ctx, appID, 100)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	jobs, err := s.store.BackupJobs(ctx, appID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	restores := []store.Restore{}
	if appID != 0 {
		if restores, err = s.store.Restores(ctx, appID, 20); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	ds, err := s.store.BackupDestinations(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	dests := []map[string]any{}
	for _, d := range ds {
		dests = append(dests, destView(d, false))
	}
	zone, _ := time.Now().Zone()
	writeJSON(w, 200, map[string]any{"kind": kind, "backups": bs, "jobs": jobs, "restores": restores,
		"destinations": dests, "timezone": zone, "admin": u.IsAdmin()})
}

func (s *Server) runBackup(w http.ResponseWriter, r *http.Request, u *store.User) {
	var app *store.App
	if r.PathValue("id") != "" {
		a, ok := s.loadApp(w, r, u, store.LevelDeploy)
		if !ok {
			return
		}
		app = a
	} else if !requireAdmin(w, u) {
		return
	}
	var in struct {
		DestinationID int64 `json:"destinationId"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	dest, err := s.store.BackupDestination(r.Context(), in.DestinationID)
	if err != nil {
		writeErr(w, 400, "pick a backup destination")
		return
	}
	b, err := s.backups.Run(r.Context(), app, dest, nil, "manual:"+u.Username)
	if errors.Is(err, backup.ErrBusy) {
		writeErr(w, 409, err.Error())
		return
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, b)
}

// loadBackup loads the {id} backup and checks access to its service.
func (s *Server) loadBackup(w http.ResponseWriter, r *http.Request, u *store.User, need int) (*store.Backup, bool) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return nil, false
	}
	b, err := s.store.Backup(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	if b.AppID == nil {
		// Panel backups, and backups of services that were deleted.
		return b, requireAdmin(w, u)
	}
	if _, ok := s.loadAppID(w, r, u, *b.AppID, need); !ok {
		return nil, false
	}
	return b, true
}

func (s *Server) getBackup(w http.ResponseWriter, r *http.Request, u *store.User) {
	if b, ok := s.loadBackup(w, r, u, store.LevelView); ok {
		writeJSON(w, 200, b)
	}
}

func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request, u *store.User) {
	b, ok := s.loadBackup(w, r, u, store.LevelManage)
	if !ok {
		return
	}
	if b.Status == "running" {
		writeErr(w, 409, "this backup is still running")
		return
	}
	if err := s.backups.Delete(r.Context(), b); err != nil {
		writeErr(w, 502, "couldn't delete it from storage: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// remoteBackups lists the service's backups found in a destination's
// bucket, including ones made by another SimplePanel.
func (s *Server) remoteBackups(w http.ResponseWriter, r *http.Request, u *store.User) {
	var app *store.App
	if r.PathValue("id") != "" {
		a, ok := s.loadApp(w, r, u, store.LevelView)
		if !ok {
			return
		}
		app = a
	} else if !requireAdmin(w, u) {
		return
	}
	did, _ := strconv.ParseInt(r.URL.Query().Get("destination"), 10, 64)
	dest, err := s.store.BackupDestination(r.Context(), did)
	if err != nil {
		writeErr(w, 400, "pick a backup destination")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	list, err := s.backups.ListRemote(ctx, dest, app)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// downloadBackup streams a backup, decrypted, as a .gz file.
func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request, u *store.User) {
	var dest *store.BackupDestination
	var key string
	var err error
	if r.PathValue("id") != "" {
		b, ok := s.loadBackup(w, r, u, store.LevelDeploy)
		if !ok {
			return
		}
		if b.Status != "succeeded" {
			writeErr(w, 409, "this backup didn't finish")
			return
		}
		if dest, err = s.store.BackupDestination(r.Context(), b.DestinationID); err != nil {
			s.storeErr(w, err)
			return
		}
		key = b.Key
	} else {
		// A backup found in the bucket: /apps/{app}/backups/file?destination=&key=
		a, ok := s.loadAppID(w, r, u, mustInt(r.PathValue("app")), store.LevelDeploy)
		if !ok {
			return
		}
		did, _ := strconv.ParseInt(r.URL.Query().Get("destination"), 10, 64)
		if dest, err = s.store.BackupDestination(r.Context(), did); err != nil {
			writeErr(w, 400, "pick a backup destination")
			return
		}
		key = r.URL.Query().Get("key")
		if !strings.HasPrefix(key, backup.Prefix(dest, a)) || strings.Contains(key, "..") {
			writeErr(w, 403, "that file doesn't belong to this service")
			return
		}
	}
	body, err := s.backups.Open(r.Context(), dest, key)
	if errors.Is(err, s3.ErrNotFound) {
		writeErr(w, 404, "the file is no longer in the bucket")
		return
	}
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	defer body.Close()
	// Large downloads outlive the server's write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	name := strings.TrimSuffix(path.Base(key), ".age")
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = io.Copy(w, body)
}

func mustInt(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// restoreBackup restores a backup into the service it belongs to (which
// replaces its data) or, for databases, into a new database.
func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request, u *store.User) {
	var in struct {
		BackupID      int64  `json:"backupId"`
		DestinationID int64  `json:"destinationId"`
		Key           string `json:"key"`
		NewName       string `json:"newName"`
		Confirm       string `json:"confirm"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.NewName = strings.TrimSpace(strings.ToLower(in.NewName))
	need := store.LevelManage // replacing data
	if in.NewName != "" {
		need = store.LevelDeploy
	}
	a, ok := s.loadApp(w, r, u, need)
	if !ok {
		return
	}
	if in.NewName == "" && in.Confirm != a.Name {
		writeErr(w, 400, fmt.Sprintf("restoring replaces the current data of %s; confirm with its name", a.Name))
		return
	}
	var dest *store.BackupDestination
	var err error
	if in.BackupID > 0 {
		b, err := s.store.Backup(r.Context(), in.BackupID)
		if err != nil || b.AppID == nil || *b.AppID != a.ID || b.Status != "succeeded" {
			writeErr(w, 400, "that backup can't be restored here")
			return
		}
		in.DestinationID, in.Key = b.DestinationID, b.Key
	}
	if dest, err = s.store.BackupDestination(r.Context(), in.DestinationID); err != nil {
		writeErr(w, 400, "pick a backup destination")
		return
	}
	// Restoring another service's backup is limited to admins, who can see
	// every project anyway.
	if (!strings.HasPrefix(in.Key, backup.Prefix(dest, a)) && !u.IsAdmin()) || strings.Contains(in.Key, "..") || in.Key == "" {
		writeErr(w, 403, "that backup belongs to another service; ask an admin to restore it")
		return
	}
	rs, err := s.backups.Restore(r.Context(), backup.RestoreRequest{App: a, Dest: dest, Key: in.Key, NewName: in.NewName, User: u.Username})
	if errors.Is(err, backup.ErrBusy) {
		writeErr(w, 409, err.Error())
		return
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, rs)
}

func (s *Server) getRestore(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	rs, err := s.store.Restore(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if rs.AppID == nil {
		if !requireAdmin(w, u) {
			return
		}
	} else if _, ok := s.loadAppID(w, r, u, *rs.AppID, store.LevelView); !ok {
		return
	}
	writeJSON(w, 200, rs)
}
