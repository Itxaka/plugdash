// Package plugin defines the contract every plugdash plugin implements.
//
// A plugin is a self-contained unit that, given a user-supplied configuration,
// runs (typically fetching data from some external source) and returns a Result
// describing both the data and how the dashboard should visualize it.
package plugin

import (
	"context"
	"time"
)

// FieldType enumerates the kinds of configuration inputs a plugin can request.
type FieldType string

const (
	FieldString FieldType = "string"
	FieldNumber FieldType = "number"
	FieldBool   FieldType = "bool"
	// FieldList is a comma-or-newline separated list of strings, surfaced as a
	// textarea in the UI and parsed into []string.
	FieldList FieldType = "list"
	// FieldSelect is a single choice from a fixed set, surfaced as a dropdown.
	// The choices are given in ConfigField.Options.
	FieldSelect FieldType = "select"
)

// SelectOption is one choice for a FieldSelect config field.
type SelectOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ConfigField describes a single configuration input the plugin accepts. The
// server exposes these so the config UI can render an appropriate form without
// knowing anything about the plugin internals.
type ConfigField struct {
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Type        FieldType `json:"type"`
	Required    bool      `json:"required"`
	Placeholder string    `json:"placeholder,omitempty"`
	Help        string    `json:"help,omitempty"`
	Default     any       `json:"default,omitempty"`
	// Options lists the choices for a FieldSelect field; ignored otherwise.
	Options []SelectOption `json:"options,omitempty"`
}

// Visualization names the renderer the frontend should use for a Result's data.
// Plugins pick one; the frontend maps each to a widget.
type Visualization string

const (
	// VizList renders Data as a list of {title, subtitle, url, timestamp} items.
	VizList Visualization = "list"
	// VizTable renders Data as {columns:[], rows:[[]]}.
	VizTable Visualization = "table"
	// VizChecklist renders Data as {items:[{label, ok, detail}]} with pass/fail marks.
	VizChecklist Visualization = "checklist"
	// VizStat renders Data as {value, label, status} as a single big stat.
	VizStat Visualization = "stat"
	// VizTimeseries renders Data as a line chart:
	// {label, unit, total, points:[{t: "RFC3339-or-date", v: number}]}.
	// Points must be in ascending time order. Used for "value over time" widgets
	// (e.g. stars history) computed just-in-time from timestamped source data.
	VizTimeseries Visualization = "timeseries"
	// VizGauge renders Data as a progress bar with a percentage:
	// {label, value: number, max: number, unit?, status?: "ok"|"warn"|"error", detail?}.
	// Used for completion/utilization widgets (e.g. milestone progress).
	VizGauge Visualization = "gauge"
)

// Result is what a plugin returns from Run. Data must be JSON-serializable and
// match the shape expected by the chosen Visualization.
type Result struct {
	Visualization Visualization `json:"visualization"`
	Title         string        `json:"title,omitempty"`
	Data          any           `json:"data"`
}

// Config is the decoded per-tracker configuration handed to Run. It is a plain
// map so plugins can pull typed values via the helper methods below.
type Config map[string]any

// String returns the string value for key, or "" if missing.
func (c Config) String(key string) string {
	if v, ok := c[key].(string); ok {
		return v
	}
	return ""
}

// Int returns the int value for key, coercing from JSON's float64, or 0.
func (c Config) Int(key string) int {
	switch v := c[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// Bool returns the bool value for key, or false.
func (c Config) Bool(key string) bool {
	v, _ := c[key].(bool)
	return v
}

// List returns the []string value for key. It accepts either a JSON array or a
// single string split on commas and newlines.
func (c Config) List(key string) []string {
	switch v := c[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		return splitList(v)
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	cur := ""
	flush := func() {
		t := trimSpace(cur)
		if t != "" {
			out = append(out, t)
		}
		cur = ""
	}
	for _, r := range s {
		if r == ',' || r == '\n' || r == '\r' {
			flush()
			continue
		}
		cur += string(r)
	}
	flush()
	return out
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// Plugin is implemented by every data source. Implementations must be safe for
// concurrent use: Run may be invoked for multiple trackers at once.
type Plugin interface {
	// ID is the stable machine identifier (e.g. "github-releases").
	ID() string
	// Name is the human-friendly label shown in the UI.
	Name() string
	// Description is a one-line explanation of what the plugin tracks.
	Description() string
	// ConfigSchema lists the configuration fields the plugin accepts.
	ConfigSchema() []ConfigField
	// RefreshInterval is the minimum time the dashboard should wait between
	// automatic re-runs of this plugin. It lets a plugin tell the dashboard how
	// fresh its data needs to be, so cheap/volatile sources (health checks) can
	// poll often while expensive/slow-moving ones (releases, star history) avoid
	// hammering external APIs. Users can always force an immediate refresh from
	// the widget. The value is advisory but the dashboard honors it as a floor.
	RefreshInterval() time.Duration
	// Run executes the plugin against cfg and returns a Result.
	Run(ctx context.Context, cfg Config) (Result, error)
}
