// Package githubrepostats implements a plugdash plugin that reports
// repository-level statistics (stars, forks, open issues, watchers) for a
// GitHub repository.
package githubrepostats

import (
	"context"
	"fmt"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// Plugin reports GitHub repository statistics.
type Plugin struct{}

// New returns a ready-to-register repo-stats plugin.
func New() *Plugin { return &Plugin{} }

// ID is the stable machine identifier.
func (p *Plugin) ID() string { return "github-repo-stats" }

// Name is the human-friendly label shown in the UI.
func (p *Plugin) Name() string { return "GitHub Repo Stats" }

// Description is a one-line explanation of what the plugin tracks.
func (p *Plugin) Description() string {
	return "Show stars, forks, open issues and watchers for a GitHub repository."
}

// RefreshInterval defaults to hourly: counts drift slowly.
func (p *Plugin) RefreshInterval() time.Duration { return time.Hour }

// ConfigSchema lists the configuration fields the plugin accepts.
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
			Key:      "token",
			Label:    "GitHub token",
			Type:     plugin.FieldString,
			Required: false,
			Help:     "Optional. Falls back to the GITHUB_TOKEN environment variable when empty.",
		},
	}
}

// repoInfo is the subset of GitHub's repository object this plugin consumes.
type repoInfo struct {
	StargazersCount  int    `json:"stargazers_count"`
	ForksCount       int    `json:"forks_count"`
	OpenIssuesCount  int    `json:"open_issues_count"`
	SubscribersCount int    `json:"subscribers_count"`
	Language         string `json:"language"`
	Description      string `json:"description"`
	HTMLURL          string `json:"html_url"`
}

// Run fetches repository stats and returns them as a table.
func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	owner, name, err := plugins.NormalizeRepo(cfg.String("repo"))
	if err != nil {
		return plugin.Result{}, err
	}

	client := plugins.NewGHClient(cfg.String("token"))

	var out repoInfo
	if err := client.Get(ctx, "/repos/"+owner+"/"+name, &out); err != nil {
		return plugin.Result{}, err
	}

	rows := [][]any{
		{"Stars", out.StargazersCount},
		{"Forks", out.ForksCount},
		{"Open issues", out.OpenIssuesCount},
		{"Watchers", out.SubscribersCount},
		{"Language", out.Language},
	}

	return plugin.Result{
		Visualization: plugin.VizTable,
		Title:         fmt.Sprintf("%s/%s — repo stats", owner, name),
		Data: map[string]any{
			"columns": []string{"Metric", "Value"},
			"rows":    rows,
		},
	}, nil
}
