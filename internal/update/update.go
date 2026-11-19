// Package update lets SimplePanel update itself from inside the panel.
//
// It follows the stable channel: GitHub releases of SimpleAOB/SimplePanel
// that aren't drafts or pre-releases. An update:
//
//  1. pulls ghcr.io/simpleaob/simplepanel:<version>,
//  2. checks the image's OCI labels name that version, that repository and
//     the commit the release tag points at, and pins it by digest,
//  3. saves a copy of the panel database,
//  4. hands the new image to Swarm, which replaces the panel's task.
//
// Swarm does the replacing, not the panel, so it finishes even though the
// old panel is stopped part way through. The panel's service gets
// FailureAction=rollback: if the new version doesn't pass its health check,
// or crashes within the monitor window, Swarm puts the previous version
// back. Whichever version boots next reads the saved job and records the
// outcome.
//
// The panel is replaced stop-first, not start-first: it publishes port 3000
// in host mode and keeps its state in SQLite, so two copies can't run at
// once. The panel itself is away for a few seconds; apps, databases and
// Traefik are separate services and keep running.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/notify"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

const (
	DefaultRepo  = "SimpleAOB/SimplePanel"
	DefaultImage = "ghcr.io/simpleaob/simplepanel"

	checkEvery  = 6 * time.Hour
	tickEvery   = 15 * time.Minute
	keepCopies  = 3
	maxNotes    = 20000
	pullTimeout = 15 * time.Minute
)

// Settings keys.
const (
	keyCheck    = "update_check"
	keySettings = "update_settings"
	keyJob      = "update_job"
	keyNotified = "update_notified"
)

// Config says where releases and images come from.
type Config struct {
	Repo    string // GitHub owner/name with the releases
	Image   string // image repository the releases are published to
	Service string // the panel's own Swarm service
	Version string // the running version, e.g. v0.4.0 (or "main" for :edge)
	DataDir string // where database copies are kept
	APIBase string // GitHub API, overridable for tests
}

// Release is a stable GitHub release.
type Release struct {
	Version     string `json:"version"` // 0.4.0
	Tag         string `json:"tag"`     // v0.4.0
	Name        string `json:"name"`
	Notes       string `json:"notes"`
	URL         string `json:"url"`
	PublishedAt string `json:"publishedAt"`
	Commit      string `json:"commit"`
}

// Check is the result of the last release check.
type Check struct {
	Latest    *Release `json:"latest"`
	Available bool     `json:"available"`
	Note      string   `json:"note,omitempty"`
	Error     string   `json:"error,omitempty"`
	CheckedAt int64    `json:"checkedAt"`
	For       string   `json:"for"` // the running version the check was made for
}

// Settings are the admin's update preferences.
type Settings struct {
	Auto bool `json:"auto"`
	Hour int  `json:"hour"` // server-local hour to install in; -1 for any time
}

