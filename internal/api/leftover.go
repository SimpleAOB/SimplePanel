package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// leftoverGuard stops new services from starting on top of the data that
// deleted services with the same names left behind (see
// deploy.LeftoverVolumes): a database would skip its setup, ignore its new
// password, or not start at all, and an app would find someone else's
// files. It answers 409 with code "leftover_data" and the volume names,
// unless replace is set; then it deletes the old volumes first, which
// needs the manage level on the project (canReplace). Callers skip it when
// the request asks to keep and use the old data.
//
// apps are unsaved services (or, for new volumes on a saved app, a copy
// holding only those volumes; msg then explains). If Docker can't be
// asked, the change goes ahead and the deploy reports Docker's problem.
func (s *Server) leftoverGuard(w http.ResponseWriter, r *http.Request, apps []*store.App, replace, canReplace bool, msg string) bool {
	var vols, names []string
	dbs := 0
	for _, a := range apps {
		v, err := s.deploy.LeftoverVolumes(r.Context(), a)
		if err != nil {
			s.log.Warn("checking for leftover volumes", "app", a.ServiceName(), "err", err)
			continue
		}
		if len(v) > 0 {
			vols = append(vols, v...)
			names = append(names, a.Name)
			if a.IsDatabase() {
				dbs++
			}
		}
	}
	if len(vols) == 0 {
		return true
	}
	if !replace {
		if msg == "" {
			kind := "app"
			if dbs == len(names) {
				kind = "database"
			}
			if len(names) == 1 {
				msg = fmt.Sprintf("%s %s named %q was deleted from this project earlier, and its data is still on the server. A new %s would start on top of that old data. ",
					map[bool]string{true: "An", false: "A"}[kind == "app"], kind, names[0], kind)
				if kind == "app" {
					msg += "Delete the old data, use it as it is, or choose another name."
				} else {
					// The database would skip its setup and keep the old
					// users and passwords, so using the data isn't offered.
					msg += "Delete the old data, or choose another name."
				}
			} else {
				msg = fmt.Sprintf("Services named %s were deleted earlier, and their data is still on the server. New services would start on top of that old data. Delete the old data, or import them under other names.", quoteList(names))
			}
		}
		writeJSON(w, 409, map[string]any{
			"error":      msg,
			"code":       "leftover_data",
			"volumes":    vols,
			"canReplace": canReplace,
		})
		return false
	}
	if !canReplace {
		writeErr(w, 403, "your role can't delete a deleted service's old data; ask an admin, or choose another name")
		return false
	}
	if err := s.deploy.RemoveLeftoverVolumes(r.Context(), vols); err != nil {
		writeErr(w, 409, err.Error())
		return false
	}
	return true
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	if len(q) == 2 {
		return q[0] + " and " + q[1]
	}
	return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
}
