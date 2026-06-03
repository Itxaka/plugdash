// Package githubdependabot implements a plugin that lists open Dependabot
// security alerts for a single repository, one row per alert with a
// severity- or CVE-tagged badge. Data is fetched just-in-time; nothing is
// persisted between runs.
package githubdependabot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// Plugin lists open Dependabot alerts for a repository.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "github-dependabot" }
func (p *Plugin) Name() string { return "Dependabot Alerts" }
func (p *Plugin) Description() string {
	return "Open Dependabot security alerts for a repository."
}

// RefreshInterval defaults to 1h: advisories are published continuously but a
// repository's alert set doesn't need minute-level polling.
func (p *Plugin) RefreshInterval() time.Duration { return 1 * time.Hour }

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
			Key:     "count",
			Label:   "Number of alerts",
			Type:    plugin.FieldNumber,
			Default: 20,
		},
		{
			Key:   "token",
			Label: "GitHub token (optional)",
			Type:  plugin.FieldString,
			Help:  "Needs a token with security-events / repo read access. Falls back to GITHUB_TOKEN.",
		},
	}
}

// listItem matches the frontend "list" visualization shape (multi-badge form).
type listItem struct {
	Title    string          `json:"title"`
	Subtitle string          `json:"subtitle"`
	URL      string          `json:"url"`
	Badges   []plugins.Badge `json:"badges,omitempty"`
}

// ghAlert is the subset of the Dependabot alert object this plugin consumes.
type ghAlert struct {
	Number           int    `json:"number"`
	State            string `json:"state"`
	HTMLURL          string `json:"html_url"`
	SecurityAdvisory struct {
		Summary  string `json:"summary"`
		Severity string `json:"severity"`
		CVEID    string `json:"cve_id"`
	} `json:"security_advisory"`
	SecurityVulnerability struct {
		Package struct {
			Name      string `json:"name"`
			Ecosystem string `json:"ecosystem"`
		} `json:"package"`
	} `json:"security_vulnerability"`
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	owner, name, err := plugins.NormalizeRepo(cfg.String("repo"))
	if err != nil {
		return plugin.Result{}, err
	}

	count := cfg.Int("count")
	if count <= 0 {
		count = 20
	}
	if count > 100 {
		count = 100
	}

	client := plugins.NewGHClient(cfg.String("token"))

	var alerts []ghAlert
	path := fmt.Sprintf("/repos/%s/%s/dependabot/alerts?state=open&per_page=%d", owner, name, count)
	if err := client.Get(ctx, path, &alerts); err != nil {
		return plugin.Result{}, err
	}

	if len(alerts) == 0 {
		return plugin.Result{
			Visualization: plugin.VizList,
			Title:         fmt.Sprintf("%s/%s — no open alerts", owner, name),
			Data: map[string]any{"items": []listItem{{
				Title:  "No open Dependabot alerts",
				Badges: []plugins.Badge{{Label: "clean", Tone: "ok"}},
			}}},
		}, nil
	}

	items := make([]listItem, 0, len(alerts))
	for _, a := range alerts {
		items = append(items, alertItem(a))
	}

	return plugin.Result{
		Visualization: plugin.VizList,
		Title:         fmt.Sprintf("%s/%s — %d open alert(s)", owner, name, len(items)),
		Data:          map[string]any{"items": items},
	}, nil
}

// alertItem renders one Dependabot alert row.
func alertItem(a ghAlert) listItem {
	severity := a.SecurityAdvisory.Severity
	pkgName := a.SecurityVulnerability.Package.Name
	if eco := a.SecurityVulnerability.Package.Ecosystem; eco != "" {
		pkgName = fmt.Sprintf("%s (%s)", pkgName, eco)
	}

	title := a.SecurityAdvisory.Summary
	if title == "" {
		title = a.SecurityVulnerability.Package.Name
	}

	label := a.SecurityAdvisory.CVEID
	if label == "" {
		label = severity
	}

	return listItem{
		Title:    title,
		Subtitle: fmt.Sprintf("%s · %s", pkgName, severity),
		URL:      a.HTMLURL,
		Badges:   []plugins.Badge{{Label: label, Tone: severityTone(severity)}},
	}
}

// severityTone maps a Dependabot severity to a badge tone.
func severityTone(severity string) string {
	switch strings.ToLower(severity) {
	case "critical", "high":
		return "bad"
	case "medium":
		return "warn"
	case "low":
		return "neutral"
	default:
		return "neutral"
	}
}
