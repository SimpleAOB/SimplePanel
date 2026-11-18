package deploy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// MaxPreviews caps how many pull request previews one app can have.
const MaxPreviews = 10

// PreviewName is the service name of a pull request preview:
// <app>-pr-<n>, shortening the app name to stay within 30 characters.
func PreviewName(parent string, pr int) string {
	suffix := fmt.Sprintf("-pr-%d", pr)
	base := parent
	if len(base)+len(suffix) > 30 {
		base = strings.TrimRight(base[:30-len(suffix)], "-")
	}
	return base + suffix
}

// SyncPreview copies the parent app's build and runtime settings onto a
// preview (new or existing) and points it at the pull request's branch.
// Previews run one replica with their own fresh volumes, no published
// ports and no custom domains; they are reached on the automatic
// subdomain. PreviewEnv lines override the parent's variables.
func SyncPreview(p *store.App, parent *store.App, branch, title, prURL string) {
	if p.ID == 0 {
		p.ProjectID, p.ProjectName = parent.ProjectID, parent.ProjectName
		p.Kind = "app"
	}
	p.Source = "git"
	p.GitURL, p.GitBranch, p.GitToken = parent.GitURL, branch, parent.GitToken
	p.GitProviderID, p.GitRepo = parent.GitProviderID, parent.GitRepo
	p.DeployKey, p.DeployKeyPub, p.SSHKnownHosts = parent.DeployKey, parent.DeployKeyPub, parent.SSHKnownHosts
	p.BuildPath, p.Dockerfile, p.BuildType = parent.BuildPath, parent.Dockerfile, parent.BuildType
	p.BuildImage, p.BuildCommand, p.OutputDir, p.SPA = parent.BuildImage, parent.BuildCommand, parent.OutputDir, parent.SPA
	p.Env = MergeEnv(parent.Env, parent.PreviewEnv)
	p.Command, p.CPULimit, p.MemoryMB = parent.Command, parent.CPULimit, parent.MemoryMB
	p.Volumes = append([]store.Volume{}, parent.Volumes...)
	p.Ports = []store.Port{}
	p.Replicas = 1
	p.AutoDomain = true
	p.AutoDeploy, p.Previews, p.PreviewEnv = false, false, ""
	p.PRTitle, p.PRURL = title, prURL
	if len(p.PRTitle) > 200 {
		p.PRTitle = p.PRTitle[:200]
	}
}

// MergeEnv returns base with the KEY=value lines of over replacing or
// adding to it. Invalid lines in over are ignored.
func MergeEnv(base, over string) string {
	if strings.TrimSpace(over) == "" {
		return base
	}
	ov, err := ParseEnv(over)
	if err != nil {
		return base
	}
	set := map[string]string{}
	var order []string
	for _, l := range ov {
		k, _, _ := strings.Cut(l, "=")
		if _, dup := set[k]; !dup {
			order = append(order, k)
		}
		set[k] = l
	}
	var out []string
	for _, line := range strings.Split(base, "\n") {
		t := strings.TrimSpace(line)
		k, _, ok := strings.Cut(t, "=")
		if ok && !strings.HasPrefix(t, "#") {
			if l, hit := set[strings.TrimSpace(k)]; hit {
				out = append(out, l)
				delete(set, strings.TrimSpace(k))
				continue
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	for _, k := range order {
		if l, left := set[k]; left {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n") + "\n"
}

// RemoveWithVolumes deletes the app's service and then its named volumes
// (used for previews, whose data is throwaway). Swarm needs a moment to
// release the volumes, so it retries in the background.
func (m *Manager) RemoveWithVolumes(ctx context.Context, app *store.App) error {
	if err := m.Remove(ctx, app); err != nil {
		return err
	}
	if len(app.Volumes) == 0 {
		return nil
	}
	names := make([]string, len(app.Volumes))
	for i, v := range app.Volumes {
		names[i] = app.ServiceName() + "_" + v.Name
	}
	go func() {
		for i := 0; i < 10 && len(names) > 0; i++ {
			time.Sleep(3 * time.Second)
			left := names[:0]
			for _, n := range names {
				if err := m.docker.VolumeRemove(context.Background(), n); err != nil {
					left = append(left, n)
				}
			}
			names = left
		}
		if len(names) > 0 {
			m.log.Warn("could not remove preview volumes", "volumes", names)
		}
	}()
	return nil
}

// RunningContainer returns the id of a running container of the app.
func (m *Manager) RunningContainer(ctx context.Context, app *store.App) (string, error) {
	tasks, err := m.docker.ServiceTasks(ctx, app.ServiceName())
	if err != nil {
		return "", err
	}
	for _, t := range tasks {
		if t.DesiredState == "running" && t.Status.State == "running" && t.Status.ContainerStatus.ContainerID != "" {
			return t.Status.ContainerStatus.ContainerID, nil
		}
	}
	return "", fmt.Errorf("%s isn't running; deploy or start it first", app.Name)
}
