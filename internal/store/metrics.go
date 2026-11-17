package store

import (
	"context"
)

// Metric resolutions. Raw samples are rolled up into 5-minute averages
// once they are a day old, which keeps a month of history small.
const (
	ResRaw  = 0
	Res5Min = 1
)

// ServerTarget is the metrics target id of the server itself; services use
// their app id.
const ServerTarget = 0

// Sample is one point of a target's resource use.
type Sample struct {
	Target    int64   `json:"-"`
	TS        int64   `json:"t"`
	CPU       float64 `json:"cpu"`      // percent of the whole server
	Mem       int64   `json:"mem"`      // bytes
	MemLimit  int64   `json:"memLimit"` // bytes: the service's limit, or the server's total
	NetRx     float64 `json:"rx"`       // bytes per second
	NetTx     float64 `json:"tx"`
	DiskUsed  int64   `json:"disk,omitempty"`
	DiskTotal int64   `json:"diskTotal,omitempty"`
}

// AddSamples stores raw samples in one transaction.
func (s *Store) AddSamples(ctx context.Context, samples []Sample) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO metrics(target,res,ts,cpu,mem,mem_limit,net_rx,net_tx,disk_used,disk_total)
		VALUES (?,0,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, m := range samples {
		if _, err := st.ExecContext(ctx, m.Target, m.TS, m.CPU, m.Mem, m.MemLimit, m.NetRx, m.NetTx, m.DiskUsed, m.DiskTotal); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Samples returns a target's samples in [from, to] at resolution res.
func (s *Store) Samples(ctx context.Context, target int64, res int, from, to int64) ([]Sample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts,cpu,mem,mem_limit,net_rx,net_tx,disk_used,disk_total FROM metrics
		WHERE target=? AND res=? AND ts>=? AND ts<=? ORDER BY ts`, target, res, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Sample{}
	for rows.Next() {
		m := Sample{Target: target}
		if err := rows.Scan(&m.TS, &m.CPU, &m.Mem, &m.MemLimit, &m.NetRx, &m.NetTx, &m.DiskUsed, &m.DiskTotal); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LatestSamples returns the newest raw sample of every target newer than since.
func (s *Store) LatestSamples(ctx context.Context, since int64) (map[int64]Sample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.target,m.ts,m.cpu,m.mem,m.mem_limit,m.net_rx,m.net_tx,m.disk_used,m.disk_total
		FROM metrics m JOIN (SELECT target, MAX(ts) AS ts FROM metrics WHERE res=0 AND ts>=? GROUP BY target) l
		ON l.target=m.target AND l.ts=m.ts WHERE m.res=0`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Sample{}
	for rows.Next() {
		var m Sample
		if err := rows.Scan(&m.Target, &m.TS, &m.CPU, &m.Mem, &m.MemLimit, &m.NetRx, &m.NetTx, &m.DiskUsed, &m.DiskTotal); err != nil {
			return nil, err
		}
		out[m.Target] = m
	}
	return out, rows.Err()
}

// RollupMetrics averages raw samples older than rawBefore into 5-minute
// buckets, deletes them, and drops rolled-up samples older than keepAfter.
func (s *Store) RollupMetrics(ctx context.Context, rawBefore, keepAfter int64) error {
	rawBefore -= rawBefore % 300 // only whole buckets
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO metrics(target,res,ts,cpu,mem,mem_limit,net_rx,net_tx,disk_used,disk_total)
		SELECT target, 1, ts - ts % 300, AVG(cpu), CAST(AVG(mem) AS INTEGER), MAX(mem_limit), AVG(net_rx), AVG(net_tx),
			CAST(AVG(disk_used) AS INTEGER), MAX(disk_total)
		FROM metrics WHERE res=0 AND ts<? GROUP BY target, ts - ts % 300`, rawBefore); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM metrics WHERE res=0 AND ts<?`, rawBefore); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM metrics WHERE res=1 AND ts<?`, keepAfter); err != nil {
		return err
	}
	// Services that were deleted.
	if _, err := tx.ExecContext(ctx, `DELETE FROM metrics WHERE target<>0 AND target NOT IN (SELECT id FROM apps)`); err != nil {
		return err
	}
	return tx.Commit()
}
