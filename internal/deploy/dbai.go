package deploy

// AI read-only database access.
//
// When a user turns it on for a database, SimplePanel creates a dedicated
// read-only database user (aiUser) inside it. MCP tools then run queries as
// that user by executing the database's own command-line client inside its
// container, so the panel needs no database drivers and no network path to
// the database. The read-only user's password stays on the server.
//
// Layers of protection, from strongest:
//   1. The user only has SELECT on the app's own data: PostgreSQL tables
//      and sequences outside the system schemas (plus default privileges
//      for tables the app creates later); MySQL and MariaDB SELECT and
//      SHOW VIEW on the app's database. It can't read password hashes.
//   2. Every session is read-only and has a statement timeout.
//   3. Results are capped in rows and bytes.

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

const (
	aiUser           = "sp_ai_readonly"
	aiQueryTimeout   = 10 * time.Second
	AIMaxRows        = 500
	aiMaxOutput      = 2 << 20 // bytes read back from the client
	pgNullMarker     = "␀"
	minPostgresMajor = 15 // PUBLIC can no longer create tables in public
)

// QueryResult is a capped query result.
type QueryResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	RowCount  int      `json:"rowCount"`
	Truncated bool     `json:"truncated,omitempty"`
}

func (m *Manager) dbVars(app *store.App) (*Engine, map[string]string, error) {
	e := EngineByKey(app.Kind)
	if e == nil {
		return nil, nil, errors.New("not a database")
	}
	vars := map[string]string{}
	lines, _ := ParseEnv(app.Env)
	for _, l := range lines {
		k, v, _ := strings.Cut(l, "=")
		vars[k] = v
	}
	for _, k := range []string{e.userVar, e.dbVar} {
		if k != "" && !safeIdent(vars[k]) {
			return nil, nil, fmt.Errorf("%s is missing or has unexpected characters", k)
		}
	}
	return e, vars, nil
}

func safeIdent(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// container returns the ID of the service's running container.
func (m *Manager) container(ctx context.Context, app *store.App) (string, error) {
	tasks, err := m.docker.ServiceTasks(ctx, app.ServiceName())
	if err != nil {
		return "", err
	}
	for _, t := range tasks {
		if t.DesiredState == "running" && t.Status.State == "running" && t.Status.ContainerStatus.ContainerID != "" {
			return t.Status.ContainerStatus.ContainerID, nil
		}
	}
	return "", errors.New("the database isn't running; deploy or start it first")
}

func (m *Manager) exec(ctx context.Context, app *store.App, cmd, env []string) (string, error) {
	return m.execMax(ctx, app, cmd, env, aiMaxOutput)
}

func (m *Manager) execMax(ctx context.Context, app *store.App, cmd, env []string, max int64) (string, error) {
	id, err := m.container(ctx, app)
	if err != nil {
		return "", err
	}
	out, stderr, code, err := m.docker.Exec(ctx, id, cmd, env, max)
	if err != nil {
		return "", err
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = fmt.Sprintf("exited with code %d", code)
		}
		return "", errors.New(cleanClientError(msg))
	}
	return out, nil
}

// cleanClientError drops noise such as mysql's command-line password warning.
func cleanClientError(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines { // MariaDB echoes the statement before the error
		if strings.HasPrefix(l, "ERROR") {
			lines = lines[i:]
			break
		}
	}
	var keep []string
	for _, l := range lines {
		if strings.Contains(l, "Using a password on the command line") {
			continue
		}
		keep = append(keep, l)
	}
	// Our queries start with session settings, so "at line 2" means the
	// user's first line: drop it rather than confuse.
	return strings.TrimSpace(atLineRe.ReplaceAllString(strings.Join(keep, "\n"), ""))
}

var atLineRe = regexp.MustCompile(` at line \d+`)

func mysqlClient(e *Engine) string {
	if e.Key == "mariadb" {
		return "mariadb" // MariaDB 11 images no longer ship a "mysql" alias
	}
	return "mysql"
}

// adminExec runs SQL as the database's administrator.
func (m *Manager) adminExec(ctx context.Context, app *store.App, sql string) (string, error) {
	e, vars, err := m.dbVars(app)
	if err != nil {
		return "", err
	}
	switch e.Key {
	case "mongo", "redis":
		return "", errNoAIAccess
	case "postgres":
		// Local socket connections are trusted in the official image.
		return m.exec(ctx, app, []string{"psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-tA",
			"-U", vars[e.userVar], "-d", vars[e.dbVar], "-c", sql}, nil)
	default:
		return m.exec(ctx, app, []string{mysqlClient(e), "--batch", "--skip-column-names", "-uroot", "-e", sql},
			[]string{"MYSQL_PWD=" + vars[e.rootVar]})
	}
}

