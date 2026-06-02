// Package githubrate implements a plugdash plugin that plots a GitHub
// repository's activity as PER-PERIOD COUNTS over time — how many commits,
// issues, PRs or stars happen per day, week or month. Unlike the cumulative
// github-activity plugin, each point is the count within its bucket, with quiet
// periods filled in as zero. Everything is computed just-in-time from the
// timestamps GitHub attaches to each item; nothing is persisted between runs.
package githubrate

import (
	"context"
	"fmt"
	"sort"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

const (
	defaultMaxPages = 20
	maxPoints       = 365
)

// Plugin renders a repository's per-period activity counts as a line chart.
type Plugin struct{}

// New constructs the plugin.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "github-activity-rate" }
func (p *Plugin) Name() string { return "GitHub Activity Rate" }
func (p *Plugin) Description() string {
	return "Plot how many commits / issues / PRs / stars happen per day, week or month."
}

// periodOptions are the bucketing granularities offered by this plugin.
var periodOptions = []plugin.SelectOption{
	{Value: "day", Label: "Day"},
	{Value: "week", Label: "Week"},
	{Value: "month", Label: "Month"},
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
			Default: "commits",
			Help:    "What to count per period.",
			Options: plugins.ActivityMetricOptions,
		},
		{
			Key:     "period",
			Label:   "Period",
			Type:    plugin.FieldSelect,
			Default: "week",
			Help:    "Bucket size for the counts.",
			Options: periodOptions,
		},
		{Key: "token", Label: "GitHub token", Type: plugin.FieldString},
		{
			Key:     "max_pages",
			Label:   "Max pages",
			Type:    plugin.FieldNumber,
			Default: defaultMaxPages,
			Help:    "Pages of 100 items to fetch; caps the window and API usage.",
		},
	}
}

// RefreshInterval keeps re-runs gentle on the API: activity history moves slowly.
func (p *Plugin) RefreshInterval() time.Duration { return 6 * time.Hour }

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

// Run fetches the chosen metric's items and builds the per-period count series.
func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	owner, name, err := plugins.NormalizeRepo(cfg.String("repo"))
	if err != nil {
		return plugin.Result{}, err
	}

	metricKey := cfg.String("metric")
	if metricKey == "" {
		metricKey = "commits"
	}
	metricLabel := plugins.ActivityMetricLabel(metricKey)
	if metricLabel == "" {
		return plugin.Result{}, fmt.Errorf("unknown metric %q", metricKey)
	}

	period := cfg.String("period")
	if period == "" {
		period = "week"
	}
	switch period {
	case "day", "week", "month":
	default:
		return plugin.Result{}, fmt.Errorf("unknown period %q", period)
	}

	maxPages := cfg.Int("max_pages")
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}

	times, err := plugins.FetchActivityTimestamps(ctx, owner, name, metricKey, cfg.String("token"), maxPages)
	if err != nil {
		return plugin.Result{}, err
	}

	label := metricLabel + " per " + period
	total := len(times)
	return plugin.Result{
		Visualization: plugin.VizTimeseries,
		Title:         fmt.Sprintf("%s/%s — %s", owner, name, label),
		Data: timeseriesData{
			Label:  label,
			Unit:   "",
			Total:  float64(total),
			Points: buildSeries(times, period),
		},
	}, nil
}

// buildSeries buckets timestamps into per-period counts in ascending time order.
// Gaps between the first and last bucket are filled with zero-count points so
// quiet periods render as 0. The result is downsampled to at most maxPoints.
func buildSeries(times []time.Time, period string) []point {
	if len(times) == 0 {
		return []point{}
	}

	counts := make(map[string]int)
	for _, t := range times {
		start := bucketStart(t, period)
		counts[start.Format("2006-01-02")]++
	}

	// Determine the bucket span.
	first := bucketStart(times[0], period)
	last := first
	for _, t := range times {
		bs := bucketStart(t, period)
		if bs.Before(first) {
			first = bs
		}
		if bs.After(last) {
			last = bs
		}
	}

	// Emit one point per bucket from first to last, filling gaps with 0.
	var full []point
	for cur := first; !cur.After(last); cur = nextBucket(cur, period) {
		key := cur.Format("2006-01-02")
		full = append(full, point{T: key, V: float64(counts[key])})
	}

	// full is already ascending by construction; sort defensively.
	sort.Slice(full, func(i, j int) bool { return full[i].T < full[j].T })

	if len(full) <= maxPoints {
		return full
	}
	return downsample(full, maxPoints)
}

// bucketStart returns the UTC start date of the bucket containing t for the
// given period.
func bucketStart(t time.Time, period string) time.Time {
	u := t.UTC()
	y, m, d := u.Date()
	switch period {
	case "month":
		return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	case "week":
		day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		// Go: Sunday=0..Saturday=6. Map to Monday-based offset (Mon=0..Sun=6).
		offset := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -offset)
	default: // day
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
}

// nextBucket advances a bucket-start date to the next bucket's start.
func nextBucket(t time.Time, period string) time.Time {
	switch period {
	case "month":
		return t.AddDate(0, 1, 0)
	case "week":
		return t.AddDate(0, 0, 7)
	default: // day
		return t.AddDate(0, 0, 1)
	}
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