// Job is an update in progress, or the last one.
type Job struct {
	State      string `json:"state"` // running|restarting|succeeded|failed|rolled_back
	Step       string `json:"step"`
	From       string `json:"from"`
	To         string `json:"to"`
	Image      string `json:"image,omitempty"`
	Snapshot   string `json:"snapshot,omitempty"`
	Error      string `json:"error,omitempty"`
	By         string `json:"by"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt int64  `json:"finishedAt,omitempty"`
}

// Active reports whether the job hasn't finished.
func (j *Job) Active() bool { return j != nil && (j.State == "running" || j.State == "restarting") }

// Status is what the settings page and the banner show.
type Status struct {
	Current    string   `json:"current"`
	Channel    string   `json:"channel"`
	Check      Check    `json:"check"`
	CanUpdate  bool     `json:"canUpdate"`
	Blocker    string   `json:"blocker,omitempty"`
	Settings   Settings `json:"settings"`
	Job        *Job     `json:"job"`
	ActiveJobs int      `json:"activeJobs"`
	Image      string   `json:"image"`
}

// Updater checks for and installs updates.
type Updater struct {
	cfg    Config
	st     *store.Store
	dc     *docker.Client
	notify *notify.Notifier
	log    *slog.Logger
	http   *http.Client

	mu       sync.Mutex
	running  bool
	lastPoll time.Time
}

func New(cfg Config, st *store.Store, dc *docker.Client, n *notify.Notifier, log *slog.Logger) *Updater {
	if cfg.Repo == "" {
		cfg.Repo = DefaultRepo
	}
	if cfg.Image == "" {
		cfg.Image = DefaultImage
	}
	if cfg.Service == "" {
		cfg.Service = "simplepanel"
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.github.com"
	}
	return &Updater{cfg: cfg, st: st, dc: dc, notify: n, log: log, http: &http.Client{Timeout: 20 * time.Second}}
}

// ---- Versions ----

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// parseVersion parses a stable version (v1.2.3 or 1.2.3). Pre-releases
// and builds such as "main" or "dev" don't parse.
func parseVersion(v string) ([3]int, bool) {
	m := semverRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// newer reports whether version a is newer than b. Both must parse.
func newer(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// Display formats a version for people: v0.4.0, or the build name as is.
func Display(v string) string {
	if p, ok := parseVersion(v); ok {
		return fmt.Sprintf("v%d.%d.%d", p[0], p[1], p[2])
	}
	return v
}

// ---- Stored state ----

func (u *Updater) loadJSON(ctx context.Context, key string, v any) bool {
	s, err := u.st.Setting(ctx, key)
	if err != nil || s == "" {
		return false
	}
	return json.Unmarshal([]byte(s), v) == nil
}

func (u *Updater) saveJSON(ctx context.Context, key string, v any) {
	b, _ := json.Marshal(v)
	if err := u.st.SetSetting(context.WithoutCancel(ctx), key, string(b)); err != nil {
		u.log.Error("update: saving state", "key", key, "err", err)
	}
}

func (u *Updater) LoadSettings(ctx context.Context) Settings {
	s := Settings{Hour: 3}
	u.loadJSON(ctx, keySettings, &s)
	return s
}

func (u *Updater) SaveSettings(ctx context.Context, s Settings) error {
	if s.Hour < -1 || s.Hour > 23 {
		return errors.New("hour must be between 0 and 23, or -1 for any time")
	}
	u.saveJSON(ctx, keySettings, s)
	return nil
}

func (u *Updater) job(ctx context.Context) *Job {
	var j Job
	if !u.loadJSON(ctx, keyJob, &j) {
		return nil
	}
	return &j
}

func (u *Updater) setJob(ctx context.Context, j *Job) { u.saveJSON(ctx, keyJob, j) }

// lastCheck returns the stored check, unless it was made for another
// running version (the panel has been updated since).
func (u *Updater) lastCheck(ctx context.Context) Check {
	var c Check
	if !u.loadJSON(ctx, keyCheck, &c) || c.For != u.cfg.Version {
		return Check{}
	}
	return c
}

// ---- Status ----

// blocker explains why this install can't update itself, or returns "".
func (u *Updater) blocker(ctx context.Context) string {
	svc, err := u.dc.ServiceInspectRaw(ctx, u.cfg.Service)
	if err != nil {
		return fmt.Sprintf("SimplePanel isn't running as the Swarm service %q here, so it can't replace itself. Update it with the install command instead.", u.cfg.Service)
	}
	img := serviceImage(svc.Spec)
	if name, _ := docker.SplitTag(stripDigest(img)); !strings.EqualFold(name, u.cfg.Image) {
		return fmt.Sprintf("This panel runs a custom image (%s). In-panel updates only install official images from %s.", name, u.cfg.Image)
	}
	return ""
}

func (u *Updater) Status(ctx context.Context) Status {
	s := Status{
		Current:  Display(u.cfg.Version),
		Channel:  "stable",
		Check:    u.lastCheck(ctx),
		Settings: u.LoadSettings(ctx),
		Job:      u.job(ctx),
		Image:    u.cfg.Image,
	}
	s.ActiveJobs, _ = u.st.ActiveJobs(ctx)
	s.Blocker = u.blocker(ctx)
	s.CanUpdate = s.Blocker == "" && s.Check.Available && !s.Job.Active()
	return s
}

// ---- Checking for releases ----

func (u *Updater) github(ctx context.Context, path, accept string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.cfg.APIBase+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "SimplePanel-updater")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := u.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("can't reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, 2<<20)
	if resp.StatusCode != 200 {
		_, _ = io.Copy(io.Discard, body)
		if resp.StatusCode == 403 || resp.StatusCode == 429 {
			return resp.StatusCode, errors.New("GitHub's rate limit was reached; try again in an hour")
		}
		return resp.StatusCode, fmt.Errorf("GitHub answered HTTP %d", resp.StatusCode)
	}
	if s, ok := out.(*string); ok {
		b, err := io.ReadAll(body)
		*s = strings.TrimSpace(string(b))
		return 200, err
	}
	return 200, json.NewDecoder(body).Decode(out)
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// latestRelease asks GitHub for the newest stable release.
func (u *Updater) latestRelease(ctx context.Context) (*Release, error) {
	var gr struct {
		TagName     string `json:"tag_name"`
		Name        string `json:"name"`
		Body        string `json:"body"`
		HTMLURL     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
	}
	code, err := u.github(ctx, "/repos/"+u.cfg.Repo+"/releases/latest", "application/vnd.github+json", &gr)
	if code == 404 {
		return nil, nil // no stable release yet
	}
	if err != nil {
		return nil, err
	}
	p, ok := parseVersion(gr.TagName)
	if !ok || gr.Draft || gr.Prerelease {
		return nil, nil
	}
	r := &Release{
		Version: fmt.Sprintf("%d.%d.%d", p[0], p[1], p[2]), Tag: gr.TagName, Name: gr.Name,
		Notes: gr.Body, URL: gr.HTMLURL, PublishedAt: gr.PublishedAt,
	}
	if len(r.Notes) > maxNotes {
		r.Notes = r.Notes[:maxNotes] + "\n\n…"
	}
	if !strings.HasPrefix(r.URL, "https://github.com/") {
		r.URL = "https://github.com/" + u.cfg.Repo + "/releases/tag/" + url.PathEscape(r.Tag)
	}
	var sha string
	if _, err := u.github(ctx, "/repos/"+u.cfg.Repo+"/commits/"+url.PathEscape(r.Tag), "application/vnd.github.sha", &sha); err != nil {
		return nil, err
	}
	if !shaRe.MatchString(sha) {
		return nil, errors.New("GitHub returned an unexpected commit for " + r.Tag)
	}
	r.Commit = sha
	return r, nil
}

// runningRevision is the commit the running image was built from, read
// from its OCI labels. Used for builds without a version number (:edge).
func (u *Updater) runningRevision(ctx context.Context) string {
	svc, err := u.dc.ServiceInspectRaw(ctx, u.cfg.Service)
	if err != nil {
		return ""
	}
	labels, err := u.dc.ImageLabels(ctx, serviceImage(svc.Spec))
	if err != nil {
		return ""
	}
	if rev := labels["org.opencontainers.image.revision"]; shaRe.MatchString(rev) {
		return rev
	}
	return ""
}

// CheckNow asks GitHub for the latest stable release and stores the result.
// force skips the 30-second cache that keeps a busy settings page from
// using up GitHub's rate limit.
func (u *Updater) CheckNow(ctx context.Context, force bool) Check {
	u.mu.Lock()
	if prev := u.lastCheck(ctx); !force && prev.CheckedAt > 0 && time.Since(u.lastPoll) < 30*time.Second {
		u.mu.Unlock()
		return prev
	}
	u.lastPoll = time.Now()
	u.mu.Unlock()

	c := Check{CheckedAt: time.Now().Unix(), For: u.cfg.Version}
	rel, err := u.latestRelease(ctx)
	switch {
	case err != nil:
		c.Error = err.Error()
		// Keep showing the last known release.
		if prev := u.lastCheck(ctx); prev.Latest != nil {
			c.Latest, c.Available, c.Note = prev.Latest, prev.Available, prev.Note
		}
	case rel == nil:
		c.Note = "No stable release has been published yet."
	default:
		c.Latest = rel
		c.Available, c.Note = u.compare(ctx, rel)
	}
	u.saveJSON(ctx, keyCheck, c)
	if c.Available && c.Latest != nil {
		u.announce(ctx, c.Latest)
	}
	return c
}

// compare decides whether rel is newer than what's running.
func (u *Updater) compare(ctx context.Context, rel *Release) (bool, string) {
	cur, ok := parseVersion(u.cfg.Version)
	rv, _ := parseVersion(rel.Version)
	if ok {
		if newer(rv, cur) {
			return true, ""
		}
		return false, "You're on the latest stable release."
	}
	// A build of main (:edge) has no version number. Offer the release only
	// if it contains every commit of the running build, so switching never
	// goes backwards.
	rev := u.runningRevision(ctx)
	if rev == "" {
		return false, fmt.Sprintf("This is a development build (%s). In-panel updates install stable releases; re-run the install command to switch.", u.cfg.Version)
	}
	var cmp struct {
		Status string `json:"status"`
	}
	if _, err := u.github(ctx, "/repos/"+u.cfg.Repo+"/compare/"+rev+"..."+rel.Commit, "application/vnd.github+json", &cmp); err != nil {
		return false, "Couldn't compare your build with the latest release: " + err.Error()
	}
	switch cmp.Status {
	case "ahead":
		return true, fmt.Sprintf("You're on a build of main (%s). Updating switches you to the stable channel.", rev[:7])
	case "identical":
		return false, fmt.Sprintf("Your build of main (%s) is the same code as %s.", rev[:7], Display(rel.Version))
	default:
		return false, fmt.Sprintf("Your build of main (%s) has changes newer than %s. You'll be offered the first stable release that includes them.", rev[:7], Display(rel.Version))
	}
}

// announce sends one "update available" notification per version.
func (u *Updater) announce(ctx context.Context, rel *Release) {
	if v, _ := u.st.Setting(ctx, keyNotified); v == rel.Version {
		return
	}
	_ = u.st.SetSetting(ctx, keyNotified, rel.Version)
	if u.notify == nil {
		return
	}
	u.notify.Send(notify.Event{
		Type: notify.UpdateAvailable, Level: "info",
		Title:   fmt.Sprintf("SimplePanel %s is available", Display(rel.Version)),
		Message: fmt.Sprintf("You're running %s. Update from Settings > General. Release notes: %s", Display(u.cfg.Version), rel.URL),
		Link:    "/#/settings/general",
	})
}

// ---- Updating ----

var (
	ErrBusy        = errors.New("an update is already running")
	ErrNotOffered  = errors.New("that version isn't the latest stable release offered to this panel; check for updates again")
	ErrJobsRunning = errors.New("deployments, backups or cron jobs are running")
)

// Actor is who started an update, for the activity log.
type Actor struct {
	UserID    *int64
	Username  string
	Source    string // ui|api|system
	TokenID   *int64
	TokenName string
}

// Start begins updating to version, which must be the release the last
// check offered. Unless force is set, it refuses while jobs are running,
// since restarting the panel interrupts them.
func (u *Updater) Start(ctx context.Context, version string, force bool, by Actor) (*Job, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running || u.job(ctx).Active() {
		return nil, ErrBusy
	}
	if b := u.blocker(ctx); b != "" {
		return nil, errors.New(b)
	}
	c := u.lastCheck(ctx)
	if c.Latest == nil || !c.Available || strings.TrimPrefix(version, "v") != c.Latest.Version {
		return nil, ErrNotOffered
	}
	if n, _ := u.st.ActiveJobs(ctx); n > 0 && !force {
		return nil, ErrJobsRunning
	}
	rel := *c.Latest
	j := &Job{State: "running", Step: "Starting", From: Display(u.cfg.Version), To: Display(rel.Version),
		By: by.Username, StartedAt: time.Now().Unix()}
	u.setJob(ctx, j)
	u.audit(ctx, by, "start SimplePanel update", j.From+" → "+j.To, true, "")
	u.running = true
	go func() {
		defer func() {
			u.mu.Lock()
			u.running = false
			u.mu.Unlock()
		}()
		u.run(context.Background(), j, rel, by)
	}()
	return j, nil
}

func (u *Updater) audit(ctx context.Context, by Actor, action, target string, ok bool, errMsg string) {
	if by.Source == "" {
		by.Source = "system"
	}
	e := &store.AuditEntry{Source: by.Source, UserID: by.UserID, Username: by.Username, TokenID: by.TokenID,
		TokenName: by.TokenName, Action: action, Target: target, OK: ok, Error: errMsg}
	if err := u.st.AddAudit(context.WithoutCancel(ctx), e); err != nil {
		u.log.Error("update: activity log", "err", err)
	}
}

func (u *Updater) fail(ctx context.Context, j *Job, by Actor, err error) {
	j.State, j.Error, j.FinishedAt = "failed", err.Error(), time.Now().Unix()
	u.setJob(ctx, j)
	u.log.Error("update failed", "to", j.To, "err", err)
	u.audit(ctx, Actor{Source: "system", Username: "SimplePanel"}, "SimplePanel update failed", j.From+" → "+j.To, false, j.Error)
	if u.notify != nil {
		u.notify.Send(notify.Event{Type: notify.UpdateFailed, Level: "error",
			Title:   "Updating SimplePanel to " + j.To + " failed",
			Message: j.Error + ". SimplePanel is running " + Display(u.cfg.Version) + ".", Link: "/#/settings/general"})
	}
}

func (u *Updater) step(ctx context.Context, j *Job, s string) {
	j.Step = s
	u.setJob(ctx, j)
	u.log.Info("update", "to", j.To, "step", s)
}

func (u *Updater) run(ctx context.Context, j *Job, rel Release, by Actor) {
	ref := u.cfg.Image + ":" + rel.Version

	u.step(ctx, j, "Downloading "+j.To)
	pctx, cancel := context.WithTimeout(ctx, pullTimeout)
	err := u.dc.ImagePull(pctx, ref, io.Discard)
	cancel()
	if err != nil {
		u.fail(ctx, j, by, fmt.Errorf("downloading %s: %w", ref, err))
		return
	}

	u.step(ctx, j, "Verifying the image")
	pinned, err := u.verify(ctx, ref, rel)
	if err != nil {
		u.fail(ctx, j, by, err)
		return
	}
	j.Image = pinned

	u.step(ctx, j, "Saving a copy of the database")
	snap, err := u.snapshot(ctx, j.From)
	if err != nil {
		u.fail(ctx, j, by, fmt.Errorf("saving a copy of the database: %w", err))
		return
	}
	j.Snapshot = snap

	u.step(ctx, j, "Restarting SimplePanel")
	j.State = "restarting"
	u.setJob(ctx, j)
	if err := u.replace(ctx, pinned, rel.Version); err != nil {
		j.State = "running"
		u.fail(ctx, j, by, fmt.Errorf("updating the Swarm service: %w", err))
		return
	}
	// Swarm stops this process next. The new version records the outcome.
}

// verify checks the pulled image is the release it claims to be and
// returns it pinned by digest.
func (u *Updater) verify(ctx context.Context, ref string, rel Release) (string, error) {
	labels, err := u.dc.ImageLabels(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("reading the image: %w", err)
	}
	if err := checkLabels(labels, u.cfg.Repo, rel); err != nil {
		return "", err
	}
	digestRef, err := u.dc.ImageDigestRef(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("reading the image digest: %w", err)
	}
	_, digest := docker.SplitTag(digestRef)
	if !strings.HasPrefix(digest, "sha256:") {
		return "", errors.New("the image has no registry digest, so it can't be pinned")
	}
	return ref + "@" + digest, nil
}

// checkLabels compares an image's OCI labels with the release.
func checkLabels(labels map[string]string, repo string, rel Release) error {
	src := strings.TrimSuffix(strings.TrimSuffix(labels["org.opencontainers.image.source"], "/"), ".git")
	if !strings.EqualFold(src, "https://github.com/"+repo) {
		return fmt.Errorf("the image says it was built from %q, not github.com/%s; not installing it", src, repo)
	}
	if v := strings.TrimPrefix(labels["org.opencontainers.image.version"], "v"); v != rel.Version {
		return fmt.Errorf("the image is labelled version %q, not %s; not installing it", v, rel.Version)
	}
	if rev := labels["org.opencontainers.image.revision"]; rev != rel.Commit {
		return fmt.Errorf("the image was built from commit %.12s, but %s points at %.12s; not installing it", rev, rel.Tag, rel.Commit)
	}
	return nil
}

// snapshot copies the database to DataDir/updates, keeping the newest few.
func (u *Updater) snapshot(ctx context.Context, from string) (string, error) {
	dir := filepath.Join(u.cfg.DataDir, "updates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	safe := regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(from, "_")
	path := filepath.Join(dir, fmt.Sprintf("simplepanel-%s-%s.db", safe, time.Now().UTC().Format("20060102-150405")))
	if err := u.st.Snapshot(ctx, path); err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o600)
	if ents, err := os.ReadDir(dir); err == nil {
		var dbs []string
		for _, e := range ents {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "simplepanel-") && strings.HasSuffix(e.Name(), ".db") {
				dbs = append(dbs, e.Name())
			}
		}
		slices.SortFunc(dbs, func(a, b string) int { return strings.Compare(stamp(b), stamp(a)) })
		for _, name := range dbs[min(len(dbs), keepCopies):] {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	return path, nil
}

// stamp is the timestamp part of a snapshot name.
func stamp(name string) string {
	name = strings.TrimSuffix(name, ".db")
	if i := strings.LastIndex(name, "-"); i > 0 {
		if j := strings.LastIndex(name[:i], "-"); j >= 0 {
			return name[j+1:]
		}
	}
	return name
}

// replace hands the new image to Swarm.
func (u *Updater) replace(ctx context.Context, image, version string) error {
	for range 3 { // retry if the spec changed under us
		svc, err := u.dc.ServiceInspectRaw(ctx, u.cfg.Service)
		if err != nil {
			return err
		}
		spec, err := patchSpec(svc.Spec, image, version)
		if err != nil {
			return err
		}
		err = u.dc.ServiceUpdateRaw(ctx, svc.ID, svc.Version.Index, spec)
		var apiErr *docker.APIError
		if errors.As(err, &apiErr) && strings.Contains(apiErr.Message, "update out of sequence") {
			continue
		}
		return err
	}
	return errors.New("the service kept changing; try again")
}

// patchSpec returns the service spec with the new image and the update
// policy that makes Swarm roll back a bad version. The spec is edited as
// generic JSON so every field SimplePanel doesn't model is kept.
func patchSpec(raw json.RawMessage, image, version string) (json.RawMessage, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber() // keep large integers exact
	var spec map[string]any
	if err := dec.Decode(&spec); err != nil {
		return nil, err
	}
	tt, _ := spec["TaskTemplate"].(map[string]any)
	if tt == nil {
		return nil, errors.New("the service has no task template")
	}
	cs, _ := tt["ContainerSpec"].(map[string]any)
	if cs == nil {
		return nil, errors.New("the service has no container spec")
	}
	cs["Image"] = image
	// Check health every 2 seconds while starting, so Swarm sees the new
	// version is up quickly (the image's own check waits 30 seconds), and
	// every 10 seconds after, so a broken version is rolled back in about
	// a minute and a half rather than three.
	cs["Healthcheck"] = map[string]any{
		"Test":          []string{"CMD-SHELL", "wget -qO- http://127.0.0.1:3000/api/health >/dev/null || exit 1"},
		"Interval":      int64(10 * time.Second),
		"Timeout":       int64(5 * time.Second),
		"StartPeriod":   int64(60 * time.Second),
		"StartInterval": int64(2 * time.Second),
		"Retries":       3,
	}
	policy := func(failure string) map[string]any {
		return map[string]any{
			"Parallelism":     1,
			"FailureAction":   failure,
			"Monitor":         int64(30 * time.Second),
			"MaxFailureRatio": 0,
			"Order":           "stop-first",
		}
	}
	spec["UpdateConfig"] = policy("rollback")
	spec["RollbackConfig"] = policy("pause")
	labels, _ := spec["Labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	labels["simplepanel.version"] = version
	spec["Labels"] = labels
	return json.Marshal(spec)
}

// failedTaskReason explains why a task of image failed, in plain words.
func (u *Updater) failedTaskReason(ctx context.Context, image string) string {
	tasks, err := u.dc.ServiceTasks(ctx, u.cfg.Service)
	if err != nil || image == "" {
		return ""
	}
	for _, t := range tasks {
		if t.Spec.ContainerSpec.Image != image || t.Status.Err == "" {
			continue
		}
		if strings.Contains(t.Status.Err, "unhealthy") {
			return "the new version didn't pass its health check"
		}
		return "the new version failed to start (" + t.Status.Err + ")"
	}
	return ""
}

func serviceImage(raw json.RawMessage) string {
	var s struct {
		TaskTemplate struct {
			ContainerSpec struct{ Image string }
		}
	}
	_ = json.Unmarshal(raw, &s)
	return s.TaskTemplate.ContainerSpec.Image
}

func stripDigest(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i]
	}
	return ref
}

// ---- Background loop ----

// Run records the outcome of an update that restarted the panel, then
// checks for releases every few hours and installs them when automatic
// updates are on.
func (u *Updater) Run(ctx context.Context) {
	go func() {
		u.reconcile(ctx)
		timer := time.NewTimer(time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			u.tick(ctx)
			timer.Reset(tickEvery)
		}
	}()
}

func (u *Updater) tick(ctx context.Context) {
	c := u.lastCheck(ctx)
	if time.Since(time.Unix(c.CheckedAt, 0)) >= checkEvery {
		c = u.CheckNow(ctx, true)
	}
	s := u.LoadSettings(ctx)
	if !s.Auto || !c.Available || c.Latest == nil {
		return
	}
	if s.Hour >= 0 && time.Now().Hour() != s.Hour {
		return
	}
	// Don't retry a version that already failed; an admin can.
	if j := u.job(ctx); j != nil && j.To == Display(c.Latest.Version) && (j.State == "failed" || j.State == "rolled_back") {
		return
	}
	if _, err := u.Start(ctx, c.Latest.Version, false, Actor{Source: "system", Username: "automatic update"}); err != nil &&
		!errors.Is(err, ErrJobsRunning) && !errors.Is(err, ErrBusy) {
		u.log.Warn("automatic update not started", "err", err)
	}
}

// reconcile finishes the record of an update the previous process started.
func (u *Updater) reconcile(ctx context.Context) {
	j := u.job(ctx)
	if j == nil {
		return
	}
	sys := Actor{Source: "system", Username: "SimplePanel"}
	switch j.State {
	case "running":
		// The panel stopped before handing over to Swarm.
		u.fail(ctx, j, sys, errors.New("SimplePanel restarted before the update was handed to Swarm"))
		return
	case "restarting":
	default:
		return
	}
	if Display(u.cfg.Version) == j.To {
		j.State, j.Step, j.FinishedAt = "succeeded", "Done", time.Now().Unix()
		u.setJob(ctx, j)
		u.log.Info("updated", "from", j.From, "to", j.To)
		u.audit(ctx, sys, "SimplePanel updated", j.From+" → "+j.To, true, "")
		if u.notify != nil {
			u.notify.Send(notify.Event{Type: notify.PanelUpdated, Level: "success",
				Title:   "SimplePanel updated to " + j.To,
				Message: "Updated from " + j.From + ". Release notes are in Settings > General.", Link: "/#/settings/general"})
		}
		return
	}
	if Display(u.cfg.Version) != j.From {
		// Neither version: someone changed the service another way.
		u.fail(ctx, j, sys, fmt.Errorf("SimplePanel is now running %s, which was installed another way", Display(u.cfg.Version)))
		return
	}
	// The old version is running again: Swarm rolled back. Wait for the
	// rollback to settle so its message can be recorded.
	msg := ""
	for range 24 {
		svc, err := u.dc.ServiceInspectRaw(ctx, u.cfg.Service)
		if err == nil && svc.UpdateStatus != nil {
			msg = svc.UpdateStatus.Message
			if st := svc.UpdateStatus.State; st != "updating" && st != "rollback_started" {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	if why := u.failedTaskReason(ctx, j.Image); why != "" {
		msg = why
	} else if msg == "" || strings.HasPrefix(msg, "rollback") {
		msg = "the new version stopped before it was ready"
	}
	j.State, j.Error, j.FinishedAt = "rolled_back", msg, time.Now().Unix()
	u.setJob(ctx, j)
	u.log.Error("update rolled back", "to", j.To, "reason", msg)
	u.audit(ctx, sys, "SimplePanel update rolled back", j.From+" → "+j.To, false, msg)
	if u.notify != nil {
		u.notify.Send(notify.Event{Type: notify.UpdateFailed, Level: "error",
			Title:   "Updating SimplePanel to " + j.To + " was rolled back",
			Message: "Swarm put " + j.From + " back: " + msg + ". A copy of the database from before the update is at " + j.Snapshot + ".",
			Link:    "/#/settings/general"})
	}
}
