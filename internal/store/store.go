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
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS settings (
	id   INTEGER PRIMARY KEY CHECK (id = 1),
	data TEXT NOT NULL DEFAULT '{}'
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
		`SELECT id, plugin_id, name, config, refresh_interval_seconds, created_at FROM trackers WHERE id = ?`, id,
	)
	return scanTracker(row)
}

// ListTrackers returns all trackers ordered by creation time.
func (s *Store) ListTrackers() ([]*Tracker, error) {
	rows, err := s.db.Query(
		`SELECT id, plugin_id, name, config, refresh_interval_seconds, created_at FROM trackers ORDER BY created_at ASC, id ASC`,
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
	if err := sc.Scan(&t.ID, &t.PluginID, &t.Name, &rawCfg, &t.RefreshIntervalSeconds, &created); err != nil {
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
