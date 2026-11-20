// Package monitor samples CPU, memory, disk and network use of the server
// and of every service, stores the history in SQLite, and raises alerts.
//
// It is deliberately lighter than a Prometheus + Loki stack: one goroutine
// reads /proc and Docker's one-shot container stats every 30 seconds. Raw
// samples are kept for a day, then averaged into 5-minute points that are
// kept for the configured number of days.
package monitor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/deploy"
	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/notify"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Interval between samples.
const Interval = 30 * time.Second

// Settings are the alert thresholds and history length.
type Settings struct {
	CPU           int `json:"cpu"`           // server CPU %, 0 = off
	Memory        int `json:"memory"`        // server memory %, 0 = off
	Disk          int `json:"disk"`          // disk %, 0 = off
	ServiceMemory int `json:"serviceMemory"` // % of a service's memory limit, 0 = off
	ServiceCPU    int `json:"serviceCpu"`    // % of a service's CPU limit, 0 = off
	Minutes       int `json:"minutes"`       // how long CPU/memory must stay high
	RetentionDays int `json:"retentionDays"`
}

// DefaultSettings are used until the owner changes them.
var DefaultSettings = Settings{CPU: 90, Memory: 90, Disk: 85, ServiceMemory: 90, ServiceCPU: 0, Minutes: 5, RetentionDays: 14}

const settingsKey = "monitoring"

// LoadSettings reads the settings, falling back to the defaults.
func LoadSettings(ctx context.Context, st *store.Store) Settings {
	s := DefaultSettings
	if v, _ := st.Setting(ctx, settingsKey); v != "" {
		_ = json.Unmarshal([]byte(v), &s)
	}
	return s
}

// Validate checks the settings' ranges.
func (s *Settings) Validate() error {
	for name, v := range map[string]int{"CPU": s.CPU, "memory": s.Memory, "disk": s.Disk, "service memory": s.ServiceMemory, "service CPU": s.ServiceCPU} {
		if v != 0 && (v < 10 || v > 100) {
			return fmt.Errorf("%s threshold must be 0 (off) or between 10 and 100", name)
		}
	}
	if s.Minutes < 1 || s.Minutes > 60 {
		return errors.New("minutes must be between 1 and 60")
	}
	if s.RetentionDays < 1 || s.RetentionDays > 90 {
		return errors.New("history must be kept between 1 and 90 days")
	}
	return nil
}

func SaveSettings(ctx context.Context, st *store.Store, s Settings) error {
	b, _ := json.Marshal(s)
	return st.SetSetting(ctx, settingsKey, string(b))
}

// Config locates the host's /proc and the disk to watch.
type Config struct {
	ProcDir  string // "/host/proc" when the host's /proc is mounted in, else "/proc"
	DiskPath string // a path on the disk to report (the data volume)
}

type cpuReading struct{ total, idle uint64 }

type netReading struct {
	rx, tx uint64
	at     time.Time
}

type ctrReading struct {
	st *docker.Stats
	at time.Time
}

type alertState struct {
	high   int // consecutive samples over the threshold
	low    int // consecutive samples under it while firing
	firing bool
}

type Monitor struct {
	cfg    Config
	store  *store.Store
	docker *docker.Client
	deploy *deploy.Manager
	notify *notify.Notifier
	log    *slog.Logger

	mu       sync.Mutex
	prevCPU  *cpuReading
	prevNet  *netReading
	prevCtr  map[string]ctrReading
	alerts   map[string]*alertState
	down     map[int64]bool
	host     HostInfo
	lastRoll time.Time
}

// HostInfo describes the server.
type HostInfo struct {
	CPUs     int    `json:"cpus"`
	MemTotal int64  `json:"memTotal"`
	HostProc bool   `json:"hostProc"` // false: network numbers are the panel container's own
	Source   string `json:"-"`
}

