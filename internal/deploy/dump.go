package deploy

// Logical database dumps and volume archives for backups. Data is streamed
// through the database's own client tools (via docker exec) or through a
// short-lived helper container, so the panel needs no database drivers and
// nothing is buffered in memory.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/docker"
	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// DefaultHelperImage runs volume backups and restores.
const DefaultHelperImage = "alpine:3.22"

// BackupKind is what a backup of app contains: the engine name for
// databases, "volumes" for apps with volumes, or "" if there's nothing to
// back up.
func BackupKind(app *store.App) string {
	if app.IsDatabase() {
		return app.Kind
	}
	if len(app.Volumes) > 0 {
		return "volumes"
	}
	return ""
}

// tailWriter keeps the last part of a command's stderr for error messages.
type tailWriter struct{ b []byte }

func (t *tailWriter) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 8<<10 {
		t.b = t.b[len(t.b)-8<<10:]
	}
	return len(p), nil
}

func (t *tailWriter) String() string { return strings.TrimSpace(cleanClientError(string(t.b))) }

func dumpClient(e *Engine) string {
	if e.Key == "mariadb" {
		return "mariadb-dump"
	}
	return "mysqldump"
}

// DumpDatabase writes a plain SQL dump of the database to w. The dump drops
// and recreates objects, so it can be restored over existing data or into
// an empty database, and leaves out owners and grants so it restores into a
// database with a different user.
func (m *Manager) DumpDatabase(ctx context.Context, app *store.App, w io.Writer) error {
	e, vars, err := m.dbVars(app)
	if err != nil {
		return err
	}
	id, err := m.container(ctx, app)
	if err != nil {
		return err
	}
	var cmd, env []string
	switch e.Key {
	case "postgres":
		cmd = []string{"pg_dump", "-U", vars[e.userVar], "-d", vars[e.dbVar], "--clean", "--if-exists", "--no-owner", "--no-privileges"}
	case "mongo":
		// Every database is dumped; admin (users) and config are skipped on
		// restore, so a restore never changes who can log in.
		cmd, env = mongoTool(e, vars, "mongodump --archive --quiet")
	case "redis":
		// redis-cli writes trailing sync markers when the target is stdout,
		// so the snapshot goes through a file in the container.
		cmd = []string{"sh", "-c", `f=$(mktemp) || exit 1
redis-cli --no-auth-warning --rdb "$f" >/dev/null
rc=$?
[ "$rc" -eq 0 ] && cat "$f"
rc2=$?
rm -f "$f"
[ "$rc" -eq 0 ] && exit "$rc2"
exit "$rc"`}
		env = []string{"REDISCLI_AUTH=" + vars[e.passVar]}
	default:
		cmd = []string{dumpClient(e), "-uroot", "--single-transaction", "--quick", "--routines", "--triggers", "--events",
			"--no-tablespaces", "--hex-blob", "--default-character-set=utf8mb4"}
		if e.Key == "mysql" {
			cmd = append(cmd, "--set-gtid-purged=OFF")
		}
		cmd = append(cmd, vars[e.dbVar])
		env = []string{"MYSQL_PWD=" + vars[e.rootVar]}
	}
	var stderr tailWriter
	code, err := m.docker.ExecStream(ctx, id, cmd, env, nil, w, &stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s exited with code %d: %s", cmd[0], code, stderr.String())
	}
	return nil
}

// mongoTool returns a shell command that runs a MongoDB tool (with its
// arguments) as the administrator. The password is passed through the
// environment rather than the exec command line.
func mongoTool(e *Engine, vars map[string]string, tool string) (cmd, env []string) {
	return []string{"sh", "-c", `exec ` + tool + ` --username "$SP_USER" --password "$SP_PASSWORD" --authenticationDatabase admin`},
		[]string{"SP_USER=" + vars[e.userVar], "SP_PASSWORD=" + vars[e.passVar]}
}

