// Package githubissuewatch implements a plugin that watches a specific set of
// GitHub issues (or pull requests) and reports, per item, whether it has been
// answered (the latest comment is from someone other than the author), how long
// since the last interaction, and — for pull requests — the status of its CI
// checks. Everything is fetched just-in-time; nothing is persisted between runs.
package githubissuewatch

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// Plugin watches specific issues/PRs for answered state, staleness and CI.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "github-issue-watch" }
func (p *Plugin) Name() string { return "Issue Watcher" }
func (p *Plugin) Description() string {
	return "Watch specific issues/PRs: answered state, time since last reply, and CI status."
}

// RefreshInterval defaults to 15m: issue/PR activity is moderately volatile.
func (p *Plugin) RefreshInterval() time.Duration { return 15 * time.Minute }

func (p *Plugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:         "issues",
			Label:       "Issues / PRs",
			Type:        plugin.FieldList,
			Required:    true,
			Placeholder: "kairos-io/kairos#1234\nhttps://github.com/kairos-io/kairos/pull/56",
			Help:        "One per line: owner/repo#number, or a full issue/PR URL.",
		},
		{
			Key:   "token",
			Label: "GitHub token (optional)",
			Type:  plugin.FieldString,
			Help:  "Personal access token to raise rate limits. Falls back to GITHUB_TOKEN env.",
		},
	}
}

// listItem matches the frontend "list" visualization shape, extended with a
// multi-badge slice (the single-badge form is unused here).
type listItem struct {
	Title     string          `json:"title"`
	Subtitle  string          `json:"subtitle"`
	URL       string          `json:"url"`
	Timestamp string          `json:"timestamp"`
	Icon      string          `json:"icon,omitempty"`
	Badges    []plugins.Badge `json:"badges,omitempty"`
}

// itemResult pairs the rendered list item with the facts the run needs to
// summarize (answered state and whether the item failed to resolve).
type itemResult struct {
	item     listItem
	answered bool
	isError  bool
}

// ghIssue is the subset of the issue object this plugin consumes. The issues
// endpoint also serves pull requests; those carry a non-null pull_request field.
type ghIssue struct {
	Title     string    `json:"title"`
	HTMLURL   string    `json:"html_url"`
	State     string    `json:"state"`
	Comments  int       `json:"comments"`
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
}

type ghComment struct {
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

type ghPull struct {
	Merged bool `json:"merged"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	refs := cfg.List("issues")
	if len(refs) == 0 {
		return plugin.Result{}, fmt.Errorf("no issues configured")
	}

	client := plugins.NewGHClient(cfg.String("token"))

	items := make([]listItem, 0, len(refs))
	unanswered := 0
	for _, ref := range refs {
		r := evalRef(ctx, client, ref)
		items = append(items, r.item)
		if !r.isError && !r.answered {
			unanswered++
		}
	}

	return plugin.Result{
		Visualization: plugin.VizList,
		Title:         fmt.Sprintf("Issue watch — %d unanswered / %d", unanswered, len(items)),
		Data:          map[string]any{"items": items},
	}, nil
}

// evalRef resolves one ref into a list item. It never returns an error: a bad
// ref or a fetch failure becomes an error row so one bad entry can't sink the run.
func evalRef(ctx context.Context, client *plugins.GHClient, ref string) itemResult {
	owner, repo, num, err := parseRef(ref)
	if err != nil {
		return itemResult{
			isError: true,
			item: listItem{
				Title:    ref,
				Subtitle: err.Error(),
				Badges:   []plugins.Badge{{Label: "invalid", Tone: "bad"}},
			},
		}
	}

	label := fmt.Sprintf("%s/%s#%d", owner, repo, num)
	icon := plugins.OwnerAvatarURL(owner)
	fallbackURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, num)

	var iss ghIssue
	if err := client.Get(ctx, fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, num), &iss); err != nil {
		return itemResult{
			isError: true,
			item: listItem{
				Title:    label,
				Subtitle: "error: " + err.Error(),
				URL:      fallbackURL,
				Icon:     icon,
				Badges:   []plugins.Badge{{Label: "error", Tone: "bad"}},
			},
		}
	}

	isPR := iss.PullRequest != nil
	kind := "issue"
	if isPR {
		kind = "PR"
	}

	// Answered + last-interaction time. With no comments the last interaction is
	// the creation; otherwise it's the most recent comment, which also tells us
	// whether someone other than the author has replied.
	answered := false
	lastActivity := iss.CreatedAt
	activityWord := "opened"
	if iss.Comments > 0 {
		lastPage := (iss.Comments + 99) / 100
		var comments []ghComment
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100&page=%d", owner, repo, num, lastPage)
		if err := client.Get(ctx, path, &comments); err == nil && len(comments) > 0 {
			last := comments[len(comments)-1]
			lastActivity = last.CreatedAt
			activityWord = "last reply"
			answered = !strings.EqualFold(last.User.Login, iss.User.Login)
		}
	}

	badges := make([]plugins.Badge, 0, 2)
	if answered {
		badges = append(badges, plugins.Badge{Label: "answered", Tone: "ok"})
	} else {
		badges = append(badges, plugins.Badge{Label: "no reply", Tone: "warn"})
	}

	state := iss.State // open / closed
	// CI applies only to pull requests: resolve the head commit, then aggregate
	// its check runs. Failures here are non-fatal — the row still renders.
	if isPR {
		var pull ghPull
		if err := client.Get(ctx, fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, num), &pull); err == nil {
			if pull.Merged {
				state = "merged"
			}
			if pull.Head.SHA != "" {
				if ci, ok := client.CIBadge(ctx, owner, repo, pull.Head.SHA); ok {
					badges = append(badges, ci)
				}
			}
		}
	}

	ts := ""
	if !lastActivity.IsZero() {
		ts = lastActivity.Format(time.RFC3339)
	}
	return itemResult{
		answered: answered,
		item: listItem{
			Title:     iss.Title,
			Subtitle:  fmt.Sprintf("%s · %s · %s · %s", label, kind, state, activityWord),
			URL:       iss.HTMLURL,
			Timestamp: ts,
			Icon:      icon,
			Badges:    badges,
		},
	}
}

// parseRef parses a tracked-item reference into owner, repo and number. It
// accepts "owner/repo#123" and full GitHub issue/PR URLs
// (https://github.com/owner/repo/issues/123 or .../pull/123).
func parseRef(s string) (owner, repo string, number int, err error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	if s == "" {
		return "", "", 0, fmt.Errorf("empty reference")
	}

	if i := strings.Index(s, "github.com/"); i >= 0 {
		parts := strings.Split(s[i+len("github.com/"):], "/")
		if len(parts) < 4 || (parts[2] != "issues" && parts[2] != "pull") {
			return "", "", 0, fmt.Errorf("unrecognized GitHub issue/PR URL %q", s)
		}
		n, e := strconv.Atoi(parts[3])
		if e != nil || n <= 0 {
			return "", "", 0, fmt.Errorf("bad issue number in %q", s)
		}
		return parts[0], parts[1], n, nil
	}

	hash := strings.LastIndex(s, "#")
	if hash < 0 {
		return "", "", 0, fmt.Errorf("missing #number in %q (want owner/repo#123)", s)
	}
	n, e := strconv.Atoi(strings.TrimSpace(s[hash+1:]))
	if e != nil || n <= 0 {
		return "", "", 0, fmt.Errorf("bad issue number in %q", s)
	}
	owner, repo, err = plugins.NormalizeRepo(s[:hash])
	if err != nil {
		return "", "", 0, err
	}
	return owner, repo, n, nil
}
