// Package githubmilestone implements a plugin that reports a GitHub milestone's
// completion as a gauge: how many of its issues are closed, with a due-date
// hint. Computed just-in-time; nothing is persisted between runs.
package githubmilestone

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// Plugin reports milestone completion as a gauge.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "github-milestone" }
func (p *Plugin) Name() string { return "Milestone Progress" }
func (p *Plugin) Description() string {
	return "Show how complete a GitHub milestone is (issues closed vs total)."
}

// RefreshInterval defaults to 30m: milestone counts drift gradually.
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
			Key:         "milestone",
			Label:       "Milestone",
			Type:        plugin.FieldString,
			Required:    true,
			Placeholder: "v1.0 or 4",
			Help:        "Milestone title, or its number.",
		},
		{
			Key:   "token",
			Label: "GitHub token (optional)",
			Type:  plugin.FieldString,
			Help:  "Falls back to GITHUB_TOKEN.",
		},
	}
}

// ghMilestone is the subset of GitHub's milestone object this plugin reads.
type ghMilestone struct {
	Number       int        `json:"number"`
	Title        string     `json:"title"`
	State        string     `json:"state"`
	OpenIssues   int        `json:"open_issues"`
	ClosedIssues int        `json:"closed_issues"`
	HTMLURL      string     `json:"html_url"`
	DueOn        *time.Time `json:"due_on"`
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	owner, name, err := plugins.NormalizeRepo(cfg.String("repo"))
	if err != nil {
		return plugin.Result{}, err
	}
	spec := strings.TrimSpace(cfg.String("milestone"))
	if spec == "" {
		return plugin.Result{}, fmt.Errorf("milestone is required")
	}

	client := plugins.NewGHClient(cfg.String("token"))

	ms, err := resolveMilestone(ctx, client, owner, name, spec)
	if err != nil {
		return plugin.Result{}, err
	}

	total := ms.OpenIssues + ms.ClosedIssues
	status := "warn"
	if total > 0 && ms.ClosedIssues == total {
		status = "ok"
	}
	detail := fmt.Sprintf("%d open · %d closed", ms.OpenIssues, ms.ClosedIssues)
	if ms.DueOn != nil && !ms.DueOn.IsZero() {
		due := ms.DueOn.Format("2006-01-02")
		if status != "ok" && time.Now().After(*ms.DueOn) {
			status = "error" // past due and not done
			detail += " · overdue (" + due + ")"
		} else {
			detail += " · due " + due
		}
	}

	return plugin.Result{
		Visualization: plugin.VizGauge,
		Title:         fmt.Sprintf("%s/%s · %s", owner, name, ms.Title),
		Data: map[string]any{
			"label":  "issues closed",
			"value":  ms.ClosedIssues,
			"max":    total,
			"status": status,
			"detail": detail,
		},
	}, nil
}

// resolveMilestone fetches the milestone by number when spec is numeric, else
// matches it by title (case-insensitively) across all milestones.
func resolveMilestone(ctx context.Context, client *plugins.GHClient, owner, name, spec string) (*ghMilestone, error) {
	if n, err := strconv.Atoi(spec); err == nil && n > 0 {
		var ms ghMilestone
		if err := client.Get(ctx, fmt.Sprintf("/repos/%s/%s/milestones/%d", owner, name, n), &ms); err != nil {
			return nil, err
		}
		return &ms, nil
	}

	var all []ghMilestone
	if err := client.Get(ctx, fmt.Sprintf("/repos/%s/%s/milestones?state=all&per_page=100", owner, name), &all); err != nil {
		return nil, err
	}
	for i := range all {
		if strings.EqualFold(all[i].Title, spec) {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("milestone %q not found in %s/%s", spec, owner, name)
}
