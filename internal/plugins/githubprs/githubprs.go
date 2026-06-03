// Package githubprs implements a plugin that lists open pull requests across one
// or more repositories, annotating each with its review state, CI status, and
// draft flag — a review queue for a single developer or a small team. Everything
// is fetched just-in-time; nothing is persisted between runs.
package githubprs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// Plugin lists open PRs across repos with review/CI state.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "github-prs" }
func (p *Plugin) Name() string { return "Pull Requests" }
func (p *Plugin) Description() string {
	return "Open pull requests across repos with review state and CI status."
}

// RefreshInterval defaults to 5m: PRs move faster than issues.
func (p *Plugin) RefreshInterval() time.Duration { return 5 * time.Minute }

func (p *Plugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:         "repos",
			Label:       "Repositories",
			Type:        plugin.FieldList,
			Required:    true,
			Placeholder: "kairos-io/kairos\nkairos-io/immucore",
			Help:        "One owner/repo per line.",
		},
		{
			Key:         "author",
			Label:       "Author filter",
			Type:        plugin.FieldString,
			Placeholder: "octocat",
			Help:        "Only show PRs opened by this GitHub login. Leave empty for all.",
		},
		{
			Key:         "reviewer",
			Label:       "Review-requested-of filter",
			Type:        plugin.FieldString,
			Placeholder: "octocat",
			Help:        "Only show PRs where this login is a requested reviewer. Leave empty for all.",
		},
		{
			Key:     "count",
			Label:   "Number of PRs",
			Type:    plugin.FieldNumber,
			Default: 20,
			Help:    "Max PRs to show in total (across all repos).",
		},
		{
			Key:   "token",
			Label: "GitHub token (optional)",
			Type:  plugin.FieldString,
			Help:  "Personal access token to raise rate limits. Falls back to GITHUB_TOKEN env.",
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

// ghPull is the subset of the pulls list object this plugin consumes.
type ghPull struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	HTMLURL   string    `json:"html_url"`
	Draft     bool      `json:"draft"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	RequestedReviewers []struct {
		Login string `json:"login"`
	} `json:"requested_reviewers"`
}

type ghReview struct {
	State string `json:"state"`
	User  struct {
		Login string `json:"login"`
	} `json:"user"`
}

// collected pairs a PR with the repo it came from, for cross-repo sorting.
type collected struct {
	owner, repo string
	pull        ghPull
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	repos := cfg.List("repos")
	if len(repos) == 0 {
		return plugin.Result{}, fmt.Errorf("no repositories configured")
	}
	author := strings.TrimSpace(cfg.String("author"))
	reviewer := strings.TrimSpace(cfg.String("reviewer"))
	count := cfg.Int("count")
	if count <= 0 {
		count = 20
	}

	client := plugins.NewGHClient(cfg.String("token"))

	var items []listItem // error rows for bad repos, prepended as encountered
	var pulls []collected
	for _, raw := range repos {
		owner, name, err := plugins.NormalizeRepo(raw)
		if err != nil {
			items = append(items, errorItem(raw, "invalid repo"))
			continue
		}
		var repoPulls []ghPull
		path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&per_page=100&sort=updated&direction=desc", owner, name)
		if err := client.Get(ctx, path, &repoPulls); err != nil {
			items = append(items, errorItem(owner+"/"+name, "error: "+err.Error()))
			continue
		}
		for _, pr := range repoPulls {
			if author != "" && !strings.EqualFold(pr.User.Login, author) {
				continue
			}
			if reviewer != "" && !hasRequestedReviewer(pr, reviewer) {
				continue
			}
			pulls = append(pulls, collected{owner: owner, repo: name, pull: pr})
		}
	}

	// Newest-updated first across all repos, then cap.
	sort.SliceStable(pulls, func(i, j int) bool {
		return pulls[i].pull.UpdatedAt.After(pulls[j].pull.UpdatedAt)
	})
	if len(pulls) > count {
		pulls = pulls[:count]
	}

	for _, c := range pulls {
		items = append(items, buildItem(ctx, client, c))
	}

	return plugin.Result{
		Visualization: plugin.VizList,
		Title:         fmt.Sprintf("Open PRs — %d", len(pulls)),
		Data:          map[string]any{"items": items},
	}, nil
}

// buildItem renders one PR row, fetching its review state and CI status.
func buildItem(ctx context.Context, client *plugins.GHClient, c collected) listItem {
	pr := c.pull
	label := fmt.Sprintf("%s/%s#%d", c.owner, c.repo, pr.Number)

	badges := make([]plugins.Badge, 0, 3)
	if pr.Draft {
		badges = append(badges, plugins.Badge{Label: "draft", Tone: "neutral"})
	}
	badges = append(badges, reviewBadge(ctx, client, c.owner, c.repo, pr.Number))
	if pr.Head.SHA != "" {
		if ci, ok := client.CIBadge(ctx, c.owner, c.repo, pr.Head.SHA); ok {
			badges = append(badges, ci)
		}
	}

	ts := ""
	if !pr.UpdatedAt.IsZero() {
		ts = pr.UpdatedAt.Format(time.RFC3339)
	}
	return listItem{
		Title:     pr.Title,
		Subtitle:  fmt.Sprintf("%s · @%s", label, pr.User.Login),
		URL:       pr.HTMLURL,
		Timestamp: ts,
		Icon:      plugins.OwnerAvatarURL(c.owner),
		Badges:    badges,
	}
}

// reviewBadge derives an aggregate review-state badge from a PR's reviews:
// changes-requested wins, then approved, otherwise review-pending. Each
// reviewer's latest *decisive* review (APPROVED / CHANGES_REQUESTED) counts.
func reviewBadge(ctx context.Context, client *plugins.GHClient, owner, repo string, number int) plugins.Badge {
	var reviews []ghReview
	if err := client.Get(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number), &reviews); err != nil {
		return plugins.Badge{Label: "review pending", Tone: "warn"}
	}
	latest := map[string]string{} // login -> latest decisive state
	for _, r := range reviews {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED":
			latest[r.User.Login] = r.State
		}
	}
	approved := false
	for _, state := range latest {
		if state == "CHANGES_REQUESTED" {
			return plugins.Badge{Label: "changes requested", Tone: "bad"}
		}
		if state == "APPROVED" {
			approved = true
		}
	}
	if approved {
		return plugins.Badge{Label: "approved", Tone: "ok"}
	}
	return plugins.Badge{Label: "review pending", Tone: "warn"}
}

func hasRequestedReviewer(pr ghPull, login string) bool {
	for _, rr := range pr.RequestedReviewers {
		if strings.EqualFold(rr.Login, login) {
			return true
		}
	}
	return false
}

func errorItem(label, detail string) listItem {
	return listItem{
		Title:    label,
		Subtitle: detail,
		Badges:   []plugins.Badge{{Label: "error", Tone: "bad"}},
	}
}
