// Package dockerimage implements a plugin that checks whether Docker images
// exist in a registry for a set of tags and (optionally) architectures,
// rendering the outcome as a pass/fail checklist.
package dockerimage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// Plugin checks image/tag/arch existence against a Docker Registry v2 endpoint.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "docker-image" }
func (p *Plugin) Name() string { return "Docker Image Check" }
func (p *Plugin) Description() string {
	return "Check whether Docker images exist for given tags (manual or a repo's latest release) and architectures."
}

// RefreshInterval defaults to 24h: published images rarely change once tagged.
func (p *Plugin) RefreshInterval() time.Duration { return 24 * time.Hour }

func (p *Plugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:         "image",
			Label:       "Image",
			Type:        plugin.FieldString,
			Required:    true,
			Placeholder: "ghcr.io/org/repo or nginx",
			Help:        "Image ref without a tag. Docker Hub and ghcr.io supported.",
		},
		{
			Key:         "tags",
			Label:       "Tags",
			Type:        plugin.FieldList,
			Placeholder: "v1.0.0\nlatest",
			Help:        "Tags to check, one per line. Optional if a tag source repo is set.",
		},
		{
			Key:         "tag_source",
			Label:       "Tag from GitHub repo",
			Type:        plugin.FieldString,
			Placeholder: "owner/repo",
			Help:        "Also check this GitHub repo's latest stable release tag. Tries both vX.Y.Z and X.Y.Z.",
		},
		{
			Key:         "arches",
			Label:       "Architectures",
			Type:        plugin.FieldList,
			Placeholder: "amd64\narm64",
			Help:        "Leave empty to only check tag existence.",
		},
		{
			Key:   "token",
			Label: "Registry token",
			Type:  plugin.FieldString,
			Help:  "Bearer token for private registries.",
		},
		{
			Key:   "github_token",
			Label: "GitHub token",
			Type:  plugin.FieldString,
			Help:  "Used only to resolve the tag source repo. Falls back to GITHUB_TOKEN.",
		},
	}
}

// checklistItem is one row of the checklist visualization.
type checklistItem struct {
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	URL    string `json:"url,omitempty"`
}

// tagSpec is one thing to check: a display label plus the tag string(s) to try
// in the registry, in order. Manual tags have a single candidate; a tag derived
// from a GitHub release has two (the vX.Y.Z form and the v-stripped form).
type tagSpec struct {
	label      string
	candidates []string
}

// Run checks every configured tag (and arch, when given) and builds a checklist
// Result. Tags come from the manual list and/or the latest stable release of a
// GitHub repo. A registry/transport error is returned as a plugin error; a
// simple "tag not found" (404) is a valid, non-error checklist outcome.
func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	image := strings.TrimSpace(cfg.String("image"))
	if err := plugins.ValidateImage(image); err != nil {
		return plugin.Result{}, err
	}
	tags := cfg.List("tags")
	tagSource := strings.TrimSpace(cfg.String("tag_source"))
	if len(tags) == 0 && tagSource == "" {
		return plugin.Result{}, fmt.Errorf("provide at least one tag or a tag source repo")
	}
	arches := cfg.List("arches")
	token := strings.TrimSpace(cfg.String("token"))

	specs := make([]tagSpec, 0, len(tags)+1)
	for _, t := range tags {
		specs = append(specs, tagSpec{label: t, candidates: []string{t}})
	}

	// Derive a tag from the latest stable release of the source repo, if set.
	if tagSource != "" {
		owner, repo, err := plugins.NormalizeRepo(tagSource)
		if err != nil {
			return plugin.Result{}, err
		}
		gh := plugins.NewGHClient(cfg.String("github_token"))
		rel, err := gh.ReleaseByTag(ctx, owner, repo, "latest")
		if err != nil {
			return plugin.Result{}, fmt.Errorf("resolve latest release of %s/%s: %w", owner, repo, err)
		}
		tag := strings.TrimSpace(rel.TagName)
		if tag == "" {
			return plugin.Result{}, fmt.Errorf("latest release of %s/%s has no tag", owner, repo)
		}
		candidates := []string{tag}
		if stripped := strings.TrimPrefix(tag, "v"); stripped != tag && stripped != "" {
			candidates = append(candidates, stripped)
		}
		specs = append(specs, tagSpec{
			label:      fmt.Sprintf("%s (latest of %s/%s)", tag, owner, repo),
			candidates: candidates,
		})
	}

	var items []checklistItem
	for _, sp := range specs {
		matched, exists, platforms, err := resolveTag(ctx, image, sp.candidates, token)
		if err != nil {
			return plugin.Result{}, err
		}

		if len(arches) == 0 {
			detail := "not found"
			if exists {
				detail = "found: " + matched
			}
			items = append(items, checklistItem{Label: sp.label, OK: exists, Detail: detail})
			continue
		}

		for _, arch := range arches {
			ok, detail := evalArch(exists, platforms, arch, matched)
			items = append(items, checklistItem{
				Label:  fmt.Sprintf("%s (%s)", sp.label, arch),
				OK:     ok,
				Detail: detail,
			})
		}
	}

	present := 0
	allOK := true
	for _, it := range items {
		if it.OK {
			present++
		} else {
			allOK = false
		}
	}

	return plugin.Result{
		Visualization: plugin.VizChecklist,
		Title:         fmt.Sprintf("%s — %d/%d present", image, present, len(items)),
		Data: map[string]any{
			"items":  items,
			"all_ok": allOK,
		},
	}, nil
}

// resolveTag tries each candidate tag against the registry in order, returning
// the first that exists along with its manifest platforms. A transport error
// (anything other than a clean 404) aborts and is returned to the caller.
func resolveTag(ctx context.Context, image string, candidates []string, token string) (matched string, exists bool, platforms []plugins.Platform, err error) {
	for _, c := range candidates {
		ex, pf, e := plugins.CheckManifest(ctx, image, c, token)
		if e != nil {
			return "", false, nil, fmt.Errorf("check %s:%s: %w", image, c, e)
		}
		if ex {
			return c, true, pf, nil
		}
	}
	return "", false, nil, nil
}

// evalArch decides whether a tag satisfies a requested architecture and returns
// a human-readable detail string. matched is the tag string that actually
// resolved in the registry (which may differ from the display label when a
// v-stripped variant matched).
//
//   - tag missing                              → false, "missing tag"
//   - tag present, single-arch (no platforms)  → true,  "tag <matched> present, single-arch (unverified)"
//   - tag present, arch in platform list       → true,  "found: <matched>"
//   - tag present, arch absent from list        → false, "tag <matched> present, arch not in manifest list"
func evalArch(exists bool, platforms []plugins.Platform, arch, matched string) (bool, string) {
	if !exists {
		return false, "missing tag"
	}
	if len(platforms) == 0 {
		return true, fmt.Sprintf("tag %s present, single-arch (unverified)", matched)
	}
	for _, pf := range platforms {
		if strings.EqualFold(pf.Architecture, arch) {
			return true, "found: " + matched
		}
	}
	return false, fmt.Sprintf("tag %s present, arch not in manifest list", matched)
}