// errNoAIAccess is returned for engines without AI read-only access.
var errNoAIAccess = errors.New("AI read-only access is only available for PostgreSQL, MySQL and MariaDB")

// EnableAIAccess creates (or resets) the read-only user with password.
func (m *Manager) EnableAIAccess(ctx context.Context, app *store.App, password string) error {
	e, _, err := m.dbVars(app)
	if err != nil {
		return err
	}
	if !e.AIAccess {
		return errNoAIAccess
	}
	return m.provisionSQLUser(ctx, app, aiUser, password, aiQueryTimeout, 3)
}

// provisionSQLUser creates (or resets) a SELECT-only user on a PostgreSQL,
// MySQL or MariaDB database. roleTimeout is the statement timeout stored on
// the PostgreSQL role (sessions may lower it).
func (m *Manager) provisionSQLUser(ctx context.Context, app *store.App, user, password string, roleTimeout time.Duration, maxConns int) error {
	if !safeIdent(password) || !safeIdent(user) {
		return errors.New("bad user or password")
	}
	e, vars, err := m.dbVars(app)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch e.Key {
	case "postgres":
		v, err := m.adminExec(ctx, app, "SHOW server_version_num")
		if err != nil {
			return err
		}
		if n, _ := strconv.Atoi(strings.TrimSpace(v)); n < minPostgresMajor*10000 {
			return fmt.Errorf("read-only access needs PostgreSQL %d or newer", minPostgresMajor)
		}
		_, err = m.adminExec(ctx, app, fmt.Sprintf(`DO $$BEGIN
IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%[1]s') THEN CREATE ROLE %[1]s LOGIN; END IF;
END$$;
ALTER ROLE %[1]s WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT CONNECTION LIMIT %[6]d PASSWORD '%[2]s';
ALTER ROLE %[1]s SET default_transaction_read_only = on;
ALTER ROLE %[1]s SET statement_timeout = '%[3]dms';
REVOKE pg_read_all_data FROM %[1]s;
GRANT CONNECT ON DATABASE "%[4]s" TO %[1]s;
REVOKE TEMPORARY ON DATABASE "%[4]s" FROM %[1]s;
DO $$DECLARE s text; BEGIN
FOR s IN SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg\_%%' AND nspname <> 'information_schema' LOOP
  EXECUTE format('GRANT USAGE ON SCHEMA %%I TO %[1]s', s);
  EXECUTE format('GRANT SELECT ON ALL TABLES IN SCHEMA %%I TO %[1]s', s);
  EXECUTE format('GRANT SELECT ON ALL SEQUENCES IN SCHEMA %%I TO %[1]s', s);
END LOOP;
END$$;
ALTER DEFAULT PRIVILEGES FOR ROLE "%[5]s" GRANT SELECT ON TABLES TO %[1]s;
ALTER DEFAULT PRIVILEGES FOR ROLE "%[5]s" GRANT SELECT ON SEQUENCES TO %[1]s;`, user, password, roleTimeout.Milliseconds(), vars[e.dbVar], vars[e.userVar], maxConns))
		return err
	case "mysql", "mariadb":
		_, err := m.adminExec(ctx, app, fmt.Sprintf(`CREATE USER IF NOT EXISTS '%[1]s'@'localhost' IDENTIFIED BY '%[2]s';
ALTER USER '%[1]s'@'localhost' IDENTIFIED BY '%[2]s' WITH MAX_USER_CONNECTIONS %[4]d;
REVOKE ALL PRIVILEGES, GRANT OPTION FROM '%[1]s'@'localhost';
GRANT SELECT, SHOW VIEW ON `+"`%[3]s`"+`.* TO '%[1]s'@'localhost';`, user, password, vars[e.dbVar], maxConns))
		return err
	}
	return errors.New(e.Label + " has no SQL user to create")
}

// DisableAIAccess removes the read-only user and ends its sessions.
func (m *Manager) DisableAIAccess(ctx context.Context, app *store.App) error {
	e, _, err := m.dbVars(app)
	if err != nil {
		return err
	}
	if !e.AIAccess {
		return nil // never turned on
	}
	return m.dropSQLUser(ctx, app, aiUser)
}

