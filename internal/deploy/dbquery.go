package deploy

// The database Query tab: a built-in, read-only query tool.
//
// Queries run as a dedicated read-only user (queryUser) that SimplePanel
// creates in each database the first time someone opens its Query tab, so
// the database itself refuses writes; the panel never relies on spotting
// risky statements. Like AI read-only access, every query runs through the
// database's own client inside its container, so no port is exposed and the
// panel needs no drivers.
//
//   - PostgreSQL, MySQL, MariaDB: SELECT (and SHOW VIEW) on the app's data
//     only, read-only sessions, a statement timeout per query.
//   - MongoDB: the built-in readAnyDatabase role (no access to users or
//     password hashes). Queries are parsed by the panel into a fixed shape
//     (collection, method, JSON arguments) and passed to a fixed mongosh
//     script as data, so no user-written JavaScript ever runs.
//   - Redis: an ACL user limited to read commands, minus anything flagged
//     dangerous (KEYS, DEBUG, CONFIG…). Redis keeps ACLs in memory, so the
//     user is created again after a restart.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

const (
	queryUser = "sp_query_ro"
	// Limits a request may ask for; the UI defaults are 30 s and 1,000 rows.
	QueryDefaultTimeout = 30 * time.Second
	QueryMaxTimeout     = 120 * time.Second
	QueryDefaultRows    = 1000
	QueryMaxRows        = 10000
	queryMaxOutput      = 16 << 20
	queryMaxConns       = 5
)

// QueryOptions tune one query.
type QueryOptions struct {
	Timeout  time.Duration
	MaxRows  int
	Explain  bool
	Database string // MongoDB: the database to run in
}

// Normalize fills in defaults and clamps the limits.
func (o *QueryOptions) Normalize() {
	if o.Timeout <= 0 {
		o.Timeout = QueryDefaultTimeout
	}
	o.Timeout = min(max(o.Timeout, time.Second), QueryMaxTimeout)
	if o.MaxRows <= 0 {
		o.MaxRows = QueryDefaultRows
	}
	o.MaxRows = min(o.MaxRows, QueryMaxRows)
}

// QuerySupported reports whether engine kind has the Query tab.
func QuerySupported(kind string) bool {
	switch kind {
	case "postgres", "mysql", "mariadb", "mongo", "redis":
		return true
	}
	return false
}

// QueryLanguage names what the query box takes for kind.
func QueryLanguage(kind string) string {
	switch kind {
	case "mongo":
		return "mongo"
	case "redis":
		return "redis"
	}
	return "sql"
}

// ErrQueryAuth means the read-only user is missing or has another password
// (for example after Redis restarted or a backup was restored); the caller
// should create it again and retry once.
var ErrQueryAuth = errors.New("the read-only database user needs to be set up again")

