// Package githubactions implements a plugdash plugin that gives a birds-eye CI
// view across many GitHub repositories. For each configured repo it reports
// whether the latest commit on the default (or a chosen) branch is passing CI,
// as reported by the GitHub Actions / checks API.
package githubactions

import (
	"context"
	"fmt"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// Plugin watches CI status of the latest commit across many repositories.
type Plugin struct{}

// New returns a ready-to-use plugin instance.
func New() *Plugin { return &Plugin{} }

// ID is the stable machine identifier.
func (p *Plugin) ID() string { return "github-actions-status" }

// Name is the human-friendly label shown in the UI.
func (p *Plugin) Name() string { return "GitHub Actions Status" }

// Description is a one-line explanation of what the plugin tracks.
func (p *Plugin) Description() string {
	return "Watch CI status of the latest commit across many repositories."
}

// ConfigSchema lists the configuration fields the plugin accepts.
func (p *Plugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:         "repos",
			Label:       "Repositories",
			Type:        plugin.FieldList,
			Required:    true,
			Placeholder: "kairos-io/kairos\nkubernetes/kubernetes",
			Help:        "One owner/repo per line.",
		},
		{
			Key:         "branch",
			Label:       "Branch",
			Type:        plugin.FieldString,
			Required:    false,
			Placeholder: "leave empty for default branch",
		},
		{
			Key:      "token",
			Label:    "GitHub Token",
			Type:     plugin.FieldString,
			Required: false,
			Help:     "Optional. Falls back to the GITHUB_TOKEN environment variable.",
		},
	}
}

// RefreshInterval reports how long the dashboard should wait between re-runs.
func (p *Plugin) RefreshInterval() time.Duration { return 2 * time.Minute }

// checkItem mirrors a single entry in the checklist Data shape.
type checkItem struct {
	Label  string      `json:"label"`
	OK     bool        `json:"ok"`
	Detail string      `json:"detail"`
	URL    string      `json:"url,omitempty"`
	Links  []checkLink `json:"links,omitempty"`
}

// checkLink is a per-job link rendered as a pill in the UI. One per check run.
type checkLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
	OK    bool   `json:"ok"`
}

// maxLinks caps the number of per-job links per repo to avoid pathological
// explosions when a commit has an unusual number of check runs.
const maxLinks = 25

// repoMeta is the subset of the repository object we decode.
type repoMeta struct {
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

// checkRun is one entry from the check-runs API.
type checkRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
}

// checkRunsResp is the check-runs API envelope.
type checkRunsResp struct {
	TotalCount int        `json:"total_count"`
	CheckRuns  []checkRun `json:"check_runs"`
}

// Run executes the plugin against cfg and returns a checklist Result.
func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	repos := cfg.List("repos")
	if len(repos) == 0 {
		return plugin.Result{}, fmt.Errorf("no repositories configured")
	}

	client := plugins.NewGHClient(cfg.String("token"))
	branch := cfg.String("branch")

	items := make([]checkItem, 0, len(repos))
	for _, raw := range repos {
		items = append(items, evalRepo(ctx, client, raw, branch))
	}

	passing := 0
	allOK := true
	for _, it := range items {
		if it.OK {
			passing++
		} else {
			allOK = false
		}
	}

	data := map[string]any{
		"items":  items,
		"all_ok": allOK,
	}

	return plugin.Result{
		Visualization: plugin.VizChecklist,
		Title:         fmt.Sprintf("CI status — %d/%d passing", passing, len(items)),
		Data:          data,
	}, nil
}

// evalRepo resolves the ref and aggregates check-run state for one repo. It
// never returns an error: per-repo problems become a failing item so one bad
// repo cannot sink the whole run.
func evalRepo(ctx context.Context, client *plugins.GHClient, raw, branch string) checkItem {
	owner, name, err := plugins.NormalizeRepo(raw)
	if err != nil {
		return checkItem{Label: raw, OK: false, Detail: "invalid repo"}
	}
	label := owner + "/" + name

	ref := branch
	repoURL := fmt.Sprintf("https://github.com/%s/%s", owner, name)
	if ref == "" {
		var meta repoMeta
		if err := client.Get(ctx, fmt.Sprintf("/repos/%s/%s", owner, name), &meta); err != nil {
			return checkItem{Label: label, OK: false, Detail: "error: " + err.Error(), URL: repoURL}
		}
		ref = meta.DefaultBranch
		if meta.HTMLURL != "" {
			repoURL = meta.HTMLURL
		}
	}

	var runs checkRunsResp
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs", owner, name, ref)
	if err := client.Get(ctx, path, &runs); err != nil {
		return checkItem{Label: label, OK: false, Detail: "error: " + err.Error(), URL: repoURL}
	}

	return aggregate(label, repoURL, runs)
}

// aggregate turns a set of check runs into a single checklist item. The overall
// ok/detail/url stay the birds-eye pass/fail aggregation; links is purely
// additive, carrying one pill per check run for the UI to jump to a job.
func aggregate(label, repoURL string, runs checkRunsResp) checkItem {
	if runs.TotalCount == 0 {
		return checkItem{Label: label, OK: false, Detail: "no checks", URL: repoURL}
	}

	links := buildLinks(runs.CheckRuns)

	failConclusions := map[string]bool{
		"failure":         true,
		"timed_out":       true,
		"cancelled":       true,
		"action_required": true,
	}

	for _, r := range runs.CheckRuns {
		if failConclusions[r.Conclusion] {
			detail := "failing"
			if r.Name != "" {
				detail = "failing: " + r.Name
			}
			url := repoURL
			if r.HTMLURL != "" {
				url = r.HTMLURL
			}
			return checkItem{Label: label, OK: false, Detail: detail, URL: url, Links: links}
		}
	}

	for _, r := range runs.CheckRuns {
		if r.Status != "completed" {
			return checkItem{Label: label, OK: false, Detail: "running", URL: repoURL, Links: links}
		}
	}

	return checkItem{Label: label, OK: true, Detail: "passing", URL: repoURL, Links: links}
}

// buildLinks turns check runs into per-job links, one per run that has an
// html_url. The result is capped at maxLinks entries.
func buildLinks(runs []checkRun) []checkLink {
	links := make([]checkLink, 0, len(runs))
	for _, r := range runs {
		if r.HTMLURL == "" {
			continue
		}
		links = append(links, checkLink{
			Label: r.Name,
			URL:   r.HTMLURL,
			OK:    r.Conclusion == "success",
		})
		if len(links) >= maxLinks {
			break
		}
	}
	if len(links) == 0 {
		return nil
	}
	return links
}
