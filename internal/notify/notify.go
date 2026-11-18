// Package notify sends SimplePanel events (failed deploys, failed backups,
// resource alerts…) to Discord, Slack, Telegram, email and generic
// webhooks.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// Event types.
const (
	DeployFailed     = "deploy.failed"
	DeploySucceeded  = "deploy.succeeded"
	BackupFailed     = "backup.failed"
	BackupSucceeded  = "backup.succeeded"
	RestoreFinished  = "restore.finished"
	ServiceDown      = "service.down"
	ServiceRecovered = "service.recovered"
	AlertCPU         = "alert.cpu"
	AlertMemory      = "alert.memory"
	AlertDisk        = "alert.disk"
	AlertResolved    = "alert.resolved"
	CronFailed       = "cron.failed"
	PreviewReady     = "preview.ready"
	Test             = "test"
)

// EventType describes an event for the settings UI.
type EventType struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Default bool   `json:"default"` // selected for new channels
}

var EventTypes = []EventType{
	{DeployFailed, "Deploy failed", true},
	{DeploySucceeded, "Deploy succeeded", false},
	{ServiceDown, "Service keeps crashing", true},
	{ServiceRecovered, "Service recovered", true},
	{BackupFailed, "Backup failed", true},
	{BackupSucceeded, "Backup succeeded", false},
	{RestoreFinished, "Restore finished", true},
	{CronFailed, "Cron job failed", true},
	{PreviewReady, "Pull request preview ready", false},
	{AlertCPU, "High CPU", true},
	{AlertMemory, "High memory", true},
	{AlertDisk, "Disk almost full", true},
	{AlertResolved, "Alert resolved", true},
}

func validEvent(e string) bool {
	return slices.ContainsFunc(EventTypes, func(t EventType) bool { return t.Key == e })
}

// Event is something worth telling people about.
type Event struct {
	Type      string    `json:"event"`
	Level     string    `json:"level"` // info|success|warning|error
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	ProjectID int64     `json:"-"`
	Project   string    `json:"project,omitempty"`
	Service   string    `json:"service,omitempty"`
	Link      string    `json:"url,omitempty"`
	Time      time.Time `json:"time"`
}

// Channel types and their settings. Secret settings are write-only.
var channelFields = map[string]struct {
	required []string
	optional []string
	secret   []string
}{
	"discord":  {required: []string{"url"}, secret: []string{"url"}},
	"slack":    {required: []string{"url"}, secret: []string{"url"}},
	"telegram": {required: []string{"token", "chatId"}, secret: []string{"token"}},
	"email": {required: []string{"host", "port", "from", "to"}, optional: []string{"username", "password", "security"},
		secret: []string{"password"}},
	"webhook": {required: []string{"url"}, optional: []string{"secret"}, secret: []string{"secret"}},
}

// IsSecret reports whether a channel setting is write-only.
func IsSecret(typ, field string) bool {
	return slices.Contains(channelFields[typ].secret, field)
}

// Validate checks a channel's name, type, settings and events.
func Validate(c *store.NotifyChannel) error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" || len(c.Name) > 60 {
		return errors.New("give the channel a name of up to 60 characters")
	}
	f, ok := channelFields[c.Type]
	if !ok {
		return errors.New("type must be discord, slack, telegram, email or webhook")
	}
	for k, v := range c.Config {
		if !slices.Contains(f.required, k) && !slices.Contains(f.optional, k) {
			delete(c.Config, k)
			continue
		}
		c.Config[k] = strings.TrimSpace(v)
		if len(c.Config[k]) > 2000 || strings.ContainsAny(c.Config[k], "\r\n") {
			return fmt.Errorf("%s is too long or has line breaks", k)
		}
	}
	for _, k := range f.required {
		if c.Config[k] == "" {
			return fmt.Errorf("%s is required", k)
		}
	}
	switch c.Type {
	case "discord", "slack", "webhook":
		u, err := url.Parse(c.Config["url"])
		if err != nil || (u.Scheme != "https" && !(c.Type == "webhook" && u.Scheme == "http")) || u.Host == "" {
			return errors.New("url must be an https:// URL")
		}
	case "telegram":
		if strings.ContainsAny(c.Config["token"], "/ ?#") {
			return errors.New("bot token looks invalid")
		}
	case "email":
		if p, err := strconv.Atoi(c.Config["port"]); err != nil || p < 1 || p > 65535 {
			return errors.New("port must be a number between 1 and 65535")
		}
		if _, err := mail.ParseAddress(c.Config["from"]); err != nil {
			return errors.New("from must be an email address")
		}
		if _, err := mail.ParseAddressList(c.Config["to"]); err != nil {
			return errors.New("to must be one or more email addresses, separated by commas")
		}
		switch c.Config["security"] {
		case "":
			c.Config["security"] = "starttls"
		case "starttls", "tls", "none":
		default:
			return errors.New(`security must be "starttls", "tls" or "none"`)
		}
	}
	if len(c.Events) == 0 {
		return errors.New("pick at least one event")
	}
	for _, e := range c.Events {
		if !validEvent(e) {
			return fmt.Errorf("unknown event %q", e)
		}
	}
	return nil
}