func isAuthFailure(msg string) bool {
	m := strings.ToLower(msg)
	for _, s := range []string{
		"password authentication failed", // postgres
		"role \"" + queryUser + "\" does not exist",
		"access denied for user '" + queryUser + "'", // mysql/mariadb
		"authentication failed",                      // mongo
		"wrongpass", "noauth",                        // redis
	} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

func wrapAuth(err error) error {
	if err != nil && isAuthFailure(err.Error()) {
		return ErrQueryAuth
	}
	return err
}

// EnableQueryUser creates (or resets) the Query tab's read-only user.
func (m *Manager) EnableQueryUser(ctx context.Context, app *store.App, password string) error {
	if !QuerySupported(app.Kind) {
		return errors.New("this service isn't a database")
	}
	if !safeIdent(password) {
		return errors.New("bad password")
	}
	switch app.Kind {
	case "mongo":
		_, err := m.mongoAdmin(ctx, app, `const a = spConnect(process.env.SP_ADMIN_USER, process.env.SP_ADMIN_PW).getSiblingDB('admin');
const roles = [{ role: 'readAnyDatabase', db: 'admin' }];
if (a.getUser('`+queryUser+`')) a.updateUser('`+queryUser+`', { pwd: process.env.SP_RO_PW, roles });
else a.createUser({ user: '`+queryUser+`', pwd: process.env.SP_RO_PW, roles });
print('ok');`, password)
		return err
	case "redis":
		return m.redisSetUser(ctx, app, password)
	}
	return m.provisionSQLUser(ctx, app, queryUser, password, QueryMaxTimeout, queryMaxConns)
}

// DisableQueryUser removes the Query tab's read-only user (best effort).
func (m *Manager) DisableQueryUser(ctx context.Context, app *store.App) error {
	switch app.Kind {
	case "mongo":
		_, err := m.mongoAdmin(ctx, app, `const a = spConnect(process.env.SP_ADMIN_USER, process.env.SP_ADMIN_PW).getSiblingDB('admin');
if (a.getUser('`+queryUser+`')) a.dropUser('`+queryUser+`');`, "")
		return err
	case "redis":
		_, err := m.redisAdmin(ctx, app, "ACL", "DELUSER", queryUser)
		return err
	case "postgres", "mysql", "mariadb":
		return m.dropSQLUser(ctx, app, queryUser)
	}
	return nil
}

// Query runs q as the read-only user.
func (m *Manager) Query(ctx context.Context, app *store.App, password, q string, opt QueryOptions) (*QueryResult, error) {
	opt.Normalize()
	switch app.Kind {
	case "mongo":
		mq, err := ParseMongoQuery(q)
		if err != nil {
			return nil, err
		}
		r, err := m.mongoQuery(ctx, app, password, mq, opt)
		return r, wrapAuth(err)
	case "redis":
		if opt.Explain {
			return nil, errors.New("Redis has no query plans")
		}
		args, err := SplitRedisCommand(q)
		if err != nil {
			return nil, err
		}
		r, err := m.redisQuery(ctx, app, password, args, opt)
		return r, wrapAuth(err)
	case "postgres", "mysql", "mariadb":
		r, err := m.sqlQuery(ctx, app, queryUser, password, q, opt.Timeout, opt.MaxRows, opt.Explain)
		return r, wrapAuth(err)
	}
	return nil, errors.New("this service isn't a database")
}

// QueryTables lists what the table browser shows: tables and views, Mongo
// collections (schema = database) or Redis keys (up to opt.MaxRows,
// filtered by pattern).
func (m *Manager) QueryTables(ctx context.Context, app *store.App, password, pattern string) (*QueryResult, error) {
	opt := QueryOptions{Timeout: 20 * time.Second, MaxRows: QueryMaxRows}
	opt.Normalize()
	switch app.Kind {
	case "mongo":
		r, err := m.mongoRun(ctx, app, password, mongoRequest{Op: "tables", MaxTimeMS: opt.Timeout.Milliseconds(), Limit: opt.MaxRows}, opt)
		return r, wrapAuth(err)
	case "redis":
		if pattern == "" {
			pattern = "*"
		}
		if len(pattern) > 200 {
			return nil, errors.New("pattern is too long")
		}
		r, err := m.redisScan(ctx, app, password, pattern, 1000)
		return r, wrapAuth(err)
	case "postgres", "mysql", "mariadb":
		r, err := m.sqlQuery(ctx, app, queryUser, password, listTablesSQL(app.Kind), opt.Timeout, opt.MaxRows, false)
		return r, wrapAuth(err)
	}
	return nil, errors.New("this service isn't a database")
}

// QueryColumns describes a SQL table's columns.
func (m *Manager) QueryColumns(ctx context.Context, app *store.App, password, table string) (*QueryResult, error) {
	q, err := describeTableSQL(app.Kind, table)
	if err != nil {
		return nil, err
	}
	switch app.Kind {
	case "postgres", "mysql", "mariadb":
		r, err := m.sqlQuery(ctx, app, queryUser, password, q, 20*time.Second, 2000, false)
		return r, wrapAuth(err)
	}
	return nil, errors.New("only SQL databases have columns to describe")
}

// ---- MongoDB ----

// mongoPrelude defines spConnect, which signs in to the local server.
const mongoPrelude = `function spConnect(u, p) {
  return connect('mongodb://' + encodeURIComponent(u) + ':' + encodeURIComponent(p) + '@127.0.0.1:27017/admin?authSource=admin&directConnection=true&serverSelectionTimeoutMS=5000');
}
`

func (m *Manager) mongoAdmin(ctx context.Context, app *store.App, script, roPassword string) (string, error) {
	e, vars, err := m.dbVars(app)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return m.exec(ctx, app, []string{"mongosh", "--quiet", "--norc", "--nodb", "--eval", mongoPrelude + script},
		[]string{"SP_ADMIN_USER=" + vars[e.userVar], "SP_ADMIN_PW=" + vars[e.passVar], "SP_RO_PW=" + roPassword})
}

// mongoRequest is the data handed to the fixed query script.
type mongoRequest struct {
	Op         string            `json:"op"` // tables | find | findOne | aggregate | countDocuments | estimatedDocumentCount | distinct
	DB         string            `json:"db,omitempty"`
	Collection string            `json:"coll,omitempty"`
	Args       []json.RawMessage `json:"args,omitempty"`
	Sort       json.RawMessage   `json:"sort,omitempty"`
	Projection json.RawMessage   `json:"projection,omitempty"`
	Skip       int64             `json:"skip,omitempty"`
	Limit      int               `json:"limit"`
	UserLimit  int               `json:"userLimit,omitempty"`
	Explain    bool              `json:"explain,omitempty"`
	MaxTimeMS  int64             `json:"maxTimeMS"`
}

// mongoScript runs one mongoRequest as the read-only user and prints the
// result as relaxed Extended JSON. It never evaluates request text as code:
// EJSON.parse only builds values.
const mongoScript = mongoPrelude + `const req = EJSON.parse(process.env.SP_REQ, { relaxed: true });
const conn = spConnect('` + queryUser + `', process.env.SP_PW);
const out = (v) => print(EJSON.stringify(v, { relaxed: true }));
if (req.op === 'tables') {
  const rows = [];
  const dbs = conn.getSiblingDB('admin').adminCommand({ listDatabases: 1, nameOnly: true }).databases;
  for (const d of dbs) {
    if (['admin', 'config', 'local'].includes(d.name)) continue;
    const sdb = conn.getSiblingDB(d.name);
    for (const c of sdb.getCollectionInfos({}, { nameOnly: false })) {
      if (rows.length >= req.limit) break;
      let n = null;
      if (c.type === 'collection') { try { n = sdb.getCollection(c.name).estimatedDocumentCount(); } catch (e) {} }
      rows.push({ schema: d.name, name: c.name, type: c.type, approx_rows: n });
    }
  }
  out({ docs: rows });
} else {
  const c = conn.getSiblingDB(req.db).getCollection(req.coll);
  const a = req.args || [];
  const opts = { maxTimeMS: req.maxTimeMS };
  const cap = req.userLimit && req.userLimit < req.limit ? req.userLimit : req.limit;
  if (req.op === 'find' || req.op === 'findOne') {
    let cur = c.find(a[0] || {}, req.projection || a[1] || undefined).maxTimeMS(req.maxTimeMS);
    if (req.sort) cur = cur.sort(req.sort);
    if (req.skip) cur = cur.skip(req.skip);
    cur = cur.limit(req.op === 'findOne' ? 1 : cap + 1);
    if (req.explain) out({ value: cur.explain('queryPlanner') });
    else out({ docs: cur.toArray() });
  } else if (req.op === 'aggregate') {
    const p = (Array.isArray(a[0]) ? a[0] : a).concat([{ $limit: cap + 1 }]);
    if (req.explain) out({ value: c.explain('queryPlanner').aggregate(p, opts) });
    else out({ docs: c.aggregate(p, opts).toArray() });
  } else if (req.op === 'countDocuments') {
    out({ docs: [{ count: c.countDocuments(a[0] || {}, opts) }] });
  } else if (req.op === 'estimatedDocumentCount') {
    out({ docs: [{ count: c.estimatedDocumentCount(opts) }] });
  } else if (req.op === 'distinct') {
    out({ docs: c.distinct(a[0], a[1] || {}, opts).slice(0, cap + 1).map((v) => ({ value: v })) });
  } else {
    throw new Error('unsupported method ' + req.op);
  }
}`

func (m *Manager) mongoQuery(ctx context.Context, app *store.App, password string, q *MongoQuery, opt QueryOptions) (*QueryResult, error) {
	db := q.DB
	if db == "" {
		db = opt.Database
	}
	if db == "" {
		_, vars, _ := m.dbVars(app)
		db = vars["MONGO_INITDB_DATABASE"]
	}
	if db == "" {
		db = "test"
	}
	if !mongoNameOK(db) {
		return nil, errors.New("bad database name")
	}
	req := mongoRequest{Op: q.Method, DB: db, Collection: q.Collection, Args: q.Args, Sort: q.Sort, Projection: q.Projection,
		Skip: q.Skip, Limit: opt.MaxRows, UserLimit: q.Limit, Explain: opt.Explain, MaxTimeMS: opt.Timeout.Milliseconds()}
	if opt.Explain && req.Op != "find" && req.Op != "findOne" && req.Op != "aggregate" {
		return nil, errors.New("EXPLAIN works with find and aggregate")
	}
	return m.mongoRun(ctx, app, password, req, opt)
}

func mongoNameOK(s string) bool {
	return s != "" && len(s) <= 64 && !strings.ContainsAny(s, "/\\. \"$*<>:|?\x00")
}

func (m *Manager) mongoRun(ctx context.Context, app *store.App, password string, req mongoRequest, opt QueryOptions) (*QueryResult, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, opt.Timeout+15*time.Second)
	defer cancel()
	out, err := m.execMax(ctx, app, []string{"mongosh", "--quiet", "--norc", "--nodb", "--eval", mongoScript},
		[]string{"SP_PW=" + password, "SP_REQ=" + string(b)}, queryMaxOutput)
	if err != nil {
		return nil, errors.New(cleanMongoError(err.Error()))
	}
	return parseMongoOutput(out, opt.MaxRows)
}

