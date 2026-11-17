package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/monitor"
	"github.com/SimpleAOB/SimplePanel/internal/notify"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// ---- Monitoring ----

var metricRanges = map[string]time.Duration{
	"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour,
}

// samples returns a target's history for ?range=, using 5-minute averages
// for ranges longer than a day.
func (s *Server) samples(r *http.Request, target int64) (string, []store.Sample, error) {
	rng := r.URL.Query().Get("range")
	d, ok := metricRanges[rng]
	if !ok {
		rng, d = "1h", time.Hour
	}
	now := time.Now().Unix()
	from := now - int64(d.Seconds())
	if d <= 24*time.Hour {
		raw, err := s.store.Samples(r.Context(), target, store.ResRaw, from, now)
		if err != nil {
			return rng, nil, err
		}
		// Raw samples older than a day were rolled up already.
		return rng, raw, nil
	}
	old, err := s.store.Samples(r.Context(), target, store.Res5Min, from, now)
	if err != nil {
		return rng, nil, err
	}
	// The last day is still raw: average it into 5-minute points too.
	raw, err := s.store.Samples(r.Context(), target, store.ResRaw, from, now)
	if err != nil {
		return rng, nil, err
	}
	return rng, append(old, bucket(raw, 300)...), nil
}

// bucket averages samples into fixed windows.
func bucket(in []store.Sample, secs int64) []store.Sample {
	var out []store.Sample
	var acc store.Sample
	n := 0
	flush := func() {
		if n == 0 {
			return
		}
		f := float64(n)
		acc.CPU /= f
		acc.NetRx /= f
		acc.NetTx /= f
		acc.Mem /= int64(n)
		acc.DiskUsed /= int64(n)
		out = append(out, acc)
		n = 0
	}
	for _, x := range in {
		b := x.TS - x.TS%secs
		if n > 0 && b != acc.TS {
			flush()
		}
		if n == 0 {
			acc = store.Sample{TS: b, MemLimit: x.MemLimit, DiskTotal: x.DiskTotal}
		}
		acc.CPU += x.CPU
		acc.NetRx += x.NetRx
		acc.NetTx += x.NetTx
		acc.Mem += x.Mem
		acc.DiskUsed += x.DiskUsed
		n++
	}
	flush()
	return out
}

func (s *Server) serverMetrics(w http.ResponseWriter, r *http.Request, u *store.User) {
	rng, ss, err := s.samples(r, store.ServerTarget)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := map[string]any{"range": rng, "samples": ss, "interval": monitor.Interval.Seconds()}
	if s.monitor != nil {
		out["host"] = s.monitor.Host()
	}
	writeJSON(w, 200, out)
}

