// Package githubworkflow implements a plugdash plugin that reports the CI
// health of a repository's GitHub Actions runs: the success rate over the last
// N runs, plus a run-duration trend rendered as a line chart. Only completed
// runs count toward the rate and the duration series; in-progress runs are
// ignored. Everything is computed just-in-time; nothing is persisted between
// runs.
package githubworkflow

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

const (
	defaultCount = 30
	maxCount     = 100
)

// Plugin reports CI success rate and run-duration trend for a repo's workflow.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "github-workflow-health" }
func (p *Plugin) Name() string { return "Workflow Health" }
func (p *Plugin) Description() string {
	return "CI success rate and run-duration trend for a repository's GitHub Actions workflow."
}

// RefreshInterval defaults to 30m: CI runs accumulate steadily, not by the second.
func (p *Plugin) RefreshInterval() time.Duration { return 30 * time.Minute }

func (p *Plugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:         "repo",
			Label:       "Repository",
			Type:        plugin.FieldString,
			Required:    true,
			Placeholder: "owner/repo",
		},
		{
			Key:         "workflow",
			Label:       "Workflow",
			Type:        plugin.FieldString,
			Placeholder: "ci.yml",
			Help:        "Workflow file name or ID. Leave empty for all workflows.",
		},
		{
			Key:   "branch",
			Label: "Branch",
			Type:  plugin.FieldString,
			Help:  "Limit to a branch (default: all).",
		},
		{
			Key:     "count",
			Label:   "Number of runs",
			Type:    plugin.FieldNumber,
			Default: defaultCount,
			Help:    "How many recent runs to sample.",
		},
		{
			Key:   "token",
			Label: "GitHub token (optional)",
			Type:  plugin.FieldString,
			Help:  "Falls back to GITHUB_TOKEN.",
		},
	}
}

// point is one sample on the duration timeseries.
type point struct {
	T string  `json:"t"`
	V float64 `json:"v"`
}

// timeseriesData matches the frontend "timeseries" visualization shape.
type timeseriesData struct {
	Label  string  `json:"label"`
	Unit   string  `json:"unit"`
	Total  float64 `json:"total"`
	Points []point `json:"points"`
}

// workflowRun is the subset of a GitHub Actions run object this plugin reads.
type workflowRun struct {
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	RunStartedAt time.Time `json:"run_started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	HTMLURL      string    `json:"html_url"`
	CreatedAt    time.Time `json:"created_at"`
}

// runsEnvelope is the GitHub Actions runs list response.
type runsEnvelope struct {
	TotalCount   int           `json:"total_count"`
	WorkflowRuns []workflowRun `json:"workflow_runs"`
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	owner, name, err := plugins.NormalizeRepo(cfg.String("repo"))
	if err != nil {
		return plugin.Result{}, err
	}

	workflow := strings.TrimSpace(cfg.String("workflow"))
	branch := strings.TrimSpace(cfg.String("branch"))

	count := cfg.Int("count")
	if count <= 0 {
		count = defaultCount
	}
	if count > maxCount {
		count = maxCount
	}

	branchParam := ""
	if branch != "" {
		branchParam = "&branch=" + branch
	}

	var path string
	if workflow != "" {
		path = fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/runs?per_page=%d%s", owner, name, workflow, count, branchParam)
	} else {
		path = fmt.Sprintf("/repos/%s/%s/actions/runs?per_page=%d%s", owner, name, count, branchParam)
	}

	client := plugins.NewGHClient(cfg.String("token"))
	var env runsEnvelope
	if err := client.Get(ctx, path, &env); err != nil {
		return plugin.Result{}, err
	}

	// Only completed runs count toward the success rate and the duration series.
	completed := make([]workflowRun, 0, len(env.WorkflowRuns))
	successCount := 0
	for _, run := range env.WorkflowRuns {
		if run.Status != "completed" {
			continue
		}
		completed = append(completed, run)
		if run.Conclusion == "success" {
			successCount++
		}
	}

	completedCount := len(completed)
	successRate := 0.0
	if completedCount > 0 {
		successRate = 100 * float64(successCount) / float64(completedCount)
	}

	// Duration series: ascending by run start time.
	sort.SliceStable(completed, func(i, j int) bool {
		return completed[i].RunStartedAt.Before(completed[j].RunStartedAt)
	})
	points := make([]point, 0, completedCount)
	for _, run := range completed {
		mins := run.UpdatedAt.Sub(run.RunStartedAt).Minutes()
		if mins < 0 {
			mins = 0
		}
		mins = math.Round(mins*10) / 10
		points = append(points, point{T: run.RunStartedAt.Format(time.RFC3339), V: mins})
	}

	title := fmt.Sprintf("%s/%s — CI %.0f%% success (last %d runs)", owner, name, successRate, completedCount)
	if workflow != "" {
		title = fmt.Sprintf("%s/%s %s — CI %.0f%% success (last %d runs)", owner, name, workflow, successRate, completedCount)
	}

	return plugin.Result{
		Visualization: plugin.VizTimeseries,
		Title:         title,
		Data: timeseriesData{
			Label:  "Run duration (min)",
			Unit:   "min",
			Total:  successRate,
			Points: points,
		},
	}, nil
}