// cleanMongoError keeps the useful line of a mongosh error.
func cleanMongoError(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if i := strings.Index(l, "Error: "); i >= 0 && !strings.HasPrefix(l, "at ") {
			return strings.TrimSpace(l[i+len("Error: "):])
		}
		if strings.HasPrefix(l, "Uncaught") {
			return strings.TrimSpace(strings.TrimPrefix(l, "Uncaught"))
		}
	}
	return s
}

// parseMongoOutput turns {"docs":[…]} into columns (top-level fields in
// order of first appearance) and rows, or {"value":…} into one cell.
func parseMongoOutput(out string, maxRows int) (*QueryResult, error) {
	out = strings.TrimSpace(out)
	if i := strings.LastIndex(out, "\n{"); i >= 0 { // ignore any warnings before the result
		out = out[i+1:]
	}
	var top struct {
		Docs  []json.RawMessage `json:"docs"`
		Value json.RawMessage   `json:"value"`
	}
	if err := json.Unmarshal([]byte(out), &top); err != nil {
		return nil, fmt.Errorf("couldn't read the result: %v", err)
	}
	res := &QueryResult{Columns: []string{}, Rows: [][]any{}}
	if top.Docs == nil {
		res.Columns = []string{"result"}
		res.Rows = [][]any{{cellValue(top.Value)}}
		return capRows(res, maxRows), nil
	}
	idx := map[string]int{}
	var docs [][][2]any
	for _, d := range top.Docs {
		fields, err := orderedFields(d)
		if err != nil { // not an object (aggregate can return anything)
			fields = [][2]any{{"value", d}}
		}
		for _, f := range fields {
			k := f[0].(string)
			if _, ok := idx[k]; !ok {
				idx[k] = len(res.Columns)
				res.Columns = append(res.Columns, k)
			}
		}
		docs = append(docs, fields)
	}
	for _, fields := range docs {
		row := make([]any, len(res.Columns))
		for i := range row {
			row[i] = missingField
		}
		for _, f := range fields {
			row[idx[f[0].(string)]] = cellValue(f[1].(json.RawMessage))
		}
		res.Rows = append(res.Rows, row)
	}
	return capRows(res, maxRows), nil
}

