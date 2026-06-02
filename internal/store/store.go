// Package store persists tracker configurations in SQLite.
//
// A "tracker" is a saved instance of a plugin together with the configuration a
// user supplied for it. The dashboard runs each tracker to produce a widget.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no CGO required
)

// Tracker is a configured plugin instance.
type Tracker struct {
	ID       int64          `json:"id"`
	PluginID string         `json:"plugin_id"`
	Name     string         `json:"name"`
	Config   map[string]any `json:"config"`
	// RefreshIntervalSeconds overrides the plugin's default cadence for this
	// tracker. 0 means "use the plugin default".
	RefreshIntervalSeconds int       `json:"refresh_interval_seconds"`
	CreatedAt              time.Time `json:"created_at"`
	// Source is where the tracker came from: "db" for user-created (editable in
	// the UI) or "file" for declarative config (config-as-code; read-only in the
	// UI and reconciled from the config file on each load).
	Source string `json:"source"`
	// ConfigKey is the stable identity of a file-managed tracker within the
	// config file. Empty for db trackers. It lets reconcile update in place
	// (preserving ID and dashboard order) instead of recreating.
	ConfigKey string `json:"config_key,omitempty"`
}

// Tracker sources.
const (
	SourceDB   = "db"
	SourceFile = "file"
)

// FileTracker is one declarative tracker entry loaded from a config file. Key
// is its stable identity within the file (used to reconcile in place).
type FileTracker struct {
	Key                    string
	PluginID               string
	Name                   string
	Config                 map[string]any
	RefreshIntervalSeconds int
}

