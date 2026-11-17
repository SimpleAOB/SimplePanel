package store

import (
	"context"
	"encoding/json"
)

// NotifyChannel is a place SimplePanel sends notifications to. Config holds
// type-specific settings, including secrets such as webhook URLs and SMTP
// passwords, and never leaves the server unmasked.
type NotifyChannel struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Type      string            `json:"type"` // discord|slack|telegram|email|webhook
	Config    map[string]string `json:"-"`
	Events    []string          `json:"events"`
	Projects  []int64           `json:"projects"` // empty = all projects
	Enabled   bool              `json:"enabled"`
	CreatedAt int64             `json:"createdAt"`
}

const channelCols = `id,name,type,config,events,projects,enabled,created_at`

func scanChannel(r scanner) (*NotifyChannel, error) {
	var c NotifyChannel
	var cfg, ev, pr string
	if err := r.Scan(&c.ID, &c.Name, &c.Type, &cfg, &ev, &pr, &c.Enabled, &c.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	_ = json.Unmarshal([]byte(cfg), &c.Config)
	_ = json.Unmarshal([]byte(ev), &c.Events)
	_ = json.Unmarshal([]byte(pr), &c.Projects)
	if c.Config == nil {
		c.Config = map[string]string{}
	}
	if c.Events == nil {
		c.Events = []string{}
	}
	if c.Projects == nil {
		c.Projects = []int64{}
	}
	return &c, nil
}

func (s *Store) NotifyChannels(ctx context.Context) ([]NotifyChannel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+channelCols+` FROM notify_channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotifyChannel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) NotifyChannel(ctx context.Context, id int64) (*NotifyChannel, error) {
	return scanChannel(s.db.QueryRowContext(ctx, `SELECT `+channelCols+` FROM notify_channels WHERE id=?`, id))
}

func (s *Store) SaveNotifyChannel(ctx context.Context, c *NotifyChannel) error {
	cfg, _ := json.Marshal(c.Config)
	ev, _ := json.Marshal(c.Events)
	pr, _ := json.Marshal(c.Projects)
	if c.ID == 0 {
		c.CreatedAt = now()
		res, err := s.db.ExecContext(ctx, `INSERT INTO notify_channels(name,type,config,events,projects,enabled,created_at) VALUES (?,?,?,?,?,?,?)`,
			c.Name, c.Type, string(cfg), string(ev), string(pr), c.Enabled, c.CreatedAt)
		if err != nil {
			return mapErr(err)
		}
		c.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE notify_channels SET name=?,type=?,config=?,events=?,projects=?,enabled=? WHERE id=?`,
		c.Name, c.Type, string(cfg), string(ev), string(pr), c.Enabled, c.ID)
	return mapErr(err)
}

func (s *Store) DeleteNotifyChannel(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM notify_channels WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// NotifyLogEntry is one delivery attempt.
type NotifyLogEntry struct {
	ID        int64  `json:"id"`
	ChannelID int64  `json:"channelId"`
	Event     string `json:"event"`
	Title     string `json:"title"`
	OK        bool   `json:"ok"`
	Error     string `json:"error"`
	CreatedAt int64  `json:"createdAt"`
}

const notifyLogKeep = 2000

func (s *Store) AddNotifyLog(ctx context.Context, e *NotifyLogEntry) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO notify_log(channel_id,event,title,ok,error,created_at) VALUES (?,?,?,?,?,?)`,
		e.ChannelID, e.Event, e.Title, e.OK, e.Error, now())
	if err != nil {
		return err
	}
	if id, _ := res.LastInsertId(); id%100 == 0 {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM notify_log WHERE id <= ?`, id-notifyLogKeep)
	}
	return nil
}

func (s *Store) NotifyLog(ctx context.Context, limit int) ([]NotifyLogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,channel_id,event,title,ok,error,created_at FROM notify_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotifyLogEntry{}
	for rows.Next() {
		var e NotifyLogEntry
		if err := rows.Scan(&e.ID, &e.ChannelID, &e.Event, &e.Title, &e.OK, &e.Error, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
