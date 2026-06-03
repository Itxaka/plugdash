// Package githubstale implements a plugin that surfaces open issues and pull
// requests with no activity for more than N days, across one or more
// repositories, via the GitHub search API. The list is ordered most-stale
// first; everything is fetched just-in-time and nothing is persisted.
package githubstale

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// Plugin lists stale (no recent activity) open issues/PRs across repos.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "github-stale" }
func (p *Plugin) Name() string { return "Stale Items" }
func (p *Plugin) Description() string {
	return "Open issues/PRs with no activity for more than N days across repos."
}

// RefreshInterval is hourly: staleness changes slowly, so there's no need to
// hammer the search API.
func (p *Plugin) RefreshInterval() time.Duration { return 1 * time.Hour }

func (p *Plugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:         "repos",
			Label:       "Repositories",
			Type:        plugin.FieldList,
			Required:    true,
			Placeholder: "kairos-io/kairos",
			Help:        "One owner/repo per line.",
		},
		{
			Key:     "days",
			Label:   "Stale after (days)",
			Type:    plugin.FieldNumber,
			Default: 30,
			Help:    "Flag items with no update in more than this many days.",
		},
		{
			Key:     "type",
			Label:   "Item type",
			Type:    plugin.FieldSelect,
			Default: "any",
			Options: []plugin.SelectOption{
				{Value: "any", Label: "Any"},
				{Value: "issue", Label: "Issues"},
				{Value: "pr", Label: "Pull requests"},
			},
		},
		{
			Key:     "count",
			Label:   "Number of items",
			Type:    plugin.FieldNumber,
			Default: 20,
		},
		{
			Key:   "token",
			Label: "GitHub token (optional)",
			Type:  plugin.FieldString,
			Help:  "Falls back to GITHUB_TOKEN.",
		},
	}
}

// listItem matches the frontend "list" visualization shape (multi-badge form).
type listItem struct {
	Title     string          `json:"title"`
	Subtitle  string          `json:"subtitle"`
	URL       string          `json:"url"`
	Timestamp string          `json:"timestamp"`
	Icon      string          `json:"icon,omitempty"`
	Badges    []plugins.Badge `json:"badges,omitempty"`
}

// searchResult is the subset of the GitHub search/issues response consumed here.
// A non-null pull_request field marks the item as a PR rather than an issue.
type searchResult struct {
	Items []searchItem `json:"items"`
}

type searchItem struct {
	Number        int             `json:"number"`
	Title         string          `json:"title"`
	HTMLURL       string          `json:"html_url"`
	UpdatedAt     time.Time       `json:"updated_at"`
	RepositoryURL string          `json:"repository_url"`
	PullRequest   json.RawMessage `json:"pull_request"`
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	repos := cfg.List("repos")
	if len(repos) == 0 {
		return plugin.Result{}, fmt.Errorf("no repositories configured")
	}

	days := cfg.Int("days")
	if days <= 0 {
		days = 30
	}

	count := cfg.Int("count")
	if count <= 0 {
		count = 20
	}

	itemType := cfg.String("type")

	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")

	q := "is:open"
	switch itemType {
	case "issue":
		q += " is:issue"
	case "pr":
		q += " is:pr"
	}
	q += " updated:<" + cutoff
	for _, raw := range repos {
		owner, name, err := plugins.NormalizeRepo(raw)
		if err != nil {
			continue
		}
		q += " repo:" + owner + "/" + name
	}

	client := plugins.NewGHClient(cfg.String("token"))

	path := fmt.Sprintf("/search/issues?q=%s&per_page=%d&sort=updated&order=asc", url.QueryEscape(q), count)
	var result searchResult
	if err := client.Get(ctx, path, &result); err != nil {
		return plugin.Result{}, err
	}

	results := result.Items
	if len(results) > count {
		results = results[:count]
	}

	items := make([]listItem, 0, len(results))
	for _, it := range results {
		repoFullName := repoFullNameFromURL(it.RepositoryURL)
		owner, _, _ := strings.Cut(repoFullName, "/")

		kind := "issue"
		if len(it.PullRequest) > 0 && string(it.PullRequest) != "null" {
			kind = "PR"
		}

		ts := ""
		if !it.UpdatedAt.IsZero() {
			ts = it.UpdatedAt.Format(time.RFC3339)
		}

		ageDays := int(time.Since(it.UpdatedAt).Hours() / 24)

		items = append(items, listItem{
			Title:     it.Title,
			Subtitle:  fmt.Sprintf("%s#%d · %s", repoFullName, it.Number, kind),
			URL:       it.HTMLURL,
			Timestamp: ts,
			Icon:      plugins.OwnerAvatarURL(owner),
			Badges:    []plugins.Badge{{Label: fmt.Sprintf("stale %dd", ageDays), Tone: "warn"}},
		})
	}

	return plugin.Result{
		Visualization: plugin.VizList,
		Title:         fmt.Sprintf("Stale items — %d", len(items)),
		Data:          map[string]any{"items": items},
	}, nil
}

// repoFullNameFromURL derives "owner/name" from a repository_url such as
// "https://api.github.com/repos/owner/name" by trimming everything up to and
// including "/repos/".
func repoFullNameFromURL(repoURL string) string {
	if i := strings.Index(repoURL, "/repos/"); i >= 0 {
		return strings.Trim(repoURL[i+len("/repos/"):], "/")
	}
	return repoURL
}
