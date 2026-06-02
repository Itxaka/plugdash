// Package githubartifacts implements a plugin that checks whether a specific
// GitHub release contains an expected set of artifacts, rendering the result as
// a pass/fail checklist.
package githubartifacts

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// Plugin verifies expected artifacts exist on a release.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "github-release-artifacts" }
func (p *Plugin) Name() string { return "GitHub Release Artifacts" }
func (p *Plugin) Description() string {
	return "Check that a GitHub release contains the expected artifacts."
}

// RefreshInterval defaults to daily: a published release's assets are stable.
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
			Key:         "tag",
			Label:       "Release tag",
			Type:        plugin.FieldString,
			Placeholder: "v1.2.3 or latest",
			Help:        "Tag to check. Leave empty or use 'latest' for the most recent release.",
		},
		{
			Key:         "expected",
			Label:       "Expected artifacts",
			Type:        plugin.FieldList,
			Required:    true,
			Placeholder: "app-linux-amd64\napp-darwin-arm64\nchecksums.txt",
			Help:        "One artifact name per line. Supports * and ? glob wildcards.",
		},
		{
			Key:   "token",
			Label: "GitHub token (optional)",
			Type:  plugin.FieldString,
			Help:  "Personal access token to raise rate limits. Falls back to GITHUB_TOKEN env.",
		},
	}
}

// checkItem matches the frontend "checklist" visualization shape.
type checkItem struct {
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	owner, name, err := plugins.NormalizeRepo(cfg.String("repo"))
	if err != nil {
		return plugin.Result{}, err
	}
	expected := cfg.List("expected")
	if len(expected) == 0 {
		return plugin.Result{}, fmt.Errorf("at least one expected artifact is required")
	}
	tag := cfg.String("tag")

	client := plugins.NewGHClient(cfg.String("token"))
	rel, err := client.ReleaseByTag(ctx, owner, name, tag)
	if err != nil {
		return plugin.Result{}, err
	}

	// Build a lookup of the asset names actually present on the release.
	present := make([]string, len(rel.Assets))
	for i, a := range rel.Assets {
		present[i] = a.Name
	}

	items := make([]checkItem, 0, len(expected))
	missing := 0
	for _, want := range expected {
		match := firstMatch(want, present)
		if match == "" {
			missing++
			items = append(items, checkItem{
				Label:  want,
				OK:     false,
				Detail: "not found",
			})
			continue
		}
		items = append(items, checkItem{
			Label:  want,
			OK:     true,
			Detail: "found: " + match,
		})
	}

	tagShown := rel.TagName
	if tagShown == "" {
		tagShown = tag
	}
	title := fmt.Sprintf("%s/%s @ %s — %d/%d artifacts present",
		owner, name, tagShown, len(expected)-missing, len(expected))

	return plugin.Result{
		Visualization: plugin.VizChecklist,
		Title:         title,
		Data: map[string]any{
			"items":       items,
			"all_ok":      missing == 0,
			"tag":         tagShown,
			"release_url": rel.HTMLURL,
		},
	}, nil
}

// firstMatch returns the first present asset name matching the want pattern,
// which may contain * and ? glob wildcards. An exact (case-insensitive) match
// is preferred. Returns "" if nothing matches.
func firstMatch(want string, present []string) string {
	want = strings.TrimSpace(want)
	// Exact, case-insensitive match first.
	for _, name := range present {
		if strings.EqualFold(name, want) {
			return name
		}
	}
	// Glob match (path.Match treats the whole name as one segment, which is
	// what we want for flat artifact filenames).
	if strings.ContainsAny(want, "*?[") {
		for _, name := range present {
			if ok, _ := path.Match(want, name); ok {
				return name
			}
		}
	}
	return ""
}