func New(cfg Config, st *store.Store, dc *docker.Client, dm *deploy.Manager, n *notify.Notifier, log *slog.Logger) *Monitor {
	if cfg.ProcDir == "" {
		cfg.ProcDir = "/proc"
		if fi, err := os.Stat("/host/proc/stat"); err == nil && !fi.IsDir() {
			cfg.ProcDir = "/host/proc"
		}
	}
	return &Monitor{cfg: cfg, store: st, docker: dc, deploy: dm, notify: n, log: log,
		prevCtr: map[string]ctrReading{}, alerts: map[string]*alertState{}, down: map[int64]bool{}}
}

// Host returns what is known about the server.
func (m *Monitor) Host() HostInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.host
}

// Start samples every Interval until ctx ends.
func (m *Monitor) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(Interval)
		defer t.Stop()
		m.sample(ctx) // primes the counters
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.sample(ctx)
			}
		}
	}()
}

func (m *Monitor) sample(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	now := time.Now()
	set := LoadSettings(ctx, m.store)
	var samples []store.Sample

	srv, ok := m.serverSample(now)
	if ok {
		samples = append(samples, srv)
	}
	apps, err := m.store.Apps(ctx, 0)
	if err != nil {
		m.log.Error("monitor: list apps", "err", err)
		return
	}
	byID := map[int64]*store.App{}
	for i := range apps {
		byID[apps[i].ID] = &apps[i]
	}
	svc := m.serviceSamples(ctx, now, byID)
	samples = append(samples, svc...)
	if len(samples) > 0 {
		if err := m.store.AddSamples(ctx, samples); err != nil {
			m.log.Error("monitor: save samples", "err", err)
		}
	}
	if ok {
		m.checkServer(srv, set)
	}
	m.checkServices(ctx, svc, byID, set)
	if now.Sub(m.lastRoll) > time.Hour {
		m.lastRoll = now
		keep := now.AddDate(0, 0, -set.RetentionDays).Unix()
		if err := m.store.RollupMetrics(ctx, now.Add(-24*time.Hour).Unix(), keep); err != nil {
			m.log.Error("monitor: rollup", "err", err)
		}
	}
}

func (m *Monitor) proc(name string) string { return filepath.Join(m.cfg.ProcDir, name) }

func readCPU(path string) (*cpuReading, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		var r cpuReading
		for i, v := range fields[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			if i == 8 || i == 9 { // guest time is already counted in user time
				continue
			}
			r.total += n
			if i == 3 || i == 4 { // idle, iowait
				r.idle += n
			}
		}
		return &r, nil
	}
	return nil, errors.New("no cpu line in " + path)
}

func readMem(path string) (total, avail int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		switch k {
		case "MemTotal":
			total = n << 10
		case "MemAvailable":
			avail = n << 10
		}
	}
	return total, avail, sc.Err()
}

// physical reports whether a network interface carries real traffic (not
// loopback or Docker's virtual bridges).
func physical(iface string) bool {
	for _, p := range []string{"lo", "veth", "docker", "br-", "vx-", "virbr", "tun", "tap", "cni", "flannel", "cali"} {
		if strings.HasPrefix(iface, p) {
			return false
		}
	}
	return true
}

