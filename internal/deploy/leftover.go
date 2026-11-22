package deploy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// A service's Docker volumes are named <project>_<name>_<volume>. Deleting
// a service deletes them by default (RemoveData), but people can keep them,
// and services deleted by older versions or through the API keep them too.
// A new service with the same name in the same project gets the same
// volume names, and so starts on top of the old data.
//
// For a database that never works: the image sees a data directory that
// already exists, skips its first-time setup (so the new user and password
// are never created), and if the old data came from another engine (MySQL
// data under MariaDB, say) the server can't even start. These helpers let
// the API spot that before creating the database.

// LeftoverVolumes returns the names of app's volumes that already exist on
// the server. app must not be saved yet: for a saved app these are simply
// its own volumes.
func (m *Manager) LeftoverVolumes(ctx context.Context, app *store.App) ([]string, error) {
	var out []string
	for _, name := range VolumeNames(app) {
		ok, err := m.docker.VolumeExists(ctx, name)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, name)
		}
	}
	return out, nil
}

// RemoveLeftoverVolumes deletes volumes found by LeftoverVolumes. A volume
// can stay in use for a few seconds after its service is removed (Swarm
// cleans up the stopped container, and a database may take its whole stop
// grace period to shut down), so it retries for about half a minute.
func (m *Manager) RemoveLeftoverVolumes(ctx context.Context, names []string) error {
	for _, n := range names {
		var err error
		for i := 0; i < 15; i++ {
			if err = m.docker.VolumeRemove(ctx, n); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		if err != nil {
			if strings.Contains(err.Error(), "in use") {
				return fmt.Errorf("the old data in volume %s is still in use by a container; try again in a minute", n)
			}
			return fmt.Errorf("deleting old volume %s: %w", n, err)
		}
		m.log.Info("removed leftover volume", "volume", n)
	}
	return nil
}

// VolumeNames returns the Docker volume names of app's volumes.
func VolumeNames(app *store.App) []string {
	out := make([]string, len(app.Volumes))
	for i, v := range app.Volumes {
		out[i] = app.ServiceName() + "_" + v.Name
	}
	return out
}

// RemoveData deletes app's volumes, once its service is gone.
func (m *Manager) RemoveData(ctx context.Context, app *store.App) error {
	return m.RemoveLeftoverVolumes(ctx, VolumeNames(app))
}
