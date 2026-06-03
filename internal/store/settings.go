package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Default auto-refresh values and bounds (in seconds).
const (
	DefaultAutoRefreshInterval = 60
	MinAutoRefreshInterval     = 5
	MaxAutoRefreshInterval     = 3600
)

// Settings holds dashboard-wide preferences persisted as a single JSON row.
type Settings struct {
	// AutoRefreshEnabled controls whether the dashboard re-runs all trackers
	// on a timer.
	AutoRefreshEnabled bool `json:"autorefresh_enabled"`
	// AutoRefreshInterval is the timer period in seconds.
	AutoRefreshInterval int `json:"autorefresh_interval"`
	// DashboardOrder is the preferred display order of trackers on the
	// dashboard, by tracker ID. Trackers absent from this list are shown after
	// the ordered ones in their natural (creation) order.
	DashboardOrder []int64 `json:"dashboard_order"`
	// Debug enables verbose logging (each run, outbound queries, plugin stderr).
	Debug bool `json:"debug"`
	// GitHubToken, when set, is exported as GITHUB_TOKEN so every GitHub plugin
	// authenticates (much higher API rate limits) without per-tracker config.
	GitHubToken string `json:"github_token"`
	// UniformSizes forces every widget onto the default 1x1 tile, ignoring each
	// plugin's preferred size. Off by default (the dashboard honors sizes).
	UniformSizes bool `json:"uniform_sizes"`
}

// DefaultSettings returns the settings used before a user has saved any.
// Live updates default on: with the server-side engine, viewing the dashboard
// is what drives (and gates) execution.
func DefaultSettings() Settings {
	return Settings{
		AutoRefreshEnabled:  true,
		AutoRefreshInterval: DefaultAutoRefreshInterval,
	}
}

// normalize clamps out-of-range or zero values to sane defaults/bounds.
func (s *Settings) normalize() {
	if s.AutoRefreshInterval == 0 {
		s.AutoRefreshInterval = DefaultAutoRefreshInterval
	}
	if s.AutoRefreshInterval < MinAutoRefreshInterval {
		s.AutoRefreshInterval = MinAutoRefreshInterval
	}
	if s.AutoRefreshInterval > MaxAutoRefreshInterval {
		s.AutoRefreshInterval = MaxAutoRefreshInterval
	}
}

// GetSettings returns the saved settings, or DefaultSettings() if none have
// been stored yet.
func (s *Store) GetSettings() (Settings, error) {
	var raw string
	err := s.db.QueryRow(`SELECT data FROM settings WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("get settings: %w", err)
	}
	out := DefaultSettings()
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return Settings{}, fmt.Errorf("unmarshal settings: %w", err)
	}
	out.normalize()
	return out, nil
}

// SaveSettings upserts the single settings row and returns the normalized
// values that were stored.
func (s *Store) SaveSettings(in Settings) (Settings, error) {
	in.normalize()
	raw, err := json.Marshal(in)
	if err != nil {
		return Settings{}, fmt.Errorf("marshal settings: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?)
		 ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		string(raw),
	)
	if err != nil {
		return Settings{}, fmt.Errorf("save settings: %w", err)
	}
	return in, nil
}