func readNet(path string) (rx, tx uint64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		iface, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		iface = strings.TrimSpace(iface)
		fields := strings.Fields(rest)
		if len(fields) < 9 || !physical(iface) {
			continue
		}
		r, _ := strconv.ParseUint(fields[0], 10, 64)
		t, _ := strconv.ParseUint(fields[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx, sc.Err()
}

func (m *Monitor) serverSample(now time.Time) (store.Sample, bool) {
	s := store.Sample{Target: store.ServerTarget, TS: now.Unix()}
	cpu, err := readCPU(m.proc("stat"))
	if err != nil {
		m.log.Warn("monitor: cpu", "err", err)
		return s, false
	}
	m.mu.Lock()
	prev := m.prevCPU
	m.prevCPU = cpu
	m.mu.Unlock()
	if prev == nil || cpu.total <= prev.total {
		return s, false
	}
	dt, di := float64(cpu.total-prev.total), float64(cpu.idle-prev.idle)
	s.CPU = max(0, min(100, (dt-di)/dt*100))
	total, avail, err := readMem(m.proc("meminfo"))
	if err == nil {
		s.Mem, s.MemLimit = total-avail, total
	}
	netPath := m.proc("net/dev")
	if m.cfg.ProcDir != "/proc" {
		netPath = m.proc("1/net/dev") // the host's network namespace
	}
	if rx, tx, err := readNet(netPath); err == nil {
		m.mu.Lock()
		if p := m.prevNet; p != nil && rx >= p.rx && tx >= p.tx {
			secs := now.Sub(p.at).Seconds()
			s.NetRx, s.NetTx = float64(rx-p.rx)/secs, float64(tx-p.tx)/secs
		}
		m.prevNet = &netReading{rx, tx, now}
		m.mu.Unlock()
	}
	var fs syscall.Statfs_t
	if m.cfg.DiskPath != "" && syscall.Statfs(m.cfg.DiskPath, &fs) == nil {
		s.DiskTotal = int64(fs.Blocks) * int64(fs.Bsize)
		s.DiskUsed = s.DiskTotal - int64(fs.Bavail)*int64(fs.Bsize)
	}
	m.mu.Lock()
	m.host = HostInfo{CPUs: m.host.CPUs, MemTotal: total, HostProc: m.cfg.ProcDir != "/proc"}
	if m.host.CPUs == 0 {
		if info, err := m.docker.Info(context.Background()); err == nil {
			m.host.CPUs = info.NCPU
		}
	}
	m.mu.Unlock()
	return s, true
}

func (m *Monitor) serviceSamples(ctx context.Context, now time.Time, apps map[int64]*store.App) []store.Sample {
	ctrs, err := m.docker.ManagedContainers(ctx)
	if err != nil {
		return nil
	}
	type result struct {
		app int64
		id  string
		st  *docker.Stats
	}
	results := make(chan result, len(ctrs))
	// Leave most of the Docker client's request slots to the UI.
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for _, c := range ctrs {
		appID := docker.AppIDLabel(c)
		if apps[appID] == nil {
			continue
		}
		wg.Add(1)
		go func(c docker.Container) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			st, err := m.docker.ContainerStats(ctx, c.ID)
			if err == nil {
				results <- result{appID, c.ID, st}
			}
		}(c)
	}
	wg.Wait()
	close(results)

	m.mu.Lock()
	defer m.mu.Unlock()
	host := m.host
	agg := map[int64]*store.Sample{}
	seen := map[string]bool{}
	for r := range results {
		seen[r.id] = true
		s := agg[r.app]
		if s == nil {
			s = &store.Sample{Target: r.app, TS: now.Unix()}
			agg[r.app] = s
		}
		s.Mem += r.st.Mem
		if p, ok := m.prevCtr[r.id]; ok {
			if dsys := float64(r.st.SystemCPU) - float64(p.st.SystemCPU); dsys > 0 && r.st.CPUTotal >= p.st.CPUTotal {
				s.CPU += float64(r.st.CPUTotal-p.st.CPUTotal) / dsys * 100
			}
			if secs := now.Sub(p.at).Seconds(); secs > 0 && r.st.NetRx >= p.st.NetRx && r.st.NetTx >= p.st.NetTx {
				s.NetRx += float64(r.st.NetRx-p.st.NetRx) / secs
				s.NetTx += float64(r.st.NetTx-p.st.NetTx) / secs
			}
		}
		m.prevCtr[r.id] = ctrReading{r.st, now}
	}
	for id := range m.prevCtr {
		if !seen[id] {
			delete(m.prevCtr, id)
		}
	}
	out := make([]store.Sample, 0, len(agg))
	for id, s := range agg {
		a := apps[id]
		s.CPU = min(100, s.CPU)
		if a.MemoryMB > 0 {
			s.MemLimit = int64(a.MemoryMB) << 20 * int64(max(a.Replicas, 1))
		} else {
			s.MemLimit = host.MemTotal
		}
		out = append(out, *s)
	}
	return out
}

// check updates an alert's state and returns "fire", "resolve" or "".
func (m *Monitor) check(key string, value float64, threshold int, sustain int) string {
	st := m.alerts[key]
	if st == nil {
		st = &alertState{}
		m.alerts[key] = st
	}
	if threshold <= 0 {
		if st.firing {
			st.firing = false
			return "resolve"
		}
		return ""
	}
	if value >= float64(threshold) {
		st.high++
		st.low = 0
		if !st.firing && st.high >= sustain {
			st.firing = true
			return "fire"
		}
		return ""
	}
	st.high = 0
	if st.firing {
		st.low++
		if st.low >= 2 {
			st.firing = false
			return "resolve"
		}
	}
	return ""
}

func gib(n int64) string { return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30)) }

