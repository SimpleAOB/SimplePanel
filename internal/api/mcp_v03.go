package api

import (
	"encoding/json"
	"strconv"

	"github.com/SimpleAOB/SimplePanel/internal/store"
)

// v03Tools are the MCP tools for cron jobs and pull request previews. They
// go through the web API's handlers, so roles and scopes are checked the
// same way.
func v03Tools() []mcpTool {
	idPath := func(id int64) map[string]string { return map[string]string{"id": strconv.FormatInt(id, 10)} }
	return []mcpTool{
		{
			name: "list_cron_jobs", title: "List a service's cron jobs", scope: store.ScopeRead, readOnly: true,
			description: "Lists the scheduled commands of a service with their schedule (five-field cron, server time zone), whether they're on, the next run and the last result.",
			props:       map[string]any{"service": pService},
			required:    []string{"service"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				raw, err := c.invoke(c.s.listCron, "GET", "/", idPath(app.ID), nil)
				return json.RawMessage(raw), err
			},
		},
		{
			name: "get_cron_runs", title: "Get a cron job's recent runs", scope: store.ScopeRead, readOnly: true,
			description: "Lists a cron job's recent runs, newest first. With run_id, returns that run's output (the last 64 KB).",
			props: map[string]any{
				"job_id": prop("integer", "Job id from list_cron_jobs."),
				"run_id": prop("integer", "Optional: one run's id, to read its output."),
			},
			required: []string{"job_id"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					JobID int64 `json:"job_id"`
					RunID int64 `json:"run_id"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				if a.RunID != 0 {
					raw, err := c.invoke(c.s.getCronRun, "GET", "/", idPath(a.RunID), nil)
					return json.RawMessage(raw), err
				}
				raw, err := c.invoke(c.s.listCronRuns, "GET", "/", idPath(a.JobID), nil)
				return json.RawMessage(raw), err
			},
		},
		{
			name: "run_cron_job", title: "Run a cron job now", scope: store.ScopeManage, audit: true,
			description: "Starts a cron job right away, outside its schedule. Returns the run id; read its output with get_cron_runs.",
			props:       map[string]any{"job_id": prop("integer", "Job id from list_cron_jobs.")},
			required:    []string{"job_id"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					JobID int64 `json:"job_id"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				raw, err := c.invoke(c.s.runCron, "POST", "/", idPath(a.JobID), nil)
				return json.RawMessage(raw), err
			},
		},
		{
			name: "list_previews", title: "List pull request previews", scope: store.ScopeRead, readOnly: true,
			description: "Lists the pull request previews of a Git app: pull request, branch, status and address. Previews are separate services named <app>-pr-<number>.",
			props:       map[string]any{"service": pService},
			required:    []string{"service"},
			run: func(c *mcpCall) (any, error) {
				var a struct {
					Service string `json:"service"`
				}
				if err := c.bind(&a); err != nil {
					return nil, err
				}
				app, err := c.service(a.Service)
				if err != nil {
					return nil, err
				}
				raw, err := c.invoke(c.s.listPreviews, "GET", "/", idPath(app.ID), nil)
				return json.RawMessage(raw), err
			},
		},
	}
}