// missingField marks a field a document doesn't have (shown blank, unlike
// null).
var missingField any = nil

// orderedFields reads a JSON object's fields in order.
func orderedFields(raw json.RawMessage) ([][2]any, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return nil, errors.New("not an object")
	}
	var out [][2]any
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, [2]any{k.(string), v})
	}
	return out, nil
}

// cellValue shows scalars as they are, and ObjectIds and dates as their
// plain value; other objects and arrays stay JSON text.
func cellValue(raw json.RawMessage) any {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil
	}
	switch s[0] {
	case '"':
		var v string
		_ = json.Unmarshal(raw, &v)
		return v
	case '{':
		fields, err := orderedFields(raw)
		if err == nil && len(fields) == 1 {
			k := fields[0][0].(string)
			v := fields[0][1].(json.RawMessage)
			switch k {
			case "$oid", "$numberDecimal", "$numberLong":
				var x string
				if json.Unmarshal(v, &x) == nil {
					return x
				}
			case "$date":
				var x string
				if json.Unmarshal(v, &x) == nil {
					return x
				}
				return strings.TrimSpace(string(v))
			}
		}
		return compactJSON(raw)
	case '[':
		return compactJSON(raw)
	}
	return s // numbers and booleans, as text so large numbers stay exact
}

func compactJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

