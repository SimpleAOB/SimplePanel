package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// MinPublicPassword is the shortest password a database may have while it
// is reachable from the internet. Passwords SimplePanel generates have 32
// characters.
const MinPublicPassword = 16

// PasswordCheck says whether a database's passwords are strong enough to
// open it to the internet. Weak is the reason, or "" when they are.
func PasswordCheck(a *store.App) (weak string, length int) {
	e := EngineByKey(a.Kind)
	if e == nil {
		return "not a database", 0
	}
	vars := envVars(a.Env)
	pw := vars[e.passVar]
	if w := weakPassword(pw); w != "" {
		return w, len(pw)
	}
	if e.rootVar != "" {
		if w := weakPassword(vars[e.rootVar]); w != "" {
			return "the root password " + w, len(pw)
		}
	}
	return "", len(pw)
}

func weakPassword(pw string) string {
	if len(pw) < MinPublicPassword {
		return fmt.Sprintf("is %d characters; use at least %d", len(pw), MinPublicPassword)
	}
	distinct := map[rune]bool{}
	for _, r := range pw {
		distinct[r] = true
	}
	if len(distinct) < 8 {
		return "repeats too few different characters"
	}
	return ""
}

func envVars(env string) map[string]string {
	vars := map[string]string{}
	lines, _ := ParseEnv(env)
	for _, l := range lines {
		k, v, _ := strings.Cut(l, "=")
		vars[k] = v
	}
	return vars
}

// ChangePassword sets a new password for the database's main user inside
// the running database. The caller saves the new environment (see
// WithPassword) and, for Redis, redeploys so a restart keeps it.
func (m *Manager) ChangePassword(ctx context.Context, app *store.App, newPassword string) error {
	if !safeIdent(newPassword) {
		return errors.New("bad password")
	}
	e, vars, err := m.dbVars(app)
	if err != nil {
		return err
	}
	switch e.Key {
	case "postgres":
		_, err = m.adminExec(ctx, app, fmt.Sprintf(`ALTER ROLE "%s" WITH PASSWORD '%s'`, vars[e.userVar], newPassword))
	case "mysql", "mariadb":
		// The image creates the user for any host ('%').
		_, err = m.adminExec(ctx, app, fmt.Sprintf(`ALTER USER '%s'@'%%' IDENTIFIED BY '%s'`, vars[e.userVar], newPassword))
	case "mongo":
		_, err = m.mongoAdmin(ctx, app, `const c = spConnect(process.env.SP_ADMIN_USER, process.env.SP_ADMIN_PW);
c.getSiblingDB('admin').changeUserPassword(process.env.SP_ADMIN_USER, process.env.SP_RO_PW);`, newPassword)
	case "redis":
		_, err = m.redisAdmin(ctx, app, "CONFIG", "SET", "requirepass", newPassword)
	default:
		err = errors.New(e.Label + " passwords can't be changed here")
	}
	return err
}

// WithPassword returns env with the engine's password variable replaced.
func WithPassword(a *store.App, env, password string) string {
	e := EngineByKey(a.Kind)
	if e == nil {
		return env
	}
	lines := strings.Split(env, "\n")
	for i, l := range lines {
		if k, _, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) == e.passVar {
			lines[i] = e.passVar + "=" + password
		}
	}
	return strings.Join(lines, "\n")
}