// RestoreDatabase runs the SQL from r against the database in a single
// transaction (PostgreSQL) or as one session (MySQL/MariaDB), or loads a
// mongodump archive (MongoDB). Redis uses RestoreRedis.
func (m *Manager) RestoreDatabase(ctx context.Context, app *store.App, r io.Reader, log io.Writer) error {
	e, vars, err := m.dbVars(app)
	if err != nil {
		return err
	}
	id, err := m.container(ctx, app)
	if err != nil {
		return err
	}
	var cmd, env []string
	switch e.Key {
	case "postgres":
		cmd = []string{"psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "--single-transaction", "-U", vars[e.userVar], "-d", vars[e.dbVar]}
		env = []string{"PGOPTIONS=-c client_min_messages=warning"}
	case "mongo":
		// Collections in the backup are dropped and replaced. Users and
		// roles (admin) and cluster metadata (config, local) are left alone,
		// so the target keeps its own credentials.
		cmd, env = mongoTool(e, vars, "mongorestore --archive --drop --quiet --nsExclude='admin.*' --nsExclude='config.*' --nsExclude='local.*'")
	case "redis":
		return errors.New("Redis backups are restored with RestoreRedis")
	default:
		cmd = []string{mysqlClient(e), "-uroot", "--default-character-set=utf8mb4", vars[e.dbVar]}
		env = []string{"MYSQL_PWD=" + vars[e.rootVar]}
	}
	var stderr tailWriter
	out := io.MultiWriter(&stderr, log)
	code, err := m.docker.ExecStream(ctx, id, cmd, env, r, io.Discard, out)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s exited with code %d: %s", cmd[0], code, stderr.String())
	}
	return nil
}

// WaitDatabaseReady waits until the database accepts connections.
func (m *Manager) WaitDatabaseReady(ctx context.Context, app *store.App, timeout time.Duration) error {
	e, vars, err := m.dbVars(app)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		switch e.Key {
		case "postgres":
			_, last = m.exec(ctx, app, []string{"psql", "-X", "-q", "-tA", "-U", vars[e.userVar], "-d", vars[e.dbVar], "-c", "SELECT 1"}, nil)
		case "mongo":
			// The image first runs a temporary server to create the user; wait
			// until it has been replaced by the real one.
			cmd, env := mongoTool(e, vars, `mongosh --quiet --norc --eval 'db.runCommand({ping: 1}).ok'`)
			cmd[2] = `test ! -e "${TMPDIR:-/tmp}/docker-entrypoint-temp-mongod.pid" || { echo "still initializing" >&2; exit 1; }; ` + cmd[2]
			_, last = m.exec(ctx, app, cmd, env)
		case "redis":
			var out string
			out, last = m.exec(ctx, app, []string{"redis-cli", "--no-auth-warning", "ping"}, []string{"REDISCLI_AUTH=" + vars[e.passVar]})
			if last == nil && strings.TrimSpace(out) != "PONG" {
				last = errors.New(strings.TrimSpace(out))
			}
		default:
			_, last = m.exec(ctx, app, []string{mysqlClient(e), "-uroot", "-e", "SELECT 1", vars[e.dbVar]}, []string{"MYSQL_PWD=" + vars[e.rootVar]})
		}
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return fmt.Errorf("the database didn't become ready: %v", last)
}

func (m *Manager) helperImage(ctx context.Context) (string, error) {
	img := orDefault(m.cfg.HelperImage, DefaultHelperImage)
	if m.docker.ImageExists(ctx, img) {
		return img, nil
	}
	if err := m.docker.ImagePull(ctx, img, nil); err != nil {
		return "", fmt.Errorf("pulling helper image %s: %w", img, err)
	}
	return img, nil
}

func (m *Manager) volumeMounts(app *store.App, readOnly bool) []docker.Mount {
	var ms []docker.Mount
	for _, v := range app.Volumes {
		ms = append(ms, docker.Mount{Type: "volume", Source: app.ServiceName() + "_" + v.Name, Target: "/volumes/" + v.Name, ReadOnly: readOnly})
	}
	return ms
}

// DumpVolumes writes a tar archive of the app's volumes to w, one folder
// per volume. Files change while the app runs, so the copy is only as
// consistent as the app's own writes.
func (m *Manager) DumpVolumes(ctx context.Context, app *store.App, w io.Writer) error {
	if len(app.Volumes) == 0 {
		return errors.New("this app has no volumes")
	}
	img, err := m.helperImage(ctx)
	if err != nil {
		return err
	}
	var stderr tailWriter
	code, err := m.docker.RunContainer(ctx, docker.RunOptions{
		Image: img, Cmd: []string{"tar", "-c", "-f", "-", "-C", "/volumes", "."}, Mounts: m.volumeMounts(app, true),
	}, nil, w, &stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("tar exited with code %d: %s", code, stderr.String())
	}
	return nil
}

