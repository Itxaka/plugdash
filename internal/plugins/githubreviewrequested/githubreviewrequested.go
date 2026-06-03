// Package githubreviewrequested implements a plugin that lists open pull
// requests awaiting a given user's review, via the GitHub search API. The user
// can be named explicitly or resolved from the authenticated token. Everything
// is fetched just-in-time; nothing is persisted between runs.
package githubreviewrequested

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// Plugin lists open PRs awaiting a user's review across GitHub.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "github-review-requested" }
func (p *Plugin) Name() string { return "Review Requested" }
func (p *Plugin) Description() string {
	return "Open pull requests waiting for your review across GitHub."
}

// RefreshInterval defaults to 10m: review queues move at a moderate pace.
func (p *Plugin) RefreshInterval() time.Duration { return 10 * time.Minute }

func (p *Plugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:         "login",
			Label:       "Login",
			Type:        plugin.FieldString,
			Placeholder: "octocat",
			Help:        "GitHub login whose requested reviews to show. Leave empty to use the authenticated token's user.",
		},
		{
			Key:   "repos",
			Label: "Repositories",
			Type:  plugin.FieldList,
			Help:  "Limit to these owner/repo (one per line). Leave empty for all of GitHub.",
		},
		{
			Key:     "count",
			Label:   "Number of PRs",
			Type:    plugin.FieldNumber,
			Default: 20,
		},
		{
			Key:   "token",
			Label: "GitHub token (optional)",
			Type:  plugin.FieldString,
			Help:  "Falls back to GITHUB_TOKEN. Required if login is empty.",
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

// searchResp is the subset of the search/issues response this plugin consumes.
type searchResp struct {
	TotalCount int          `json:"total_count"`
	Items      []searchItem `json:"items"`
}

type searchItem struct {
	Number        int       `json:"number"`
	Title         string    `json:"title"`
	HTMLURL       string    `json:"html_url"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	RepositoryURL string    `json:"repository_url"`
	User          struct {
		Login string `json:"login"`
	} `json:"user"`
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	count := cfg.Int("count")
	if count <= 0 {
		count = 20
	}

	client := plugins.NewGHClient(cfg.String("token"))

	login := strings.TrimSpace(cfg.String("login"))
	if login == "" {
		var who struct {
			Login string `json:"login"`
		}
		if err := client.Get(ctx, "/user", &who); err != nil || who.Login == "" {
			return plugin.Result{}, fmt.Errorf("set a login or provide a token")
		}
		login = who.Login
	}

	// Build the search query: open PRs with this login as a requested reviewer,
	// optionally scoped to specific repos.
	q := "is:open is:pr review-requested:" + login
	for _, raw := range cfg.List("repos") {
		owner, name, err := plugins.NormalizeRepo(raw)
		if err != nil {
			continue
		}
		q += " repo:" + owner + "/" + name
	}

	path := fmt.Sprintf("/search/issues?q=%s&per_page=%d&sort=updated&order=desc", url.QueryEscape(q), count)
	var resp searchResp
	if err := client.Get(ctx, path, &resp); err != nil {
		return plugin.Result{}, err
	}

	items := make([]listItem, 0, len(resp.Items))
	for _, pr := range resp.Items {
		repoFullName := repoFromURL(pr.RepositoryURL)
		owner := ""
		if i := strings.Index(repoFullName, "/"); i >= 0 {
			owner = repoFullName[:i]
		}
		ts := ""
		if !pr.UpdatedAt.IsZero() {
			ts = pr.UpdatedAt.Format(time.RFC3339)
		}
		items = append(items, listItem{
			Title:     pr.Title,
			Subtitle:  fmt.Sprintf("%s#%d · @%s", repoFullName, pr.Number, pr.User.Login),
			URL:       pr.HTMLURL,
			Timestamp: ts,
			Icon:      plugins.OwnerAvatarURL(owner),
			Badges:    []plugins.Badge{{Label: "review requested", Tone: "warn"}},
		})
	}

	// The API already limits via per_page, but cap defensively.
	if len(items) > count {
		items = items[:count]
	}

	return plugin.Result{
		Visualization: plugin.VizList,
		Title:         fmt.Sprintf("Review requested — %d", len(items)),
		Data:          map[string]any{"items": items},
	}, nil
}

// repoFromURL derives "OWNER/REPO" from a repository_url of the form
// "https://api.github.com/repos/OWNER/REPO".
func repoFromURL(repositoryURL string) string {
	const marker = "/repos/"
	if i := strings.Index(repositoryURL, marker); i >= 0 {
		return strings.Trim(repositoryURL[i+len(marker):], "/")
	}
	return repositoryURL
}
