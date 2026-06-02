package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

// openTestStore opens a Store backed by a fresh database file inside the test's
// temporary directory and registers cleanup.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q) failed: %v", path, err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() failed: %v", err)
		}
	})
	return s
}

func TestCreateAndGetRoundTrip(t *testing.T) {
	s := openTestStore(t)

	cfg := map[string]any{
		"repo":  "a/b",
		"count": float64(5),
		"tags":  []any{"x", "y"},
	}
	created, err := s.CreateTracker("github", "my-tracker", cfg, 0)
	if err != nil {
		t.Fatalf("CreateTracker failed: %v", err)
	}
	if created.ID == 0 {
		t.Fatalf("expected non-zero assigned ID, got %d", created.ID)
	}
	if created.PluginID != "github" {
		t.Errorf("PluginID = %q, want %q", created.PluginID, "github")
	}
	if created.Name != "my-tracker" {
		t.Errorf("Name = %q, want %q", created.Name, "my-tracker")
	}

	got, err := s.GetTracker(created.ID)
	if err != nil {
		t.Fatalf("GetTracker(%d) failed: %v", created.ID, err)
	}
	if got.ID != created.ID {
		t.Errorf("ID = %d, want %d", got.ID, created.ID)
	}
	if got.PluginID != "github" {
		t.Errorf("PluginID = %q, want %q", got.PluginID, "github")
	}
	if got.Name != "my-tracker" {
		t.Errorf("Name = %q, want %q", got.Name, "my-tracker")
	}

	// JSON numbers round-trip as float64, so the expected map uses float64.
	wantCfg := map[string]any{
		"repo":  "a/b",
		"count": float64(5),
		"tags":  []any{"x", "y"},
	}
	if !reflect.DeepEqual(got.Config, wantCfg) {
		t.Errorf("Config = %#v, want %#v", got.Config, wantCfg)
	}

	if v, ok := got.Config["count"]; !ok {
		t.Errorf("Config missing key %q", "count")
	} else if _, ok := v.(float64); !ok {
		t.Errorf("Config[%q] = %T, want float64", "count", v)
	}
}

func TestCreateTrackerNilConfig(t *testing.T) {
	s := openTestStore(t)

	created, err := s.CreateTracker("plugin", "name", nil, 0)
	if err != nil {
		t.Fatalf("CreateTracker with nil config failed: %v", err)
	}
	got, err := s.GetTracker(created.ID)
	if err != nil {
		t.Fatalf("GetTracker failed: %v", err)
	}
	if got.Config == nil {
		t.Errorf("Config = nil, want empty non-nil map")
	}
	if len(got.Config) != 0 {
		t.Errorf("Config = %#v, want empty map", got.Config)
	}
}

func TestListTrackersOrder(t *testing.T) {
	s := openTestStore(t)

	names := []string{"first", "second", "third"}
	var wantIDs []int64
	for _, n := range names {
		tr, err := s.CreateTracker("p", n, nil, 0)
		if err != nil {
			t.Fatalf("CreateTracker(%q) failed: %v", n, err)
		}
		wantIDs = append(wantIDs, tr.ID)
	}

	list, err := s.ListTrackers()
	if err != nil {
		t.Fatalf("ListTrackers failed: %v", err)
	}
	if len(list) != len(names) {
		t.Fatalf("ListTrackers returned %d trackers, want %d", len(list), len(names))
	}
	for i, tr := range list {
		if tr.Name != names[i] {
			t.Errorf("list[%d].Name = %q, want %q", i, tr.Name, names[i])
		}
		if tr.ID != wantIDs[i] {
			t.Errorf("list[%d].ID = %d, want %d", i, tr.ID, wantIDs[i])
		}
	}
}

func TestListTrackersEmpty(t *testing.T) {
	s := openTestStore(t)

	list, err := s.ListTrackers()
	if err != nil {
		t.Fatalf("ListTrackers on empty db failed: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("ListTrackers returned %d trackers, want 0", len(list))
	}
}

func TestDeleteTracker(t *testing.T) {
	s := openTestStore(t)

	tr, err := s.CreateTracker("p", "doomed", nil, 0)
	if err != nil {
		t.Fatalf("CreateTracker failed: %v", err)
	}

	if err := s.DeleteTracker(tr.ID); err != nil {
		t.Fatalf("DeleteTracker(%d) failed: %v", tr.ID, err)
	}

	_, err = s.GetTracker(tr.ID)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetTracker after delete returned %v, want sql.ErrNoRows", err)
	}
}

func TestDeleteTrackerNotFound(t *testing.T) {
	s := openTestStore(t)

	err := s.DeleteTracker(99999)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("DeleteTracker(non-existent) returned %v, want sql.ErrNoRows", err)
	}
}

func TestGetTrackerNotFound(t *testing.T) {
	s := openTestStore(t)

	_, err := s.GetTracker(12345)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetTracker(non-existent) returned %v, want sql.ErrNoRows", err)
	}
}
