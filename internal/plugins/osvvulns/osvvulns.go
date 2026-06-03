// Package osvvulns implements a plugin that checks a package version against the
// free OSV.dev vulnerability database and lists any known advisories. No
// authentication is required.
package osvvulns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"plugdash/internal/plugin"
)

// osvBaseURL is the OSV.dev API root. A var so tests can point it at a stub.
var osvBaseURL = "https://api.osv.dev"

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Plugin checks one package@version against OSV.dev.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "osv-vulns" }
func (p *Plugin) Name() string { return "Vulnerability Check" }
func (p *Plugin) Description() string {
	return "List known vulnerabilities for a package version from the OSV.dev database."
}

// RefreshInterval defaults to 6h: advisories are published continuously but a
// given package version doesn't need minute-level polling.
func (p *Plugin) RefreshInterval() time.Duration { return 6 * time.Hour }

// ecosystemOptions lists the common OSV ecosystems offered in the UI. OSV
// accepts more; the value is sent verbatim, so a custom one still works.
var ecosystemOptions = []plugin.SelectOption{
	{Value: "Go", Label: "Go"},
	{Value: "npm", Label: "npm"},
	{Value: "PyPI", Label: "PyPI"},
	{Value: "crates.io", Label: "crates.io (Rust)"},
	{Value: "Maven", Label: "Maven (Java)"},
	{Value: "NuGet", Label: "NuGet (.NET)"},
	{Value: "RubyGems", Label: "RubyGems"},
	{Value: "Packagist", Label: "Packagist (PHP)"},
	{Value: "Hex", Label: "Hex (Elixir)"},
	{Value: "Pub", Label: "Pub (Dart)"},
}

func (p *Plugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:      "ecosystem",
			Label:    "Ecosystem",
			Type:     plugin.FieldSelect,
			Default:  "Go",
			Options:  ecosystemOptions,
			Required: true,
		},
		{
			Key:         "package",
			Label:       "Package",
			Type:        plugin.FieldString,
			Required:    true,
			Placeholder: "github.com/foo/bar",
			Help:        "Package name as it appears in the ecosystem (e.g. a Go module path, or an npm package name).",
		},
		{
			Key:         "version",
			Label:       "Version",
			Type:        plugin.FieldString,
			Required:    true,
			Placeholder: "1.2.3",
			Help:        "Exact version to check (no leading v for Go modules unless that's the real tag).",
		},
	}
}

// badge is one tone-colored pill on a list item.
type badge struct {
	Label string `json:"label"`
	Tone  string `json:"tone"`
}

type listItem struct {
	Title    string  `json:"title"`
	Subtitle string  `json:"subtitle"`
	URL      string  `json:"url"`
	Badges   []badge `json:"badges,omitempty"`
}

// osvQuery is the OSV.dev /v1/query request body.
type osvQuery struct {
	Version string `json:"version"`
	Package struct {
		Name      string `json:"name"`
		Ecosystem string `json:"ecosystem"`
	} `json:"package"`
}

// osvVuln is the subset of an OSV vulnerability record this plugin renders.
type osvVuln struct {
	ID       string   `json:"id"`
	Summary  string   `json:"summary"`
	Details  string   `json:"details"`
	Aliases  []string `json:"aliases"`
	Severity []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	} `json:"severity"`
}

type osvResp struct {
	Vulns []osvVuln `json:"vulns"`
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	pkg := strings.TrimSpace(cfg.String("package"))
	version := strings.TrimSpace(cfg.String("version"))
	ecosystem := strings.TrimSpace(cfg.String("ecosystem"))
	if ecosystem == "" {
		ecosystem = "Go"
	}
	if pkg == "" || version == "" {
		return plugin.Result{}, fmt.Errorf("package and version are required")
	}

	var q osvQuery
	q.Version = version
	q.Package.Name = pkg
	q.Package.Ecosystem = ecosystem

	resp, err := queryOSV(ctx, q)
	if err != nil {
		return plugin.Result{}, err
	}

	ref := fmt.Sprintf("%s@%s", pkg, version)
	if len(resp.Vulns) == 0 {
		return plugin.Result{
			Visualization: plugin.VizList,
			Title:         ref + " — no known vulnerabilities",
			Data: map[string]any{"items": []listItem{{
				Title:    "No known vulnerabilities",
				Subtitle: ref + " (" + ecosystem + ")",
				Badges:   []badge{{Label: "clean", Tone: "ok"}},
			}}},
		}, nil
	}

	items := make([]listItem, 0, len(resp.Vulns))
	for _, v := range resp.Vulns {
		items = append(items, vulnItem(v))
	}

	return plugin.Result{
		Visualization: plugin.VizList,
		Title:         fmt.Sprintf("%s — %d known vulnerabilit%s", ref, len(items), plural(len(items))),
		Data:          map[string]any{"items": items},
	}, nil
}

// vulnItem renders one advisory. The badge prefers a CVE alias, then the OSV id.
func vulnItem(v osvVuln) listItem {
	summary := v.Summary
	if summary == "" {
		summary = firstLine(v.Details)
	}
	tag := "vuln"
	for _, a := range v.Aliases {
		if strings.HasPrefix(a, "CVE-") {
			tag = a
			break
		}
	}
	return listItem{
		Title:    v.ID,
		Subtitle: truncate(summary, 160),
		URL:      "https://osv.dev/vulnerability/" + v.ID,
		Badges:   []badge{{Label: tag, Tone: "bad"}},
	}
}

func queryOSV(ctx context.Context, q osvQuery) (osvResp, error) {
	body, err := json.Marshal(q)
	if err != nil {
		return osvResp{}, err
	}
	url := osvBaseURL + "/v1/query"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return osvResp{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	log := plugin.LoggerFrom(ctx)
	log.Debug("osv query", "url", url, "package", q.Package.Name, "version", q.Version, "ecosystem", q.Package.Ecosystem)
	resp, err := httpClient.Do(req)
	if err != nil {
		return osvResp{}, fmt.Errorf("osv request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	log.Debug("osv response", "status", resp.StatusCode, "bytes", len(raw))
	if resp.StatusCode != http.StatusOK {
		return osvResp{}, fmt.Errorf("osv %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(truncate(string(raw), 200)))
	}
	var out osvResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return osvResp{}, fmt.Errorf("decode osv response: %w", err)
	}
	return out, nil
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
