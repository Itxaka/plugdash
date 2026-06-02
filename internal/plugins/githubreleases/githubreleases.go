// Package githubreleases implements a plugin that reports the most recent
// releases of a GitHub repository as a list widget.
package githubreleases

import (
	"context"
	"fmt"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// Plugin reports the latest N releases of a repo.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string          { return "github-releases" }
func (p *Plugin) Name() string        { return "GitHub Releases" }
func (p *Plugin) Description() string { return "Track the latest releases of a GitHub repository." }

// RefreshInterval defaults to daily: releases rarely change minute-to-minute.
func (p *Plugin) RefreshInterval() time.Duration { return 24 * time.Hour }

func (p *Plugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:         "repo",
			Label:       "Repository",
			Type:        plugin.FieldString,
			Required:    true,
			Placeholder: "owner/repo",
			Help:        "GitHub repository as owner/repo or full URL.",
		},
		{
			Key:     "count",
			Label:   "Number of releases",
			Type:    plugin.FieldNumber,
			Default: 5,
			Help:    "How many recent releases to show (default 5).",
		},
		{
			Key:   "token",
			Label: "GitHub token (optional)",
			Type:  plugin.FieldString,
			Help:  "Personal access token to raise rate limits. Falls back to GITHUB_TOKEN env.",
		},
	}
}

// listItem matches the shape the frontend "list" visualization expects.
type listItem struct {
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle"`
	URL       string `json:"url"`
	Timestamp string `json:"timestamp"`
	Badge     string `json:"badge,omitempty"`
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	repo := cfg.String("repo")
	owner, name, err := plugins.NormalizeRepo(repo)
	if err != nil {
		return plugin.Result{}, err
	}
	count := cfg.Int("count")
	if count <= 0 {
		count = 5
	}

	client := plugins.NewGHClient(cfg.String("token"))
	releases, err := client.ListReleases(ctx, owner, name, count)
	if err != nil {
		return plugin.Result{}, err
	}

	items := make([]listItem, 0, len(releases))
	for _, r := range releases {
		title := r.Name
		if title == "" {
			title = r.TagName
		}
		badge := ""
		switch {
		case r.Draft:
			badge = "draft"
		case r.Prerelease:
			badge = "prerelease"
		}
		ts := ""
		if !r.PublishedAt.IsZero() {
			ts = r.PublishedAt.Format("2006-01-02")
		}
		items = append(items, listItem{
			Title:     title,
			Subtitle:  fmt.Sprintf("%s · %d assets", r.TagName, len(r.Assets)),
			URL:       r.HTMLURL,
			Timestamp: ts,
			Badge:     badge,
		})
	}

	return plugin.Result{
		Visualization: plugin.VizList,
		Title:         fmt.Sprintf("%s/%s — latest %d releases", owner, name, len(items)),
		Data:          map[string]any{"items": items},
	}, nil
}