// dropSQLUser removes a read-only SQL user and ends its sessions.
func (m *Manager) dropSQLUser(ctx context.Context, app *store.App, user string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var err error
	switch app.Kind {
	case "postgres":
		_, err = m.adminExec(ctx, app, fmt.Sprintf(`DO $$BEGIN
IF EXISTS (SELECT FROM pg_roles WHERE rolname = '%[1]s') THEN
  PERFORM pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = '%[1]s';
  EXECUTE 'DROP OWNED BY %[1]s';
  EXECUTE 'DROP ROLE %[1]s';
END IF;
END$$;`, user))
	case "mysql", "mariadb":
		_, err = m.adminExec(ctx, app, fmt.Sprintf(`DROP USER IF EXISTS '%s'@'localhost';`, user))
	}
	return err
}

// wrapLimit caps the rows of a plain query by wrapping it in a subquery.
// Other statements (SHOW, EXPLAIN…) run as they are; their output is still
// capped when it is read back.
func wrapLimit(q string, limit int) string {
	head := strings.ToLower(strings.TrimLeft(q, " \t\r\n("))
	for _, kw := range []string{"select", "with", "values", "table"} {
		if strings.HasPrefix(head, kw) && len(head) > len(kw) && strings.ContainsRune(" \t\r\n(", rune(head[len(kw)])) {
			return fmt.Sprintf("SELECT * FROM (\n%s\n) AS sp_q LIMIT %d", q, limit)
		}
	}
	return q
}

// cleanQuery trims whitespace and trailing semicolons.
func cleanQuery(q string) (string, error) {
	q = strings.TrimSpace(q)
	for strings.HasSuffix(q, ";") {
		q = strings.TrimSpace(strings.TrimSuffix(q, ";"))
	}
	if q == "" {
		return "", errors.New("query is empty")
	}
	if len(q) > 20000 {
		return "", errors.New("query is too long")
	}
	if strings.ContainsRune(q, 0) {
		return "", errors.New("query contains a NUL byte")
	}
	return q, nil
}

// AIQuery runs q as the read-only user and returns at most AIMaxRows rows.
func (m *Manager) AIQuery(ctx context.Context, app *store.App, password, q string) (*QueryResult, error) {
	e, _, err := m.dbVars(app)
	if err != nil {
		return nil, err
	}
	if !e.AIAccess {
		return nil, errNoAIAccess
	}
	return m.sqlQuery(ctx, app, aiUser, password, q, aiQueryTimeout, AIMaxRows, false)
}

// sqlQuery runs q on a SQL database as a read-only user, in a read-only
// session with a statement timeout, and returns at most maxRows rows.
func (m *Manager) sqlQuery(ctx context.Context, app *store.App, user, password, q string, timeout time.Duration, maxRows int, explain bool) (*QueryResult, error) {
	q, err := cleanQuery(q)
	if err != nil {
		return nil, err
	}
	e, vars, err := m.dbVars(app)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
	defer cancel()
	sql := wrapLimit(q, maxRows+1)
	if explain {
		sql = explainSQL(q)
	}
	max := int64(aiMaxOutput)
	if user != aiUser {
		max = queryMaxOutput
	}
	switch e.Key {
	case "postgres":
		out, err := m.execMax(ctx, app, []string{"psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "--csv", "-P", "null=" + pgNullMarker,
			"-U", user, "-d", vars[e.dbVar], "-c", sql},
			[]string{"PGPASSWORD=" + password, fmt.Sprintf("PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=%d", timeout.Milliseconds())}, max)
		if err != nil {
			return nil, err
		}
		return parseCSVMax(out, maxRows)
	case "mysql", "mariadb":
		to := fmt.Sprintf("SET SESSION max_execution_time=%d;", timeout.Milliseconds())
		if e.Key == "mariadb" {
			to = fmt.Sprintf("SET SESSION max_statement_time=%d;", int(timeout.Seconds()))
		}
		out, err := m.execMax(ctx, app, []string{mysqlClient(e), "--batch", "--default-character-set=utf8mb4",
			"-u", user, "-D", vars[e.dbVar], "-e", "SET SESSION TRANSACTION READ ONLY; " + to + "\n" + sql},
			[]string{"MYSQL_PWD=" + password}, max)
		if err != nil {
			return nil, err
		}
		return parseTSVMax(out, maxRows), nil
	}
	return nil, errors.New(e.Label + " doesn't run SQL")
}

// explainSQL asks the database for q's plan instead of its rows.
func explainSQL(q string) string {
	head := strings.ToLower(strings.TrimSpace(q))
	if strings.HasPrefix(head, "explain") || strings.HasPrefix(head, "describe") {
		return q
	}
	return "EXPLAIN " + q
}

func capRows(r *QueryResult, max int) *QueryResult {
	if len(r.Rows) > max {
		r.Rows = r.Rows[:max]
		r.Truncated = true
	}
	r.RowCount = len(r.Rows)
	return r
}

