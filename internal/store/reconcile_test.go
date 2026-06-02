package store

import (
	"testing"
)

func trackersByKey(t *testing.T, s *Store) map[string]*Tracker {
	t.Helper()
	list, err := s.ListTrackers()
	if err != nil {
		t.Fatalf("ListTrackers: %v", err)
	}
	out := map[string]*Tracker{}
	for _, tr := range list {
		if tr.Source == SourceFile {
			out[tr.ConfigKey] = tr
		}
	}
	return out
}

func TestReconcileIntoEmptyStore(t *testing.T) {
	s := openTestStore(t)
	items := []FileTracker{
		{Key: "a", PluginID: "p1", Name: "Alpha", Config: map[string]any{"x": "1"}},
		{Key: "b", PluginID: "p2", Name: "Bravo", RefreshIntervalSeconds: 60},
	}
	if err := s.ReconcileFileTrackers(items); err != nil {
		t.Fatalf("ReconcileFileTrackers: %v", err)
	}
	byKey := trackersByKey(t, s)
	if len(byKey) != 2 {
		t.Fatalf("file tracker count = %d, want 2", len(byKey))
	}
	for _, key := range []string{"a", "b"} {
		tr, ok := byKey[key]
		if !ok {
			t.Fatalf("missing file tracker %q", key)
		}
		if tr.Source != SourceFile {
			t.Errorf("%q Source = %q, want file", key, tr.Source)
		}
		if tr.ConfigKey != key {
			t.Errorf("%q ConfigKey = %q", key, tr.ConfigKey)
		}
		if tr.ID == 0 {
			t.Errorf("%q ID not assigned", key)
		}
	}
}

func TestReconcileIdempotentInPlaceUpdate(t *testing.T) {
	s := openTestStore(t)
	items := []FileTracker{
		{Key: "a", PluginID: "p1", Name: "Alpha", Config: map[string]any{"x": "1"}},
	}
	if err := s.ReconcileFileTrackers(items); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	origID := trackersByKey(t, s)["a"].ID

	// Same key, changed Name + Config.
	updated := []FileTracker{
		{Key: "a", PluginID: "p1", Name: "Alpha Renamed", Config: map[string]any{"x": "2"}},
	}
	if err := s.ReconcileFileTrackers(updated); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	tr := trackersByKey(t, s)["a"]
	if tr.ID != origID {
		t.Errorf("ID changed: got %d, want %d (matched by config_key)", tr.ID, origID)
	}
	if tr.Name != "Alpha Renamed" {
		t.Errorf("Name = %q, want Alpha Renamed", tr.Name)
	}
	if got := tr.Config["x"]; got != "2" {
		t.Errorf("Config[x] = %v, want 2", got)
	}
}

func TestReconcileRemovesSubset(t *testing.T) {
	s := openTestStore(t)
	items := []FileTracker{
		{Key: "a", PluginID: "p1", Name: "Alpha"},
		{Key: "b", PluginID: "p2", Name: "Bravo"},
	}
	if err := s.ReconcileFileTrackers(items); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	bID := trackersByKey(t, s)["b"].ID

	// Drop "a".
	if err := s.ReconcileFileTrackers([]FileTracker{
		{Key: "b", PluginID: "p2", Name: "Bravo"},
	}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	byKey := trackersByKey(t, s)
	if _, ok := byKey["a"]; ok {
		t.Error("dropped key \"a\" still present")
	}
	tr, ok := byKey["b"]
	if !ok {
		t.Fatal("remaining key \"b\" missing")
	}
	if tr.ID != bID {
		t.Errorf("b ID changed: got %d, want %d", tr.ID, bID)
	}
}

func TestReconcileLeavesDBTrackers(t *testing.T) {
	s := openTestStore(t)
	dbTr, err := s.CreateTracker("p-db", "User Tracker", map[string]any{"k": "v"}, 0)
	if err != nil {
		t.Fatalf("CreateTracker: %v", err)
	}
	if dbTr.Source != SourceDB {
		t.Fatalf("CreateTracker Source = %q, want db", dbTr.Source)
	}

	if err := s.ReconcileFileTrackers([]FileTracker{
		{Key: "a", PluginID: "p1", Name: "Alpha"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Now reconcile with nil → all file trackers removed, db survives.
	if err := s.ReconcileFileTrackers(nil); err != nil {
		t.Fatalf("reconcile nil: %v", err)
	}
	list, err := s.ListTrackers()
	if err != nil {
		t.Fatalf("ListTrackers: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("tracker count = %d, want 1 (db only)", len(list))
	}
	if list[0].ID != dbTr.ID {
		t.Errorf("surviving tracker ID = %d, want %d", list[0].ID, dbTr.ID)
	}
	if list[0].Source != SourceDB {
		t.Errorf("surviving tracker Source = %q, want db", list[0].Source)
	}
}

func TestReconcileEmptySliceRemovesAllFileTrackers(t *testing.T) {
	s := openTestStore(t)
	dbTr, err := s.CreateTracker("p-db", "User Tracker", nil, 0)
	if err != nil {
		t.Fatalf("CreateTracker: %v", err)
	}
	if err := s.ReconcileFileTrackers([]FileTracker{
		{Key: "a", PluginID: "p1", Name: "Alpha"},
		{Key: "b", PluginID: "p2", Name: "Bravo"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if err := s.ReconcileFileTrackers([]FileTracker{}); err != nil {
		t.Fatalf("reconcile empty: %v", err)
	}
	if n := len(trackersByKey(t, s)); n != 0 {
		t.Errorf("file tracker count = %d, want 0", n)
	}
	list, err := s.ListTrackers()
	if err != nil {
		t.Fatalf("ListTrackers: %v", err)
	}
	if len(list) != 1 || list[0].ID != dbTr.ID {
		t.Errorf("db tracker did not survive empty reconcile; list len=%d", len(list))
	}
}
