package store

import (
	"testing"
	"time"
)

func TestSnapshotSaveLoadUpsertCascade(t *testing.T) {
	st := openTestStore(t)

	tr, err := st.CreateTracker("p", "n", map[string]any{"x": 1}, 30)
	if err != nil {
		t.Fatalf("CreateTracker: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := st.SaveSnapshot(SnapshotRow{
		TrackerID:              tr.ID,
		PluginID:               "p",
		Name:                   "n",
		RefreshIntervalSeconds: 30,
		ResultJSON:             `{"a":1}`,
		FetchedAt:              now,
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	rows, err := st.LoadSnapshots()
	if err != nil {
		t.Fatalf("LoadSnapshots: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(rows))
	}
	got := rows[0]
	if got.TrackerID != tr.ID || got.ResultJSON != `{"a":1}` || got.RefreshIntervalSeconds != 30 {
		t.Fatalf("snapshot fields mismatch: %+v", got)
	}
	if !got.FetchedAt.Equal(now) {
		t.Errorf("fetched_at = %v, want %v", got.FetchedAt, now)
	}

	// Upsert: a second save for the same tracker overwrites, not duplicates.
	if err := st.SaveSnapshot(SnapshotRow{
		TrackerID: tr.ID, PluginID: "p", Name: "n", Error: "boom", FetchedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("SaveSnapshot (upsert): %v", err)
	}
	rows, _ = st.LoadSnapshots()
	if len(rows) != 1 {
		t.Fatalf("upsert created a duplicate: %d rows", len(rows))
	}
	if rows[0].Error != "boom" || rows[0].ResultJSON != "" {
		t.Fatalf("upsert did not overwrite: %+v", rows[0])
	}

	// Cascade: deleting the tracker removes its snapshot.
	if err := st.DeleteTracker(tr.ID); err != nil {
		t.Fatalf("DeleteTracker: %v", err)
	}
	rows, _ = st.LoadSnapshots()
	if len(rows) != 0 {
		t.Fatalf("snapshot not cascade-deleted with tracker: %d rows", len(rows))
	}
}