func (s *Server) appMetrics(w http.ResponseWriter, r *http.Request, u *store.User) {
	a, ok := s.loadApp(w, r, u, store.LevelView)
	if !ok {
		return
	}
	rng, ss, err := s.samples(r, a.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := map[string]any{"range": rng, "samples": ss, "interval": monitor.Interval.Seconds(),
		"memoryLimitMb": a.MemoryMB, "cpuLimit": a.CPULimit, "replicas": a.Replicas}
	if s.monitor != nil {
		out["host"] = s.monitor.Host()
	}
	writeJSON(w, 200, out)
}

// projectMetrics returns the latest sample of each service in a project.
func (s *Server) projectMetrics(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.allow(w, r, u, id, store.LevelView) {
		return
	}
	apps, err := s.store.Apps(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	latest, err := s.store.LatestSamples(r.Context(), time.Now().Add(-2*time.Minute).Unix())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := map[string]store.Sample{}
	for _, a := range apps {
		if x, ok := latest[a.ID]; ok {
			out[strconv.FormatInt(a.ID, 10)] = x
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) getMonitoringSettings(w http.ResponseWriter, r *http.Request, u *store.User) {
	writeJSON(w, 200, map[string]any{"settings": monitor.LoadSettings(r.Context(), s.store), "defaults": monitor.DefaultSettings})
}

func (s *Server) putMonitoringSettings(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	var in monitor.Settings
	if !readJSON(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := monitor.SaveSettings(r.Context(), s.store, in); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, in)
}

// ---- Notifications ----

type channelView struct {
	store.NotifyChannel
	Config map[string]string `json:"config"`
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	chs, err := s.store.NotifyChannels(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	views := make([]channelView, len(chs))
	for i := range chs {
		views[i] = channelView{chs[i], notify.PublicConfig(&chs[i])}
	}
	log, err := s.store.NotifyLog(r.Context(), 50)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"channels": views, "events": notify.EventTypes, "log": log})
}

type channelInput struct {
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Config   map[string]string `json:"config"`
	Events   []string          `json:"events"`
	Projects []int64           `json:"projects"`
	Enabled  *bool             `json:"enabled"`
}

// channelFrom builds a channel from input. Secret settings left empty
// keep their saved value when editing.
func (s *Server) channelFrom(r *http.Request, in channelInput, existing *store.NotifyChannel) (*store.NotifyChannel, error) {
	c := &store.NotifyChannel{Name: in.Name, Type: in.Type, Config: map[string]string{}, Events: in.Events, Projects: in.Projects, Enabled: true}
	if existing != nil {
		c.ID, c.CreatedAt, c.Enabled = existing.ID, existing.CreatedAt, existing.Enabled
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	for k, v := range in.Config {
		c.Config[k] = v
	}
	if existing != nil && existing.Type == c.Type {
		for k, v := range existing.Config {
			if notify.IsSecret(c.Type, k) && c.Config[k] == "" {
				c.Config[k] = v
			}
		}
	}
	if c.Projects == nil {
		c.Projects = []int64{}
	}
	for _, pid := range c.Projects {
		if _, err := s.store.Project(r.Context(), pid); err != nil {
			return nil, errors.New("projects lists a project that doesn't exist")
		}
	}
	slices.Sort(c.Projects)
	c.Projects = slices.Compact(c.Projects)
	return c, notify.Validate(c)
}

func (s *Server) saveChannel(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	var existing *store.NotifyChannel
	if r.PathValue("id") != "" {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, 400, "bad id")
			return
		}
		var err error
		if existing, err = s.store.NotifyChannel(r.Context(), id); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	var in channelInput
	if !readJSON(w, r, &in) {
		return
	}
	c, err := s.channelFrom(r, in, existing)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.store.SaveNotifyChannel(r.Context(), c); err != nil {
		s.storeErr(w, err)
		return
	}
	code := 200
	if existing == nil {
		code = 201
	}
	writeJSON(w, code, channelView{*c, notify.PublicConfig(c)})
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	if err := s.store.DeleteNotifyChannel(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// testChannel sends a test message, either through a saved channel
// (/notifications/{id}/test) or through unsaved settings in the body.
func (s *Server) testChannel(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !requireAdmin(w, u) {
		return
	}
	if !s.hookLimit.Allow("notify-test:" + strconv.FormatInt(u.ID, 10)) {
		writeErr(w, 429, "too many tests, wait a minute")
		return
	}
	var existing *store.NotifyChannel
	if r.PathValue("id") != "" {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, 400, "bad id")
			return
		}
		var err error
		if existing, err = s.store.NotifyChannel(r.Context(), id); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	var in channelInput
	if !readJSON(w, r, &in) {
		return
	}
	if in.Type == "" && existing != nil {
		in = channelInput{Name: existing.Name, Type: existing.Type, Events: existing.Events, Config: existing.Config, Projects: existing.Projects}
	}
	if len(in.Events) == 0 {
		in.Events = []string{notify.DeployFailed}
	}
	if in.Name == "" {
		in.Name = "test"
	}
	c, err := s.channelFrom(r, in, existing)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if s.notify == nil {
		writeErr(w, 503, "notifications are not running")
		return
	}
	if err := s.notify.SendTest(r.Context(), c); err != nil {
		writeErr(w, 502, "sending failed: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