// ---- Redis ----

func (m *Manager) redisAdmin(ctx context.Context, app *store.App, args ...string) (string, error) {
	e, vars, err := m.dbVars(app)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := m.exec(ctx, app, append([]string{"redis-cli", "--no-auth-warning"}, args...), []string{"REDISCLI_AUTH=" + vars[e.passVar]})
	if err == nil {
		if msg, isErr := redisReplyError(out); isErr {
			return "", errors.New(msg)
		}
	}
	return out, err
}

// redisACL is the read-only user's rule set: every key, no pub/sub
// channels, read commands except dangerous ones.
var redisACL = []string{"reset", "on", "~*", "resetchannels", "-@all", "+@read", "-@dangerous", "+ping", "+type", "+ttl", "+pttl"}

func (m *Manager) redisSetUser(ctx context.Context, app *store.App, password string) error {
	args := append([]string{"ACL", "SETUSER", queryUser}, redisACL...)
	args = append(args, ">"+password)
	_, err := m.redisAdmin(ctx, app, args...)
	return err
}

// redisReplyError spots an error reply in redis-cli's non-interactive
// output, which exits 0 on errors.
func redisReplyError(out string) (string, bool) {
	s := strings.TrimSpace(out)
	if rest, ok := strings.CutPrefix(s, "error:"); ok { // --json
		var msg string
		if json.Unmarshal([]byte(rest), &msg) == nil {
			return msg, true
		}
		return strings.Trim(rest, `"`), true
	}
	for _, p := range []string{"(error) ", "ERR ", "WRONGPASS", "NOPERM", "NOAUTH", "WRONGTYPE"} {
		if strings.HasPrefix(s, p) {
			return strings.TrimPrefix(s, "(error) "), true
		}
	}
	return "", false
}

func (m *Manager) redisCLI(ctx context.Context, app *store.App, password string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
	defer cancel()
	cmd := append([]string{"redis-cli", "--no-auth-warning", "--user", queryUser, "--json"}, args...)
	out, err := m.execMax(ctx, app, cmd, []string{"REDISCLI_AUTH=" + password}, queryMaxOutput)
	if err != nil {
		return "", err
	}
	if msg, isErr := redisReplyError(out); isErr {
		return "", errors.New(msg)
	}
	return out, nil
}

func (m *Manager) redisQuery(ctx context.Context, app *store.App, password string, args []string, opt QueryOptions) (*QueryResult, error) {
	out, err := m.redisCLI(ctx, app, password, opt.Timeout, args...)
	if err != nil {
		return nil, err
	}
	return parseRedisReply(strings.ToUpper(args[0]), args, out, opt.MaxRows)
}

func (m *Manager) redisScan(ctx context.Context, app *store.App, password, pattern string, limit int) (*QueryResult, error) {
	res := &QueryResult{Columns: []string{"name"}, Rows: [][]any{}}
	cursor := "0"
	for range 50 { // SCAN may return few keys per call
		out, err := m.redisCLI(ctx, app, password, 10*time.Second, "SCAN", cursor, "MATCH", pattern, "COUNT", "1000")
		if err != nil {
			return nil, err
		}
		var reply []json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &reply); err != nil || len(reply) != 2 {
			return nil, fmt.Errorf("couldn't read the SCAN reply")
		}
		var keys []string
		_ = json.Unmarshal(reply[0], &cursor)
		_ = json.Unmarshal(reply[1], &keys)
		for _, k := range keys {
			res.Rows = append(res.Rows, []any{k})
		}
		if cursor == "0" || len(res.Rows) > limit {
			break
		}
	}
	return capRows(res, limit), nil
}