func (m *Monitor) send(ev notify.Event) {
	if m.notify != nil {
		m.notify.Send(ev)
	}
}

func (m *Monitor) checkServer(s store.Sample, set Settings) {
	sustain := max(1, int(time.Duration(set.Minutes)*time.Minute/Interval))
	m.mu.Lock()
	cpu := m.check("server:cpu", s.CPU, set.CPU, sustain)
	var memPct, diskPct float64
	if s.MemLimit > 0 {
		memPct = float64(s.Mem) / float64(s.MemLimit) * 100
	}
	mem := m.check("server:mem", memPct, set.Memory, sustain)
	disk := ""
	if s.DiskTotal > 0 {
		diskPct = float64(s.DiskUsed) / float64(s.DiskTotal) * 100
		disk = m.check("server:disk", diskPct, set.Disk, 1)
	}
	m.mu.Unlock()
	link := "/#/"
	switch cpu {
	case "fire":
		m.send(notify.Event{Type: notify.AlertCPU, Level: "warning", Title: fmt.Sprintf("Server CPU at %.0f%%", s.CPU),
			Message: fmt.Sprintf("CPU use has been above %d%% for %d minutes.", set.CPU, set.Minutes), Link: link})
	case "resolve":
		m.send(notify.Event{Type: notify.AlertResolved, Level: "success", Title: "Server CPU back to normal",
			Message: fmt.Sprintf("CPU use is %.0f%%.", s.CPU), Link: link})
	}
	switch mem {
	case "fire":
		m.send(notify.Event{Type: notify.AlertMemory, Level: "warning", Title: fmt.Sprintf("Server memory at %.0f%%", memPct),
			Message: fmt.Sprintf("%s of %s in use for %d minutes.", gib(s.Mem), gib(s.MemLimit), set.Minutes), Link: link})
	case "resolve":
		m.send(notify.Event{Type: notify.AlertResolved, Level: "success", Title: "Server memory back to normal",
			Message: fmt.Sprintf("%s of %s in use.", gib(s.Mem), gib(s.MemLimit)), Link: link})
	}
	switch disk {
	case "fire":
		m.send(notify.Event{Type: notify.AlertDisk, Level: "error", Title: fmt.Sprintf("Disk %.0f%% full", diskPct),
			Message: fmt.Sprintf("%s of %s used. Free up space or old images before deploys and backups fail.", gib(s.DiskUsed), gib(s.DiskTotal)), Link: link})
	case "resolve":
		m.send(notify.Event{Type: notify.AlertResolved, Level: "success", Title: "Disk space back to normal",
			Message: fmt.Sprintf("%s of %s used.", gib(s.DiskUsed), gib(s.DiskTotal)), Link: link})
	}
}

