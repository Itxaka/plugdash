// Package githubstars implements a plugdash plugin that plots a GitHub
// repository's activity over time — stars, commits, issues or pull requests —
// as a CUMULATIVE line chart. Every series is computed just-in-time from the
// timestamps GitHub attaches to each item; nothing is persisted between runs.
package githubstars

import (
	"context"
	"fmt"
	"sort"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

const (
	defaultMaxPages = 30
	maxPoints       = 365
)

// Plugin renders a repository's cumulative activity-over-time as a line chart.
type Plugin struct{}

// New constructs the plugin.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "github-activity" }
func (p *Plugin) Name() string { return "GitHub Activity Over Time" }
func (p *Plugin) Description() string {
	return "Plot a repository's cumulative stars, commits, issues or PRs over time (computed live)."
}

// ConfigSchema lists the configuration fields.
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
			Key:     "metric",
			Label:   "Metric",
			Type:    plugin.FieldSelect,
			Default: "stars",
			Help:    "What to plot cumulatively over time.",
			Options: plugins.ActivityMetricOptions,
		},
		{Key: "token", Label: "GitHub token", Type: plugin.FieldString},
		{
			Key:     "max_pages",
			Label:   "Max pages",
			Type:    plugin.FieldNumber,
			Default: defaultMaxPages,
			Help:    "Pages of 100 items to fetch; caps history depth and API usage.",
		},
	}
}

// RefreshInterval keeps re-runs gentle on the API: activity history moves slowly.
func (p *Plugin) RefreshInterval() time.Duration { return 24 * time.Hour }

type point struct {
	T string  `json:"t"`
	V float64 `json:"v"`
}

type timeseriesData struct {
	Label  string  `json:"label"`
	Unit   string  `json:"unit"`
	Total  float64 `json:"total"`
	Points []point `json:"points"`
}

// Run fetches the chosen metric's items and builds the cumulative timeseries.
func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	owner, name, err := plugins.NormalizeRepo(cfg.String("repo"))
	if err != nil {
		return plugin.Result{}, err
	}

	metricKey := cfg.String("metric")
	if metricKey == "" {
		metricKey = "stars"
	}
	label := plugins.ActivityMetricLabel(metricKey)
	if label == "" {
		return plugin.Result{}, fmt.Errorf("unknown metric %q", metricKey)
	}

	maxPages := cfg.Int("max_pages")
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}

	times, err := plugins.FetchActivityTimestamps(ctx, owner, name, metricKey, cfg.String("token"), maxPages)
	if err != nil {
		return plugin.Result{}, err
	}

	total := len(times)
	return plugin.Result{
		Visualization: plugin.VizTimeseries,
		Title:         fmt.Sprintf("%s/%s — %s over time (%d)", owner, name, label, total),
		Data: timeseriesData{
			Label:  label,
			Total:  float64(total),
			Points: buildSeries(times),
		},
	}, nil
}

// buildSeries turns item timestamps into a cumulative daily series in ascending
// time order, downsampled to at most maxPoints points (keeping first and last).
func buildSeries(times []time.Time) []point {
	if len(times) == 0 {
		return []point{}
	}
	sorted := make([]time.Time, len(times))
	copy(sorted, times)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Before(sorted[j]) })

	var full []point
	cumulative := 0
	var curDay string
	for _, t := range sorted {
		day := t.UTC().Format("2006-01-02")
		cumulative++
		if day != curDay {
			full = append(full, point{T: day, V: float64(cumulative)})
			curDay = day
		} else {
			full[len(full)-1].V = float64(cumulative)
		}
	}
	if len(full) <= maxPoints {
		return full
	}
	return downsample(full, maxPoints)
}

// downsample reduces pts to at most n roughly evenly-spaced points, always
// keeping the first and last.
func downsample(pts []point, n int) []point {
	if n < 2 || len(pts) <= n {
		return pts
	}
	out := make([]point, 0, n)
	last := len(pts) - 1
	for i := 0; i < n-1; i++ {
		out = append(out, pts[i*last/(n-1)])
	}
	return append(out, pts[last])
}
