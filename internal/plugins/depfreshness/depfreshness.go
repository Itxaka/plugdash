// Package depfreshness implements a plugin that reports how far behind a
// repository's direct dependencies are from their latest published versions.
// It reads a go.mod or package.json from a GitHub branch just-in-time, then
// queries the Go module proxy / npm registry for the latest versions. No
// authentication is required for public repositories.
package depfreshness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// Endpoints are vars so tests can point them at stubs.
var (
	rawBaseURL      = "https://raw.githubusercontent.com"
	goProxyBaseURL  = "https://proxy.golang.org"
	npmRegistryBase = "https://registry.npmjs.org"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

// maxDeps caps how many dependencies are checked (one upstream call each).
const maxDeps = 50

// Plugin reports dependency freshness for a go.mod / package.json.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "dependency-freshness" }
func (p *Plugin) Name() string { return "Dependency Freshness" }
func (p *Plugin) Description() string {
	return "Compare a repo's direct dependencies (go.mod / package.json) against their latest releases."
}

// RefreshInterval defaults to daily: dependency releases move slowly.
func (p *Plugin) RefreshInterval() time.Duration { return 24 * time.Hour }

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
			Key:         "ref",
			Label:       "Branch or tag",
			Type:        plugin.FieldString,
			Placeholder: "main",
			Help:        "Branch or tag to read from (default: main).",
		},
		{
			Key:         "file",
			Label:       "Manifest file",
			Type:        plugin.FieldString,
			Required:    true,
			Placeholder: "go.mod",
			Help:        "Path to a go.mod or package.json within the repo.",
		},
		{
			Key:     "count",
			Label:   "Max dependencies",
			Type:    plugin.FieldNumber,
			Default: 30,
			Help:    "How many dependencies to check (one upstream lookup each).",
		},
		{
			Key:     "include_dev",
			Label:   "Include devDependencies",
			Type:    plugin.FieldBool,
			Default: true,
			Help:    "npm only: also check devDependencies.",
		},
	}
}

type badge struct {
	Label string `json:"label"`
	Tone  string `json:"tone"`
}

type listItem struct {
	Title    string  `json:"title"`
	Subtitle string  `json:"subtitle"`
	URL      string  `json:"url,omitempty"`
	Badges   []badge `json:"badges,omitempty"`
}

// dep is one dependency to check: its name and the version currently pinned.
type dep struct {
	name    string
	current string
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	owner, name, err := plugins.NormalizeRepo(cfg.String("repo"))
	if err != nil {
		return plugin.Result{}, err
	}
	ref := strings.TrimSpace(cfg.String("ref"))
	if ref == "" {
		ref = "main"
	}
	file := strings.TrimLeft(strings.TrimSpace(cfg.String("file")), "/")
	if file == "" {
		return plugin.Result{}, fmt.Errorf("file is required (go.mod or package.json)")
	}
	count := cfg.Int("count")
	if count <= 0 {
		count = 30
	}
	if count > maxDeps {
		count = maxDeps
	}
	includeDev := true
	if _, ok := cfg["include_dev"]; ok {
		includeDev = cfg.Bool("include_dev")
	}

	body, err := fetchRaw(ctx, owner, name, ref, file)
	if err != nil {
		return plugin.Result{}, err
	}

	base := file
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}

	var (
		deps   []dep
		latest func(context.Context, string) (string, error)
		eco    string
	)
	switch base {
	case "go.mod":
		deps = parseGoMod(body)
		latest = goLatest
		eco = "Go"
	case "package.json":
		deps, err = parsePackageJSON(body, includeDev)
		if err != nil {
			return plugin.Result{}, err
		}
		latest = npmLatest
		eco = "npm"
	default:
		return plugin.Result{}, fmt.Errorf("unsupported manifest %q (want go.mod or package.json)", base)
	}

	if len(deps) > count {
		deps = deps[:count]
	}

	items := make([]listItem, 0, len(deps))
	outdated := 0
	for _, d := range deps {
		it, behind := evalDep(ctx, latest, d)
		items = append(items, it)
		if behind {
			outdated++
		}
	}

	return plugin.Result{
		Visualization: plugin.VizList,
		Title:         fmt.Sprintf("%s/%s · %s — %d of %d outdated", owner, name, eco, outdated, len(items)),
		Data:          map[string]any{"items": items},
	}, nil
}