func (m *Monitor) checkServices(ctx context.Context, samples []store.Sample, apps map[int64]*store.App, set Settings) {
	sustain := max(1, int(time.Duration(set.Minutes)*time.Minute/Interval))
	host := m.Host()
	for _, s := range samples {
		a := apps[s.Target]
		if a == nil {
			continue
		}
		ev := notify.Event{ProjectID: a.ProjectID, Project: a.ProjectName, Service: a.Name,
			Link: fmt.Sprintf("/#/p/%d/a/%d/metrics", a.ProjectID, a.ID)}
		if a.MemoryMB > 0 && s.MemLimit > 0 {
			pct := float64(s.Mem) / float64(s.MemLimit) * 100
			m.mu.Lock()
			r := m.check(fmt.Sprintf("app:%d:mem", a.ID), pct, set.ServiceMemory, sustain)
			m.mu.Unlock()
			switch r {
			case "fire":
				e := ev
				e.Type, e.Level, e.Title = notify.AlertMemory, "warning", fmt.Sprintf("%s/%s near its memory limit", a.ProjectName, a.Name)
				e.Message = fmt.Sprintf("Using %.0f%% of its %d MB limit. It will be restarted if it runs out.", pct, a.MemoryMB)
				m.send(e)
			case "resolve":
				e := ev
				e.Type, e.Level, e.Title = notify.AlertResolved, "success", fmt.Sprintf("%s/%s memory back to normal", a.ProjectName, a.Name)
				e.Message = fmt.Sprintf("Using %.0f%% of its limit.", pct)
				m.send(e)
			}
		}
		if a.CPULimit > 0 && host.CPUs > 0 {
			cores := s.CPU / 100 * float64(host.CPUs)
			pct := cores / (a.CPULimit * float64(max(a.Replicas, 1))) * 100
			m.mu.Lock()
			r := m.check(fmt.Sprintf("app:%d:cpu", a.ID), pct, set.ServiceCPU, sustain)
			m.mu.Unlock()
			switch r {
			case "fire":
				e := ev
				e.Type, e.Level, e.Title = notify.AlertCPU, "warning", fmt.Sprintf("%s/%s at its CPU limit", a.ProjectName, a.Name)
				e.Message = fmt.Sprintf("Using %.0f%% of its %.2g CPU limit for %d minutes.", pct, a.CPULimit, set.Minutes)
				m.send(e)
			case "resolve":
				e := ev
				e.Type, e.Level, e.Title = notify.AlertResolved, "success", fmt.Sprintf("%s/%s CPU back to normal", a.ProjectName, a.Name)
				m.send(e)
			}
		}
	}
	// Crash loops: a service whose tasks keep failing.
	list := make([]*store.App, 0, len(apps))
	for _, a := range apps {
		list = append(list, a)
	}
	statuses := m.deploy.Statuses(ctx, list)
	for _, a := range apps {
		st := statuses[a.ID]
		m.mu.Lock()
		was := m.down[a.ID]
		isDown := st.State == "failing"
		if isDown {
			m.down[a.ID] = true
		} else if st.State == "running" || st.State == "stopped" || st.State == "not_deployed" {
			delete(m.down, a.ID)
		}
		m.mu.Unlock()
		ev := notify.Event{ProjectID: a.ProjectID, Project: a.ProjectName, Service: a.Name,
			Link: fmt.Sprintf("/#/p/%d/a/%d", a.ProjectID, a.ID)}
		if isDown && !was {
			ev.Type, ev.Level, ev.Title = notify.ServiceDown, "error", fmt.Sprintf("%s/%s keeps crashing", a.ProjectName, a.Name)
			ev.Message = "Its containers are failing to start: " + st.Message
			m.send(ev)
		} else if was && st.State == "running" {
			ev.Type, ev.Level, ev.Title = notify.ServiceRecovered, "success", fmt.Sprintf("%s/%s is running again", a.ProjectName, a.Name)
			m.send(ev)
		}
	}
}

// MemInfo is the server's memory right now, in bytes.
type MemInfo struct {
	Total     int64
	Available int64
	SwapTotal int64
	SwapFree  int64
}

// Memory reads the server's memory from /proc/meminfo.
func (m *Monitor) Memory() (MemInfo, error) {
	var mi MemInfo
	f, err := os.Open(m.proc("meminfo"))
	if err != nil {
		return mi, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		switch k {
		case "MemTotal":
			mi.Total = n << 10
		case "MemAvailable":
			mi.Available = n << 10
		case "SwapTotal":
			mi.SwapTotal = n << 10
		case "SwapFree":
			mi.SwapFree = n << 10
		}
	}
	return mi, sc.Err()
}