func parseCSV(out string) (*QueryResult, error) { return parseCSVMax(out, AIMaxRows) }

func parseCSVMax(out string, max int) (*QueryResult, error) {
	r := csv.NewReader(strings.NewReader(out))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	recs, err := r.ReadAll()
	if err != nil && len(recs) == 0 {
		if strings.TrimSpace(out) == "" {
			return &QueryResult{Columns: []string{}, Rows: [][]any{}}, nil
		}
		return nil, fmt.Errorf("couldn't read the result: %v", err)
	}
	res := &QueryResult{Columns: []string{}, Rows: [][]any{}, Truncated: err != nil}
	if len(recs) > 0 {
		res.Columns = recs[0]
		for _, rec := range recs[1:] {
			row := make([]any, len(rec))
			for i, v := range rec {
				if v == pgNullMarker {
					row[i] = nil
				} else {
					row[i] = v
				}
			}
			res.Rows = append(res.Rows, row)
		}
	}
	return capRows(res, max), nil
}

// parseTSV reads mysql --batch output: tab-separated with a header line,
// NULL for nulls, and \t \n \\ \0 escapes.
func parseTSV(out string) *QueryResult { return parseTSVMax(out, AIMaxRows) }

func parseTSVMax(out string, max int) *QueryResult {
	res := &QueryResult{Columns: []string{}, Rows: [][]any{}}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return capRows(res, max)
	}
	unescape := strings.NewReplacer(`\t`, "\t", `\n`, "\n", `\0`, "\x00", `\\`, `\`)
	for i, l := range lines {
		fields := strings.Split(l, "\t")
		if i == 0 {
			for _, f := range fields {
				res.Columns = append(res.Columns, unescape.Replace(f))
			}
			continue
		}
		row := make([]any, len(fields))
		for j, f := range fields {
			if f == "NULL" {
				row[j] = nil
			} else {
				row[j] = unescape.Replace(f)
			}
		}
		res.Rows = append(res.Rows, row)
	}
	return capRows(res, max)
}

// sqlString quotes s as an SQL string literal. MySQL also treats
// backslashes as escapes, PostgreSQL (standard_conforming_strings) doesn't.
func sqlString(s string, mysql bool) string {
	if mysql {
		s = strings.ReplaceAll(s, `\`, `\\`)
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// AIListTables lists tables and views with approximate row counts.
func (m *Manager) AIListTables(ctx context.Context, app *store.App, password string) (*QueryResult, error) {
	return m.AIQuery(ctx, app, password, listTablesSQL(app.Kind))
}

func listTablesSQL(kind string) string {
	if kind == "postgres" {
		return `SELECT t.table_schema AS schema, t.table_name AS name, t.table_type AS type,
  c.reltuples::bigint AS approx_rows
FROM information_schema.tables t
LEFT JOIN pg_namespace n ON n.nspname = t.table_schema
LEFT JOIN pg_class c ON c.relname = t.table_name AND c.relnamespace = n.oid
WHERE t.table_schema NOT IN ('pg_catalog', 'information_schema') AND t.table_schema NOT LIKE 'pg_toast%'
ORDER BY 1, 2`
	}
	return `SELECT table_name AS name, table_type AS type, table_rows AS approx_rows
FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY 1`
}

// AIDescribeTable lists a table's columns. table may be "schema.table" on
// PostgreSQL; the default schema is public.
func (m *Manager) AIDescribeTable(ctx context.Context, app *store.App, password, table string) (*QueryResult, error) {
	q, err := describeTableSQL(app.Kind, table)
	if err != nil {
		return nil, err
	}
	return m.AIQuery(ctx, app, password, q)
}

func describeTableSQL(kind, table string) (string, error) {
	table = strings.TrimSpace(table)
	if table == "" || len(table) > 200 {
		return "", errors.New("table is required")
	}
	if kind == "postgres" {
		schema, name, ok := strings.Cut(table, ".")
		if !ok {
			schema, name = "public", table
		}
		return fmt.Sprintf(`SELECT column_name AS column, data_type AS type, is_nullable AS nullable, column_default AS "default"
FROM information_schema.columns WHERE table_schema = %s AND table_name = %s ORDER BY ordinal_position`,
			sqlString(schema, false), sqlString(name, false)), nil
	}
	return fmt.Sprintf(`SELECT column_name AS `+"`column`"+`, column_type AS type, is_nullable AS nullable, column_default AS `+"`default`"+`, column_key AS `+"`key`"+`
FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = %s ORDER BY ordinal_position`, sqlString(table, true)), nil
}