// parseRedisReply shows a reply as a table: a single value as one cell,
// a list as numbered rows, and field/value or member/score pairs side by
// side.
func parseRedisReply(cmd string, args []string, out string, maxRows int) (*QueryResult, error) {
	out = strings.TrimSpace(out)
	var v any
	dec := json.NewDecoder(strings.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		// Not JSON: show the raw text.
		return capRows(&QueryResult{Columns: []string{"value"}, Rows: [][]any{{out}}}, maxRows), nil
	}
	res := &QueryResult{Columns: []string{}, Rows: [][]any{}}
	pairs := map[string][2]string{
		"HGETALL": {"field", "value"}, "CONFIG": {"name", "value"},
	}
	if (cmd == "ZRANGE" || cmd == "ZREVRANGE" || cmd == "ZRANGEBYSCORE" || cmd == "ZREVRANGEBYSCORE") && hasArg(args, "WITHSCORES") {
		pairs[cmd] = [2]string{"member", "score"}
	}
	if cmd == "HRANDFIELD" && hasArg(args, "WITHVALUES") {
		pairs[cmd] = [2]string{"field", "value"}
	}
	switch x := v.(type) {
	case []any:
		if p, ok := pairs[cmd]; ok && allPairs(x) { // RESP3 nests pairs
			res.Columns = []string{p[0], p[1]}
			for _, it := range x {
				pr := it.([]any)
				res.Rows = append(res.Rows, []any{redisCell(pr[0]), redisCell(pr[1])})
			}
		} else if p, ok := pairs[cmd]; ok && len(x)%2 == 0 {
			res.Columns = []string{p[0], p[1]}
			for i := 0; i+1 < len(x); i += 2 {
				res.Rows = append(res.Rows, []any{redisCell(x[i]), redisCell(x[i+1])})
			}
		} else {
			res.Columns = []string{"#", "value"}
			for i, it := range x {
				res.Rows = append(res.Rows, []any{fmt.Sprint(i + 1), redisCell(it)})
			}
		}
	case map[string]any: // RESP3 maps
		res.Columns = []string{"field", "value"}
		for k, it := range x {
			res.Rows = append(res.Rows, []any{k, redisCell(it)})
		}
	default:
		res.Columns = []string{"value"}
		res.Rows = [][]any{{redisCell(x)}}
	}
	return capRows(res, maxRows), nil
}

func allPairs(x []any) bool {
	for _, it := range x {
		if p, ok := it.([]any); !ok || len(p) != 2 {
			return false
		}
	}
	return len(x) > 0
}

func hasArg(args []string, want string) bool {
	for _, a := range args[1:] {
		if strings.EqualFold(a, want) {
			return true
		}
	}
	return false
}

func redisCell(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return fmt.Sprint(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// SplitRedisCommand splits a command line into arguments the way
// redis-cli does: spaces separate, and "double" or 'single' quotes group
// (double quotes understand \n, \t, \\ and \").
func SplitRedisCommand(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("query is empty")
	}
	if len(s) > 20000 || strings.ContainsRune(s, 0) {
		return nil, errors.New("query is too long or has a NUL byte")
	}
	if strings.Contains(s, "\n") {
		return nil, errors.New("run one Redis command at a time")
	}
	var args []string
	var cur strings.Builder
	inArg := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		case c == '"' || c == '\'':
			q := c
			inArg = true
			i++
			for ; i < len(s) && s[i] != q; i++ {
				if q == '"' && s[i] == '\\' && i+1 < len(s) {
					i++
					switch s[i] {
					case 'n':
						cur.WriteByte('\n')
					case 't':
						cur.WriteByte('\t')
					case 'r':
						cur.WriteByte('\r')
					default:
						cur.WriteByte(s[i])
					}
					continue
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, errors.New("unclosed quote")
			}
		default:
			inArg = true
			cur.WriteByte(c)
		}
	}
	if inArg {
		args = append(args, cur.String())
	}
	if len(args) == 0 {
		return nil, errors.New("query is empty")
	}
	if len(args) > 1000 {
		return nil, errors.New("too many arguments")
	}
	return args, nil
}
