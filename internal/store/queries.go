package store

import (
	"context"
	"database/sql"
)

// QueryPassword returns the password of a database's read-only Query tab
// user ("" until the tab is first used). It never leaves the server.
func (s *Store) QueryPassword(ctx context.Context, appID int64) (string, error) {
	var pw string
	err := s.db.QueryRowContext(ctx, `SELECT query_password FROM apps WHERE id=?`, appID).Scan(&pw)
	return pw, mapErr(err)
}

func (s *Store) SetQueryPassword(ctx context.Context, appID int64, password string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET query_password=? WHERE id=?`, password, appID)
	return err
}

// SavedQuery is a named query shared by everyone who can query the database.
type SavedQuery struct {
	ID        int64  `json:"id"`
	AppID     int64  `json:"appId"`
	UserID    *int64 `json:"userId"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	Query     string `json:"query"`
	CreatedAt int64  `json:"createdAt"`
}

const savedQueriesMax = 200

func (s *Store) SavedQueries(ctx context.Context, appID int64) ([]SavedQuery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,app_id,user_id,username,name,query,created_at FROM saved_queries WHERE app_id=? ORDER BY name COLLATE NOCASE, id`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SavedQuery{}
	for rows.Next() {
		var q SavedQuery
		var uid sql.NullInt64
		if err := rows.Scan(&q.ID, &q.AppID, &uid, &q.Username, &q.Name, &q.Query, &q.CreatedAt); err != nil {
			return nil, err
		}
		if uid.Valid {
			q.UserID = &uid.Int64
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (s *Store) SavedQuery(ctx context.Context, id int64) (*SavedQuery, error) {
	var q SavedQuery
	var uid sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id,app_id,user_id,username,name,query,created_at FROM saved_queries WHERE id=?`, id).
		Scan(&q.ID, &q.AppID, &uid, &q.Username, &q.Name, &q.Query, &q.CreatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	if uid.Valid {
		q.UserID = &uid.Int64
	}
	return &q, nil
}

// AddSavedQuery saves q; a database keeps at most savedQueriesMax.
func (s *Store) AddSavedQuery(ctx context.Context, q *SavedQuery) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM saved_queries WHERE app_id=?`, q.AppID).Scan(&n); err != nil {
		return err
	}
	if n >= savedQueriesMax {
		return ErrConflict
	}
	q.CreatedAt = now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO saved_queries(app_id,user_id,username,name,query,created_at) VALUES (?,?,?,?,?,?)`,
		q.AppID, q.UserID, q.Username, q.Name, q.Query, q.CreatedAt)
	if err != nil {
		return err
	}
	q.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) DeleteSavedQuery(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM saved_queries WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// QueryRun is one entry of a user's query history.
type QueryRun struct {
	ID        int64  `json:"id"`
	Query     string `json:"query"`
	OK        bool   `json:"ok"`
	Rows      int    `json:"rows"`
	MS        int64  `json:"ms"`
	Error     string `json:"error"`
	CreatedAt int64  `json:"createdAt"`
}

// historyKeep is how many queries are kept per user and database.
const historyKeep = 100

func (s *Store) AddQueryRun(ctx context.Context, appID, userID int64, r *QueryRun) error {
	r.CreatedAt = now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO query_history(app_id,user_id,query,ok,rows,ms,error,created_at) VALUES (?,?,?,?,?,?,?,?)`,
		appID, userID, r.Query, r.OK, r.Rows, r.MS, r.Error, r.CreatedAt)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM query_history WHERE app_id=? AND user_id=? AND id NOT IN
		(SELECT id FROM query_history WHERE app_id=? AND user_id=? ORDER BY id DESC LIMIT ?)`, appID, userID, appID, userID, historyKeep)
	return err
}

// QueryHistory is a user's own recent queries on a database, newest first.
func (s *Store) QueryHistory(ctx context.Context, appID, userID int64) ([]QueryRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,query,ok,rows,ms,error,created_at FROM query_history WHERE app_id=? AND user_id=? ORDER BY id DESC LIMIT ?`, appID, userID, historyKeep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QueryRun{}
	for rows.Next() {
		var r QueryRun
		if err := rows.Scan(&r.ID, &r.Query, &r.OK, &r.Rows, &r.MS, &r.Error, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ClearQueryHistory(ctx context.Context, appID, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM query_history WHERE app_id=? AND user_id=?`, appID, userID)
	return err
}
