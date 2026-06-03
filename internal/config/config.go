// Package config loads declarative plugdash configuration ("config-as-code")
// from a YAML file and reconciles it into the store.
//
// The model is hybrid: trackers defined in the file are reconciled on startup
// and marked source="file" (read-only in the UI), while users may still add
// their own ad-hoc trackers through the UI (source="db"). Dropping a tracker
// from the file removes it on the next load; user trackers are never touched.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"plugdash/internal/store"
)

// Config is the parsed contents of a plugdash config file.
type Config struct {
	Settings Settings      `yaml:"settings"`
	Trackers []TrackerSpec `yaml:"trackers"`
}

// Settings holds optional deployment-wide knobs. They are applied at startup
// but not persisted into the DB settings row, so the UI stays authoritative for
// anything the user changes there.
type Settings struct {
	// GitHubToken, if set, authenticates all GitHub plugins (exported as
	// GITHUB_TOKEN when that env var isn't already set).
	GitHubToken string `yaml:"github_token"`
	// Debug enables verbose logging when true.
	Debug bool `yaml:"debug"`
}

// TrackerSpec is one declarative tracker entry.
type TrackerSpec struct {
	// Key is the tracker's stable identity within the file. It is what reconcile
	// matches on, so changing it renames (recreates) the tracker. If omitted, it
	// is derived from Name (slugified).
	Key string `yaml:"key"`
	// Plugin is the plugin ID to run.
	Plugin string `yaml:"plugin"`
	// Name is the display title.
	Name string `yaml:"name"`
	// RefreshIntervalSeconds overrides the plugin's default cadence (0 = default).
	RefreshIntervalSeconds int `yaml:"refresh_interval_seconds"`
	// Config is the plugin configuration map.
	Config map[string]any `yaml:"config"`
}

// Load reads and parses the config file at path. A missing file is an error;
// callers that treat config as optional should check existence first or use
// LoadIfPresent.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return Parse(raw)
}

// Parse decodes config from YAML bytes and validates it.
func Parse(raw []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // reject unknown keys so typos surface loudly
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return &c, nil
}

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

// normalize fills derived fields and validates required ones. It also rejects
// duplicate keys, since two trackers sharing a key would fight over one row.
func (c *Config) normalize() error {
	seen := map[string]int{}
	for i := range c.Trackers {
		t := &c.Trackers[i]
		t.Plugin = strings.TrimSpace(t.Plugin)
		t.Name = strings.TrimSpace(t.Name)
		if t.Plugin == "" {
			return fmt.Errorf("trackers[%d]: plugin is required", i)
		}
		if t.Key == "" {
			t.Key = slugify(t.Name)
		}
		if t.Key == "" {
			return fmt.Errorf("trackers[%d] (%s): key or name is required", i, t.Plugin)
		}
		if t.Name == "" {
			t.Name = t.Key
		}
		if t.RefreshIntervalSeconds < 0 {
			t.RefreshIntervalSeconds = 0
		}
		if prev, dup := seen[t.Key]; dup {
			return fmt.Errorf("trackers[%d]: duplicate key %q (also trackers[%d])", i, t.Key, prev)
		}
		seen[t.Key] = i
	}
	return nil
}

// slugify turns a display name into a stable lowercase key.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugStrip.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// exportDoc is the YAML shape produced by Marshal: a bare trackers list, with
// no settings block so secrets (e.g. the GitHub token) never land in a dump.
type exportDoc struct {
	Trackers []TrackerSpec `yaml:"trackers"`
}

// Marshal serializes trackers into a plugdash config document (trackers only).
// It is the inverse of Parse: the output can be fed back via --config or the
// import endpoint. Each tracker gets a stable, unique key — its existing
// ConfigKey when present (file-sourced), otherwise a slug of its name, with the
// tracker id appended on collision so the result re-parses without duplicate
// keys.
func Marshal(trackers []*store.Tracker) ([]byte, error) {
	seen := map[string]bool{}
	specs := make([]TrackerSpec, 0, len(trackers))
	for _, t := range trackers {
		if t == nil {
			continue
		}
		key := t.ConfigKey
		if key == "" {
			key = slugify(t.Name)
		}
		if key == "" {
			key = fmt.Sprintf("tracker-%d", t.ID)
		}
		if seen[key] {
			key = fmt.Sprintf("%s-%d", key, t.ID)
		}
		seen[key] = true
		specs = append(specs, TrackerSpec{
			Key:                    key,
			Plugin:                 t.PluginID,
			Name:                   t.Name,
			RefreshIntervalSeconds: t.RefreshIntervalSeconds,
			Config:                 t.Config,
		})
	}
	return yaml.Marshal(exportDoc{Trackers: specs})
}

// FileTrackers maps the parsed tracker specs to store.FileTracker values for
// reconciliation.
func (c *Config) FileTrackers() []store.FileTracker {
	out := make([]store.FileTracker, 0, len(c.Trackers))
	for _, t := range c.Trackers {
		out = append(out, store.FileTracker{
			Key:                    t.Key,
			PluginID:               t.Plugin,
			Name:                   t.Name,
			Config:                 t.Config,
			RefreshIntervalSeconds: t.RefreshIntervalSeconds,
		})
	}
	return out
}
