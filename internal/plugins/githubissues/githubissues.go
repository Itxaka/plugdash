// Package githubissues implements a plugin that lists, across one or more
// GitHub repositories, the latest open issues that have no response yet (zero
// comments) — a birds-eye "issues that need attention" widget.
package githubissues

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// Plugin lists open issues across repos that still need a first reply.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "github-issues" }
func (p *Plugin) Name() string { return "Issues Needing Attention" }
func (p *Plugin) Description() string {
	return "Latest open issues across repos that have no response yet (zero comments)."
}

// RefreshInterval polls every 15 minutes: open-issue activity is moderately
// volatile but not worth hammering the API for.
func (p *Plugin) RefreshInterval() time.Duration { return 15 * time.Minute }

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
			Key:     "unanswered_only",
			Label:   "Unanswered only",
			Type:    plugin.FieldBool,
			Default: true,
			Help:    "Only show issues with zero comments.",
		},
		{
			Key:         "exclude_labels",
			Label:       "Ignore labels",
			Type:        plugin.FieldList,
			Placeholder: "blocked\nneed-discussion\nwontfix",
			Help:        "Hide issues carrying any of these labels (case-insensitive). One per line.",
		},
		{
			Key:     "count",
			Label:   "Number of issues",
			Type:    plugin.FieldNumber,
			Default: 10,
			Help:    "Max issues to show in total.",
		},
		{
			Key:   "token",
			Label: "GitHub token (optional)",
			Type:  plugin.FieldString,
			Help:  "Falls back to GITHUB_TOKEN.",
		},
	}
}

// ghIssue is the subset of GitHub's issue object this plugin consumes. The
// issues endpoint also returns pull requests; those carry a non-null
// pull_request field and are filtered out.
type ghIssue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	HTMLURL     string          `json:"html_url"`
	Comments    int             `json:"comments"`
	CreatedAt   time.Time       `json:"created_at"`
	PullRequest json.RawMessage `json:"pull_request"`
	User        struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// hasExcludedLabel reports whether the issue carries any of the excluded labels
// (matched case-insensitively).
func (i ghIssue) hasExcludedLabel(excluded map[string]bool) bool {
	for _, l := range i.Labels {
		if excluded[strings.ToLower(strings.TrimSpace(l.Name))] {
			return true
		}
	}
	return false
}

// issue is an issue annotated with the repo it came from.
type issue struct {
	repo string
	ghIssue
}

// listItem matches the shape the frontend "list" visualization expects.
type listItem struct {
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle"`
	URL       string `json:"url"`
	Timestamp string `json:"timestamp"`
	Badge     string `json:"badge,omitempty"`
	Icon      string `json:"icon,omitempty"`
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	repos := cfg.List("repos")
	if len(repos) == 0 {
		return plugin.Result{}, fmt.Errorf("no repositories configured")
	}

	// unanswered_only defaults to true; treat a missing key as true.
	unansweredOnly := true
	if _, ok := cfg["unanswered_only"]; ok {
		unansweredOnly = cfg.Bool("unanswered_only")
	}

	count := cfg.Int("count")
	if count <= 0 {
		count = 10
	}

	excluded := make(map[string]bool)
	for _, l := range cfg.List("exclude_labels") {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
			excluded[l] = true
		}
	}

	client := plugins.NewGHClient(cfg.String("token"))

	var collected []issue
	for _, r := range repos {
		owner, name, err := plugins.NormalizeRepo(r)
		if err != nil {
			// Skip invalid repo specs.
			continue
		}
		repoName := owner + "/" + name
		path := fmt.Sprintf("/repos/%s/%s/issues?state=open&sort=created&direction=desc&per_page=30", owner, name)
		var raw []ghIssue
		if err := client.Get(ctx, path, &raw); err != nil {
			// Per-repo API errors must not fail the whole run.
			continue
		}
		for _, it := range raw {
			// The issues endpoint also returns PRs; drop them.
			if len(it.PullRequest) > 0 && string(it.PullRequest) != "null" {
				continue
			}
			if unansweredOnly && it.Comments != 0 {
				continue
			}
			if len(excluded) > 0 && it.hasExcludedLabel(excluded) {
				continue
			}
			collected = append(collected, issue{repo: repoName, ghIssue: it})
		}
	}

	// Newest first.
	sort.SliceStable(collected, func(i, j int) bool {
		return collected[i].CreatedAt.After(collected[j].CreatedAt)
	})

	if len(collected) > count {
		collected = collected[:count]
	}

	items := make([]listItem, 0, len(collected))
	for _, it := range collected {
		subtitle := fmt.Sprintf("%s #%d · opened %s", it.repo, it.Number, relativeOrDate(it.CreatedAt))
		if !unansweredOnly {
			subtitle += fmt.Sprintf(" · %d comments", it.Comments)
		}
		ts := ""
		if !it.CreatedAt.IsZero() {
			ts = it.CreatedAt.Format(time.RFC3339)
		}
		badge := ""
		if it.Comments == 0 {
			badge = "no reply"
		}
		owner, _, _ := strings.Cut(it.repo, "/")
		items = append(items, listItem{
			Title:     it.Title,
			Subtitle:  subtitle,
			URL:       it.HTMLURL,
			Timestamp: ts,
			Badge:     badge,
			Icon:      plugins.OwnerAvatarURL(owner),
		})
	}

	return plugin.Result{
		Visualization: plugin.VizList,
		Title:         fmt.Sprintf("Issues needing attention — %d", len(items)),
		Data:          map[string]any{"items": items},
	}, nil
}

// relativeOrDate renders a human-friendly age for recent timestamps and falls
// back to an absolute date for older ones.
func relativeOrDate(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		return fmt.Sprintf("%dh ago", h)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	default:
		return t.Format("2006-01-02")
	}
}
