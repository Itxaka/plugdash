// Package fileversion implements a built-in plugin that tracks the value of a
// variable in a file on a GitHub repo branch — e.g. a pinned dependency version
// or the `go` directive in a go.mod. It reads the raw file just-in-time and
// extracts the value, rendering it as a single stat.
package fileversion

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// rawBaseURL is GitHub's raw content host. A var so tests can point it at a stub.
var rawBaseURL = "https://raw.githubusercontent.com"

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Plugin reads a value out of a file on a repo branch.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "file-version" }
func (p *Plugin) Name() string { return "File Value Watcher" }
func (p *Plugin) Description() string {
	return "Track a variable's value in a file on a GitHub repo branch (e.g. a pinned dependency)."
}

// RefreshInterval defaults to hourly: files change rarely.
func (p *Plugin) RefreshInterval() time.Duration { return time.Hour }

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
			Key:         "path",
			Label:       "File path",
			Type:        plugin.FieldString,
			Required:    true,
			Placeholder: "go.mod",
			Help:        "Path to the file within the repo.",
		},
		{
			Key:         "key",
			Label:       "Variable name",
			Type:        plugin.FieldString,
			Required:    true,
			Placeholder: "go",
			Help:        "Name to the left of a `=` / `:` (or whitespace, e.g. the go.mod `go` directive) whose value is reported.",
		},
	}
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
	path := strings.TrimLeft(strings.TrimSpace(cfg.String("path")), "/")
	key := strings.TrimSpace(cfg.String("key"))
	if path == "" || key == "" {
		return plugin.Result{}, fmt.Errorf("path and key are required")
	}

	url := fmt.Sprintf("%s/%s/%s/%s/%s", rawBaseURL, owner, name, ref, path)
	label := fmt.Sprintf("%s/%s@%s · %s · %s", owner, name, ref, path, key)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return plugin.Result{}, err
	}
	req.Header.Set("User-Agent", "plugdash-fileversion")

	log := plugin.LoggerFrom(ctx)
	log.Debug("fileversion fetch", "url", url)
	resp, err := httpClient.Do(req)
	if err != nil {
		return plugin.Result{}, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	log.Debug("fileversion response", "url", url, "status", resp.StatusCode, "bytes", len(body))
	if resp.StatusCode != http.StatusOK {
		return plugin.Result{}, fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}

	value, found := extractValue(string(body), key)
	status := "ok"
	if !found {
		value = "not found"
		status = "error"
	}
	return plugin.Result{
		Visualization: plugin.VizStat,
		Title:         label,
		Data: map[string]any{
			"value":  value,
			"label":  label,
			"status": status,
		},
	}, nil
}

// extractValue finds the first line `key = value`, `key: value`, or whitespace-
// delimited `key value` (e.g. the `go 1.22` directive in a go.mod) and returns
// the trimmed, unquoted value.
func extractValue(body, key string) (string, bool) {
	k := regexp.QuoteMeta(key)
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\s*` + k + `\s*[:=]\s*(.+?)\s*$`),
		regexp.MustCompile(`(?m)^\s*` + k + `\s+(.+?)\s*$`),
	} {
		if m := re.FindStringSubmatch(body); m != nil {
			v := strings.TrimSpace(m[1])
			v = strings.TrimRight(v, ", ") // trailing commas / spaces
			v = strings.Trim(v, `"'`)
			return v, true
		}
	}
	return "", false
}
