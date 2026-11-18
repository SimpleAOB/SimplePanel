// Package gitprovider talks to GitHub (through a GitHub App the panel
// creates for itself) and GitLab (through an access token): repository
// lists, clone tokens, commit statuses and pull request comments. It also
// parses their webhook deliveries.
package gitprovider

import (
	"encoding/json"
	"strings"
)

// Event is a push or pull request from GitHub or GitLab, reduced to what
// SimplePanel acts on.
type Event struct {
	Kind          string // "push", "pr" or "" (ignored)
	Name          string // the provider's event name, for messages
	Repo          string // "owner/name" (GitLab: path with namespace)
	DefaultBranch string
	Branch        string // push: the branch pushed; pr: the head branch
	Deleted       bool   // push that deleted the branch
	SHA           string // head commit
	Message       string // head commit message (push)

	PR       int    // pull/merge request number
	PRAction string // "open", "update", "edit" (title only) or "close"
	PRBase   string // target branch
	PRTitle  string
	PRURL    string
	Fork     bool // the head branch lives in another repository
}

// Ignored reports whether SimplePanel has nothing to do for the event.
func (e *Event) Ignored() bool { return e.Kind == "" }

// ParseGitHub parses a GitHub webhook delivery. event is the
// X-GitHub-Event header.
func ParseGitHub(event string, body []byte) (*Event, error) {
	ev := &Event{Name: event}
	switch event {
	case "push":
		var p struct {
			Ref        string `json:"ref"`
			After      string `json:"after"`
			Deleted    bool   `json:"deleted"`
			HeadCommit *struct {
				Message string `json:"message"`
			} `json:"head_commit"`
			Repository ghRepo `json:"repository"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		ev.Repo, ev.DefaultBranch = p.Repository.FullName, p.Repository.DefaultBranch
		branch, ok := strings.CutPrefix(p.Ref, "refs/heads/")
		if !ok {
			return ev, nil // tags are ignored
		}
		ev.Kind, ev.Branch, ev.SHA, ev.Deleted = "push", branch, p.After, p.Deleted
		if p.HeadCommit != nil {
			ev.Message = firstLine(p.HeadCommit.Message)
		}
	case "pull_request":
		var p struct {
			Action      string `json:"action"`
			Number      int    `json:"number"`
			PullRequest struct {
				Title   string `json:"title"`
				HTMLURL string `json:"html_url"`
				Head    struct {
					Ref  string  `json:"ref"`
					SHA  string  `json:"sha"`
					Repo *ghRepo `json:"repo"`
				} `json:"head"`
				Base struct {
					Ref string `json:"ref"`
				} `json:"base"`
			} `json:"pull_request"`
			Repository ghRepo `json:"repository"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		ev.Repo, ev.DefaultBranch = p.Repository.FullName, p.Repository.DefaultBranch
		pr := p.PullRequest
		ev.PR, ev.PRTitle, ev.PRURL, ev.PRBase = p.Number, pr.Title, pr.HTMLURL, pr.Base.Ref
		ev.Branch, ev.SHA = pr.Head.Ref, pr.Head.SHA
		ev.Fork = pr.Head.Repo == nil || !strings.EqualFold(pr.Head.Repo.FullName, p.Repository.FullName)
		switch p.Action {
		case "opened", "reopened", "ready_for_review":
			ev.Kind, ev.PRAction = "pr", "open"
		case "synchronize":
			ev.Kind, ev.PRAction = "pr", "update"
		case "edited":
			ev.Kind, ev.PRAction = "pr", "edit" // title or description changed
		case "closed":
			ev.Kind, ev.PRAction = "pr", "close"
		}
	}
	return ev, nil
}

type ghRepo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

// ParseGitLab parses a GitLab webhook delivery. event is the
// X-Gitlab-Event header.
func ParseGitLab(event string, body []byte) (*Event, error) {
	ev := &Event{Name: event}
	type project struct {
		ID                int64  `json:"id"`
		PathWithNamespace string `json:"path_with_namespace"`
		DefaultBranch     string `json:"default_branch"`
	}
	switch event {
	case "Push Hook":
		var p struct {
			Ref         string  `json:"ref"`
			After       string  `json:"after"`
			CheckoutSHA *string `json:"checkout_sha"`
			Project     project `json:"project"`
			Commits     []struct {
				ID      string `json:"id"`
				Message string `json:"message"`
			} `json:"commits"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		ev.Repo, ev.DefaultBranch = p.Project.PathWithNamespace, p.Project.DefaultBranch
		branch, ok := strings.CutPrefix(p.Ref, "refs/heads/")
		if !ok {
			return ev, nil
		}
		ev.Kind, ev.Branch, ev.SHA = "push", branch, p.After
		ev.Deleted = p.CheckoutSHA == nil || strings.Trim(p.After, "0") == ""
		for _, c := range p.Commits {
			if c.ID == p.After {
				ev.Message = firstLine(c.Message)
			}
		}
	case "Merge Request Hook":
		var p struct {
			Project          project `json:"project"`
			ObjectAttributes struct {
				IID             int    `json:"iid"`
				Title           string `json:"title"`
				URL             string `json:"url"`
				Action          string `json:"action"`
				SourceBranch    string `json:"source_branch"`
				TargetBranch    string `json:"target_branch"`
				OldRev          string `json:"oldrev"`
				SourceProjectID int64  `json:"source_project_id"`
				TargetProjectID int64  `json:"target_project_id"`
				LastCommit      struct {
					ID string `json:"id"`
				} `json:"last_commit"`
			} `json:"object_attributes"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		o := p.ObjectAttributes
		ev.Repo, ev.DefaultBranch = p.Project.PathWithNamespace, p.Project.DefaultBranch
		ev.PR, ev.PRTitle, ev.PRURL, ev.PRBase = o.IID, o.Title, o.URL, o.TargetBranch
		ev.Branch, ev.SHA = o.SourceBranch, o.LastCommit.ID
		ev.Fork = o.SourceProjectID != o.TargetProjectID
		switch o.Action {
		case "open", "reopen":
			ev.Kind, ev.PRAction = "pr", "open"
		case "update":
			ev.Kind, ev.PRAction = "pr", "update"
			if o.OldRev == "" {
				ev.PRAction = "edit" // no new commits
			}
		case "close", "merge":
			ev.Kind, ev.PRAction = "pr", "close"
		}
	}
	return ev, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