// RestoreVolumes stops the app, replaces the contents of its volumes with
// the archive from r, and starts it again.
func (m *Manager) RestoreVolumes(ctx context.Context, app *store.App, r io.Reader, log func(string, ...any)) error {
	if len(app.Volumes) == 0 {
		return errors.New("this app has no volumes")
	}
	// Only the volumes the app still has are mounted; folders in the archive
	// for volumes it no longer has land in the throwaway container.
	return m.replaceVolumes(ctx, app, `set -e
for d in /volumes/*/; do find "$d" -mindepth 1 -delete; done
tar -x -f - -C /volumes`, r, log)
}

// redisRestoreScript replaces a Redis data folder with the RDB snapshot on
// stdin. The snapshot is stored both as dump.rdb and as the base of a new
// append-only file, because Redis ignores dump.rdb when append-only
// persistence is on.
const redisRestoreScript = `set -e
cd /volumes/data
find . -mindepth 1 -delete
cat > dump.rdb
mkdir appendonlydir
cp dump.rdb appendonlydir/appendonly.aof.1.base.rdb
: > appendonlydir/appendonly.aof.1.incr.aof
printf 'file appendonly.aof.1.base.rdb seq 1 type b\nfile appendonly.aof.1.incr.aof seq 1 type i\n' > appendonlydir/appendonly.aof.manifest`

// RestoreRedis stops a Redis database, replaces its data with the RDB
// snapshot from r, and starts it again.
func (m *Manager) RestoreRedis(ctx context.Context, app *store.App, r io.Reader, log func(string, ...any)) error {
	if app.Kind != "redis" || len(app.Volumes) == 0 || app.Volumes[0].Name != "data" {
		return errors.New("not a Redis database with a data volume")
	}
	br := bufio.NewReader(r)
	if magic, err := br.Peek(5); err != nil || string(magic) != "REDIS" {
		return errors.New("the backup isn't a Redis snapshot (RDB file)")
	}
	return m.replaceVolumes(ctx, app, redisRestoreScript, br, log)
}

// replaceVolumes stops the app if it runs, runs script in a helper
// container with the app's volumes under /volumes and r as its input, and
// starts the app again.
func (m *Manager) replaceVolumes(ctx context.Context, app *store.App, script string, r io.Reader, log func(string, ...any)) error {
	img, err := m.helperImage(ctx)
	if err != nil {
		return err
	}
	running := false
	if svc, err := m.docker.ServiceInspect(ctx, app.ServiceName()); err == nil && svc.Spec.Mode.Replicated != nil && svc.Spec.Mode.Replicated.Replicas > 0 {
		running = true
		log("Stopping %s while its data is replaced", app.Name)
		if err := m.Scale(ctx, app, 0); err != nil {
			return err
		}
		if err := m.waitStopped(ctx, app, 2*time.Minute); err != nil {
			return err
		}
	}
	restart := func() {
		if running {
			log("Starting %s again", app.Name)
			if err := m.Scale(context.WithoutCancel(ctx), app, uint64(max(app.Replicas, 1))); err != nil {
				log("Couldn't start it again: %v", err)
			}
		}
	}
	defer restart()
	var stderr tailWriter
	code, err := m.docker.RunContainer(ctx, docker.RunOptions{
		Image: img, Cmd: []string{"sh", "-c", script}, Mounts: m.volumeMounts(app, false),
	}, r, io.Discard, &stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("restoring files failed (code %d): %s", code, stderr.String())
	}
	return nil
}

// waitStopped waits until none of the service's tasks are running.
func (m *Manager) waitStopped(ctx context.Context, app *store.App, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tasks, err := m.docker.ServiceTasks(ctx, app.ServiceName())
		if err != nil {
			return err
		}
		busy := false
		for _, t := range tasks {
			switch t.Status.State {
			case "running", "starting", "preparing", "assigned", "accepted", "ready", "pending", "new":
				busy = true
			}
		}
		if !busy {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("timed out waiting for the app to stop")
}

// WaitDeployment waits for a deployment to finish and reports its outcome.
func (m *Manager) WaitDeployment(ctx context.Context, id int64) (*store.Deployment, error) {
	for {
		d, err := m.store.Deployment(ctx, id)
		if err != nil {
			return nil, err
		}
		if d.Status == "succeeded" || d.Status == "failed" {
			return d, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