// PublicConfig returns the channel's settings with secrets masked.
func PublicConfig(c *store.NotifyChannel) map[string]string {
	out := map[string]string{}
	for k, v := range c.Config {
		if IsSecret(c.Type, k) && v != "" {
			if u, err := url.Parse(v); err == nil && u.Host != "" {
				v = u.Scheme + "://" + u.Host + "/…"
			} else {
				v = "••••••••"
			}
		}
		out[k] = v
	}
	return out
}

// Notifier delivers events to the configured channels.
type Notifier struct {
	store *store.Store
	log   *slog.Logger
	http  *http.Client
	// BaseURL returns the panel's public URL for links ("" = no links).
	BaseURL func() string
}

func New(st *store.Store, log *slog.Logger) *Notifier {
	return &Notifier{store: st, log: log, http: &http.Client{Timeout: 15 * time.Second}}
}

// Send delivers ev in the background to every enabled channel that wants it.
func (n *Notifier) Send(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	if ev.Link != "" && n.BaseURL != nil {
		if base := n.BaseURL(); base != "" {
			ev.Link = base + ev.Link
		} else {
			ev.Link = ""
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		chs, err := n.store.NotifyChannels(ctx)
		if err != nil {
			n.log.Error("notifications", "err", err)
			return
		}
		for i := range chs {
			c := &chs[i]
			if !c.Enabled || !slices.Contains(c.Events, ev.Type) {
				continue
			}
			if len(c.Projects) > 0 && ev.ProjectID != 0 && !slices.Contains(c.Projects, ev.ProjectID) {
				continue
			}
			go n.deliver(ctx, c, ev)
		}
	}()
}

// SendTest delivers a test message to one channel and waits for the result.
func (n *Notifier) SendTest(ctx context.Context, c *store.NotifyChannel) error {
	ev := Event{Type: Test, Level: "info", Title: "SimplePanel test notification",
		Message: "Notifications from SimplePanel will arrive here.", Time: time.Now()}
	if n.BaseURL != nil {
		ev.Link = n.BaseURL()
	}
	return n.deliver(ctx, c, ev)
}

func (n *Notifier) deliver(ctx context.Context, c *store.NotifyChannel, ev Event) error {
	var err error
	switch c.Type {
	case "discord":
		err = n.discord(ctx, c.Config, ev)
	case "slack":
		err = n.slack(ctx, c.Config, ev)
	case "telegram":
		err = n.telegram(ctx, c.Config, ev)
	case "email":
		err = sendEmail(ctx, c.Config, ev)
	case "webhook":
		err = n.webhook(ctx, c.Config, ev)
	default:
		err = fmt.Errorf("unknown channel type %q", c.Type)
	}
	entry := &store.NotifyLogEntry{ChannelID: c.ID, Event: ev.Type, Title: ev.Title, OK: err == nil}
	if err != nil {
		entry.Error = truncate(err.Error(), 300)
		n.log.Warn("notification failed", "channel", c.Name, "event", ev.Type, "err", err)
	}
	if c.ID != 0 {
		_ = n.store.AddNotifyLog(context.WithoutCancel(ctx), entry)
	}
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (n *Notifier) postJSON(ctx context.Context, u string, body any, hdr map[string]string) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SimplePanel")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := n.http.Do(req)
	if err != nil {
		// Don't echo URLs: they often contain the channel's secret.
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("couldn't reach %s: %v", req.URL.Host, ue.Err)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s answered HTTP %d: %s", req.URL.Host, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

var levelColor = map[string]int{"error": 0xf2555a, "warning": 0xf0b232, "success": 0x3ecf8e, "info": 0x5b6cff}

func context2(ev Event) string {
	var parts []string
	if ev.Project != "" {
		s := ev.Project
		if ev.Service != "" {
			s += "/" + ev.Service
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func (n *Notifier) discord(ctx context.Context, cfg map[string]string, ev Event) error {
	embed := map[string]any{
		"title": truncate(ev.Title, 250), "description": truncate(ev.Message, 3500),
		"color": levelColor[ev.Level], "timestamp": ev.Time.UTC().Format(time.RFC3339),
	}
	if ev.Link != "" {
		embed["url"] = ev.Link
	}
	if c := context2(ev); c != "" {
		embed["footer"] = map[string]string{"text": c}
	}
	return n.postJSON(ctx, cfg["url"], map[string]any{"username": "SimplePanel", "embeds": []any{embed}}, nil)
}

func (n *Notifier) slack(ctx context.Context, cfg map[string]string, ev Event) error {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	text := "*" + esc.Replace(ev.Title) + "*"
	if ev.Message != "" {
		text += "\n" + esc.Replace(ev.Message)
	}
	if c := context2(ev); c != "" {
		text += "\n_" + esc.Replace(c) + "_"
	}
	if ev.Link != "" {
		text += "\n<" + ev.Link + "|Open in SimplePanel>"
	}
	return n.postJSON(ctx, cfg["url"], map[string]any{"text": text}, nil)
}

func (n *Notifier) telegram(ctx context.Context, cfg map[string]string, ev Event) error {
	text := "<b>" + html.EscapeString(ev.Title) + "</b>"
	if ev.Message != "" {
		text += "\n" + html.EscapeString(truncate(ev.Message, 3500))
	}
	if c := context2(ev); c != "" {
		text += "\n<i>" + html.EscapeString(c) + "</i>"
	}
	if ev.Link != "" {
		text += "\n<a href=\"" + html.EscapeString(ev.Link) + "\">Open in SimplePanel</a>"
	}
	return n.postJSON(ctx, "https://api.telegram.org/bot"+cfg["token"]+"/sendMessage", map[string]any{
		"chat_id": cfg["chatId"], "text": text, "parse_mode": "HTML", "disable_web_page_preview": true,
	}, nil)
}

// Sign returns the X-SimplePanel-Signature value for body.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func (n *Notifier) webhook(ctx context.Context, cfg map[string]string, ev Event) error {
	body, _ := json.Marshal(ev)
	hdr := map[string]string{"X-SimplePanel-Event": ev.Type}
	if s := cfg["secret"]; s != "" {
		hdr["X-SimplePanel-Signature"] = Sign(s, body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg["url"], bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SimplePanel")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := n.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("couldn't reach %s: %v", req.URL.Host, ue.Err)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s answered HTTP %d", req.URL.Host, resp.StatusCode)
	}
	return nil
}

func sendEmail(ctx context.Context, cfg map[string]string, ev Event) error {
	from, _ := mail.ParseAddress(cfg["from"])
	to, err := mail.ParseAddressList(cfg["to"])
	if err != nil || from == nil {
		return errors.New("invalid from or to address")
	}
	host := cfg["host"]
	addr := net.JoinHostPort(host, cfg["port"])
	d := net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	if cfg["security"] == "tls" {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("couldn't connect to %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if cfg["security"] == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the server doesn't support STARTTLS; pick TLS or None")
		}
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if cfg["username"] != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg["username"], cfg["password"], host)); err != nil {
			return fmt.Errorf("sign-in failed: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	var rcpt []string
	for _, a := range to {
		if err := c.Rcpt(a.Address); err != nil {
			return err
		}
		rcpt = append(rcpt, a.String())
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	body := ev.Message
	if cx := context2(ev); cx != "" {
		body += "\n\n" + cx
	}
	if ev.Link != "" {
		body += "\n\nOpen in SimplePanel: " + ev.Link
	}
	msg := "From: " + from.String() + "\r\n" +
		"To: " + strings.Join(rcpt, ", ") + "\r\n" +
		"Subject: " + mimeHeader("[SimplePanel] "+ev.Title) + "\r\n" +
		"Date: " + ev.Time.Format(time.RFC1123Z) + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n" +
		"Auto-Submitted: auto-generated\r\n\r\n" +
		strings.ReplaceAll(body, "\n", "\r\n") + "\r\n"
	if _, err := io.WriteString(w, msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func mimeHeader(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	for _, r := range s {
		if r > 127 {
			return "=?utf-8?q?" + qEncode(s) + "?="
		}
	}
	return s
}

func qEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
			b.WriteByte('_')
		case c > 127 || c == '=' || c == '?' || c == '_' || c < 32:
			fmt.Fprintf(&b, "=%02X", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