// Store wraps the database handle and provides tracker CRUD.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies the
// schema migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		return nil, fmt.Errorf("set pragmas: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS trackers (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	plugin_id  TEXT    NOT NULL,
	name       TEXT    NOT NULL,
	config     TEXT    NOT NULL DEFAULT '{}',
	refresh_interval_seconds INTEGER NOT NULL DEFAULT 0,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	source     TEXT    NOT NULL DEFAULT 'db',
	config_key TEXT    NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS settings (
	id   INTEGER PRIMARY KEY CHECK (id = 1),
	data TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS snapshots (
	tracker_id INTEGER PRIMARY KEY REFERENCES trackers(id) ON DELETE CASCADE,
	plugin_id  TEXT NOT NULL,
	name       TEXT NOT NULL,
	refresh_interval_seconds INTEGER NOT NULL DEFAULT 0,
	result     TEXT NOT NULL DEFAULT '',
	error      TEXT NOT NULL DEFAULT '',
	fetched_at TIMESTAMP NOT NULL
);`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Add columns introduced after the initial schema, for databases created by
	// an older version. ADD COLUMN is idempotent here because we check first.
	if err := s.ensureColumn("trackers", "refresh_interval_seconds",
		"ALTER TABLE trackers ADD COLUMN refresh_interval_seconds INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("trackers", "source",
		"ALTER TABLE trackers ADD COLUMN source TEXT NOT NULL DEFAULT 'db'"); err != nil {
		return err
	}
	if err := s.ensureColumn("trackers", "config_key",
		"ALTER TABLE trackers ADD COLUMN config_key TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// Rename trackers created under the old plugin id after it was generalized.
	if _, err := s.db.Exec(
		`UPDATE trackers SET plugin_id = 'github-activity' WHERE plugin_id = 'github-stars-history'`,
	); err != nil {
		return fmt.Errorf("migrate plugin ids: %w", err)
	}
	return nil
}

// ensureColumn runs alterSQL only if table is missing the named column.
func (s *Store) ensureColumn(table, column, alterSQL string) error {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil // already present
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := s.db.Exec(alterSQL); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

// CreateTracker inserts a new tracker and returns it with its assigned ID. A
// refreshIntervalSeconds of 0 means "use the plugin's default cadence".
func (s *Store) CreateTracker(pluginID, name string, config map[string]any, refreshIntervalSeconds int) (*Tracker, error) {
	if config == nil {
		config = map[string]any{}
	}
	if refreshIntervalSeconds < 0 {
		refreshIntervalSeconds = 0
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	res, err := s.db.Exec(
		`INSERT INTO trackers (plugin_id, name, config, refresh_interval_seconds) VALUES (?, ?, ?, ?)`,
		pluginID, name, string(raw), refreshIntervalSeconds,
	)
	if err != nil {
		return nil, fmt.Errorf("insert tracker: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetTracker(id)
}

// UpdateTracker overwrites the name and config of an existing tracker and
// returns the updated row. The plugin_id is intentionally immutable (changing
// it would invalidate the stored config against a different schema). It returns
// sql.ErrNoRows if no tracker matched.
func (s *Store) UpdateTracker(id int64, name string, config map[string]any, refreshIntervalSeconds int) (*Tracker, error) {
	if config == nil {
		config = map[string]any{}
	}
	if refreshIntervalSeconds < 0 {
		refreshIntervalSeconds = 0
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	res, err := s.db.Exec(
		`UPDATE trackers SET name = ?, config = ?, refresh_interval_seconds = ? WHERE id = ?`,
		name, string(raw), refreshIntervalSeconds, id,
	)
	if err != nil {
		return nil, fmt.Errorf("update tracker: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, sql.ErrNoRows
	}
	return s.GetTracker(id)
}

// GetTracker returns the tracker with the given id, or sql.ErrNoRows.
func (s *Store) GetTracker(id int64) (*Tracker, error) {
	row := s.db.QueryRow(
		`SELECT id, plugin_id, name, config, refresh_interval_seconds, created_at, source, config_key FROM trackers WHERE id = ?`, id,
	)
	return scanTracker(row)
}

// ListTrackers returns all trackers ordered by creation time.
func (s *Store) ListTrackers() ([]*Tracker, error) {
	rows, err := s.db.Query(
		`SELECT id, plugin_id, name, config, refresh_interval_seconds, created_at, source, config_key FROM trackers ORDER BY created_at ASC, id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list trackers: %w", err)
	}
	defer rows.Close()
	var out []*Tracker
	for rows.Next() {
		t, err := scanTracker(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTracker removes the tracker with the given id. It returns sql.ErrNoRows
// if no row matched.
func (s *Store) DeleteTracker(id int64) error {
	res, err := s.db.Exec(`DELETE FROM trackers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete tracker: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ReconcileFileTrackers makes the set of source="file" trackers match items
// exactly: file trackers present in items are upserted by their ConfigKey
// (updating in place so IDs and dashboard order survive), and file trackers no
// longer in items are deleted. User-created (source="db") trackers are never
// touched. It runs in a single transaction. Passing an empty slice removes all
// file trackers (e.g. when --config is dropped).
func (s *Store) ReconcileFileTrackers(items []FileTracker) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin reconcile: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Existing file trackers, keyed by config_key.
	rows, err := tx.Query(`SELECT id, config_key FROM trackers WHERE source = ?`, SourceFile)
	if err != nil {
		return fmt.Errorf("load file trackers: %w", err)
	}
	existing := map[string]int64{}
	for rows.Next() {
		var id int64
		var key string
		if err := rows.Scan(&id, &key); err != nil {
			rows.Close()
			return err
		}
		existing[key] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	seen := make(map[string]bool, len(items))
	for _, it := range items {
		cfg := it.Config
		if cfg == nil {
			cfg = map[string]any{}
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("marshal config for %q: %w", it.Key, err)
		}
		interval := it.RefreshIntervalSeconds
		if interval < 0 {
			interval = 0
		}
		seen[it.Key] = true
		if id, ok := existing[it.Key]; ok {
			if _, err := tx.Exec(
				`UPDATE trackers SET plugin_id = ?, name = ?, config = ?, refresh_interval_seconds = ? WHERE id = ?`,
				it.PluginID, it.Name, string(raw), interval, id,
			); err != nil {
				return fmt.Errorf("update file tracker %q: %w", it.Key, err)
			}
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO trackers (plugin_id, name, config, refresh_interval_seconds, source, config_key)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			it.PluginID, it.Name, string(raw), interval, SourceFile, it.Key,
		); err != nil {
			return fmt.Errorf("insert file tracker %q: %w", it.Key, err)
		}
	}

	for key, id := range existing {
		if !seen[key] {
			if _, err := tx.Exec(`DELETE FROM trackers WHERE id = ?`, id); err != nil {
				return fmt.Errorf("delete stale file tracker %q: %w", key, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit reconcile: %w", err)
	}
	return nil
}

// SnapshotRow is a persisted latest tracker result — a warm cache that survives
// restarts. It is exactly one row per tracker, overwritten on every run; this is
// last-known state, not history (no time series is kept).
type SnapshotRow struct {
	TrackerID              int64
	PluginID               string
	Name                   string
	RefreshIntervalSeconds int
	// ResultJSON is the marshaled plugin result, empty when Error is set.
	ResultJSON string
	Error      string
	FetchedAt  time.Time
}

// SaveSnapshot upserts the latest result for a tracker.
func (s *Store) SaveSnapshot(r SnapshotRow) error {
	_, err := s.db.Exec(
		`INSERT INTO snapshots (tracker_id, plugin_id, name, refresh_interval_seconds, result, error, fetched_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(tracker_id) DO UPDATE SET
		   plugin_id = excluded.plugin_id,
		   name = excluded.name,
		   refresh_interval_seconds = excluded.refresh_interval_seconds,
		   result = excluded.result,
		   error = excluded.error,
		   fetched_at = excluded.fetched_at`,
		r.TrackerID, r.PluginID, r.Name, r.RefreshIntervalSeconds,
		r.ResultJSON, r.Error, r.FetchedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}
	return nil
}

// LoadSnapshots returns all persisted snapshots. Rows for deleted trackers are
// removed automatically by the foreign-key cascade, so callers still filter to
// the current tracker set defensively.
func (s *Store) LoadSnapshots() ([]SnapshotRow, error) {
	rows, err := s.db.Query(
		`SELECT tracker_id, plugin_id, name, refresh_interval_seconds, result, error, fetched_at FROM snapshots`,
	)
	if err != nil {
		return nil, fmt.Errorf("load snapshots: %w", err)
	}
	defer rows.Close()
	var out []SnapshotRow
	for rows.Next() {
		var (
			r       SnapshotRow
			fetched string
		)
		if err := rows.Scan(&r.TrackerID, &r.PluginID, &r.Name, &r.RefreshIntervalSeconds, &r.ResultJSON, &r.Error, &fetched); err != nil {
			return nil, err
		}
		r.FetchedAt = parseTime(fetched)
		out = append(out, r)
	}
	return out, rows.Err()
}

// scanner abstracts *sql.Row and *sql.Rows for scanTracker.
type scanner interface {
	Scan(dest ...any) error
}

func scanTracker(sc scanner) (*Tracker, error) {
	var (
		t       Tracker
		rawCfg  string
		created string
	)
	if err := sc.Scan(&t.ID, &t.PluginID, &t.Name, &rawCfg, &t.RefreshIntervalSeconds, &created, &t.Source, &t.ConfigKey); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(rawCfg), &t.Config); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	t.CreatedAt = parseTime(created)
	return &t, nil
}

// parseTime tolerates the couple of timestamp formats SQLite emits for
// CURRENT_TIMESTAMP and RFC3339 round-trips.
func parseTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		time.RFC3339Nano,
	} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts
		}
	}
	return time.Time{}
}