// evalDep looks up a dependency's latest version and renders its row, returning
// whether it is behind.
func evalDep(ctx context.Context, latest func(context.Context, string) (string, error), d dep) (listItem, bool) {
	want, err := latest(ctx, d.name)
	if err != nil {
		return listItem{
			Title:    d.name,
			Subtitle: "current " + d.current + " · latest unknown",
			Badges:   []badge{{Label: "lookup failed", Tone: "neutral"}},
		}, false
	}
	cur := normVersion(d.current)
	lat := normVersion(want)
	switch versionStatus(cur, lat) {
	case statusCurrent:
		return listItem{
			Title:    d.name,
			Subtitle: "current " + cur + " · latest " + lat,
			Badges:   []badge{{Label: "up to date", Tone: "ok"}},
		}, false
	case statusMajor:
		return listItem{
			Title:    d.name,
			Subtitle: "current " + cur + " · latest " + lat,
			Badges:   []badge{{Label: "major behind", Tone: "bad"}},
		}, true
	default:
		return listItem{
			Title:    d.name,
			Subtitle: "current " + cur + " · latest " + lat,
			Badges:   []badge{{Label: "outdated", Tone: "warn"}},
		}, true
	}
}

const (
	statusCurrent = iota
	statusMinor
	statusMajor
)

// versionStatus compares two normalized versions: equal -> current, differing
// major -> major, otherwise minor/patch behind.
func versionStatus(cur, lat string) int {
	if cur == lat || lat == "" {
		return statusCurrent
	}
	cm, co := majorOf(cur)
	lm, lo := majorOf(lat)
	if co && lo && cm != lm {
		return statusMajor
	}
	return statusMinor
}

func majorOf(v string) (int, bool) {
	if v == "" {
		return 0, false
	}
	part := v
	if i := strings.IndexByte(v, '.'); i >= 0 {
		part = v[:i]
	}
	n, err := strconv.Atoi(part)
	if err != nil {
		return 0, false
	}
	return n, true
}

// normVersion strips a leading v and common range operators for comparison.
func normVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimLeft(v, "^~>=< vV")
	// Drop a Go pseudo-version / build suffix tail beyond the semver core is left
	// as-is; equality still works for the common pinned case.
	return v
}

// parseGoMod extracts direct require entries (skipping // indirect) as deps.
func parseGoMod(body string) []dep {
	var deps []dep
	inBlock := false
	for raw := range strings.SplitSeq(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "require ("):
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		}
		var spec string
		if inBlock {
			spec = line
		} else if rest, ok := strings.CutPrefix(line, "require "); ok {
			spec = rest
		} else {
			continue
		}
		if strings.Contains(spec, "// indirect") {
			continue
		}
		if i := strings.Index(spec, "//"); i >= 0 {
			spec = spec[:i]
		}
		fields := strings.Fields(spec)
		if len(fields) < 2 {
			continue
		}
		deps = append(deps, dep{name: fields[0], current: fields[1]})
	}
	return deps
}

// parsePackageJSON extracts dependencies (and optionally devDependencies),
// sorted by name for deterministic output.
func parsePackageJSON(body string, includeDev bool) ([]dep, error) {
	var pj struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal([]byte(body), &pj); err != nil {
		return nil, fmt.Errorf("parse package.json: %w", err)
	}
	merged := map[string]string{}
	maps.Copy(merged, pj.Dependencies)
	if includeDev {
		for k, v := range pj.DevDependencies {
			if _, ok := merged[k]; !ok {
				merged[k] = v
			}
		}
	}
	names := make([]string, 0, len(merged))
	for k := range merged {
		names = append(names, k)
	}
	sort.Strings(names)
	deps := make([]dep, 0, len(names))
	for _, n := range names {
		deps = append(deps, dep{name: n, current: merged[n]})
	}
	return deps, nil
}

func fetchRaw(ctx context.Context, owner, name, ref, file string) (string, error) {
	url := fmt.Sprintf("%s/%s/%s/%s/%s", rawBaseURL, owner, name, ref, file)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "plugdash-depfreshness")
	log := plugin.LoggerFrom(ctx)
	log.Debug("depfreshness fetch", "url", url)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	return string(b), nil
}

// goLatest queries the Go module proxy for a module's latest version.
func goLatest(ctx context.Context, module string) (string, error) {
	var out struct {
		Version string `json:"Version"`
	}
	url := goProxyBaseURL + "/" + escapeModule(module) + "/@latest"
	if err := getJSON(ctx, url, &out); err != nil {
		return "", err
	}
	return out.Version, nil
}

// npmLatest queries the npm registry for a package's latest version.
func npmLatest(ctx context.Context, pkg string) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	url := npmRegistryBase + "/" + pkg + "/latest"
	if err := getJSON(ctx, url, &out); err != nil {
		return "", err
	}
	return out.Version, nil
}

// escapeModule applies the Go module proxy case-encoding: each uppercase letter
// becomes "!" + its lowercase form (so paths are case-insensitive on disk).
func escapeModule(m string) string {
	var b strings.Builder
	for _, r := range m {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(b, out)
}
