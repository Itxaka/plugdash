package engine

import (
	"testing"
	"time"

	"plugdash/internal/plugin"
)

// TestSnapshotPersistenceWarmStart verifies the restart behavior: a new engine
// on the same store loads the persisted snapshot AND does not re-run the tracker
// while its interval hasn't elapsed — so a service restart does not re-trigger
// every check.
func TestSnapshotPersistenceWarmStart(t *testing.T) {
	reg := plugin.NewRegistry()
	fp := &fakePlugin{id: "fake", interval: time.Hour, result: okResult()}
	reg.Register(fp)

	st := newTestStore(t)
	tr, err := st.CreateTracker(fp.id, "t1", nil, 0)
	if err != nil {
		t.Fatalf("CreateTracker: %v", err)
	}

	// First engine: force one run (bypasses the cold-start stagger), then shut down.
	e1 := New(reg, st, discardLogger())
	e1.Start()
	e1.Force(tr.ID)
	waitFor(t, 2*time.Second, "first snapshot", func() bool {
		_, ok := e1.Snapshot(tr.ID)
		return ok
	})
	if fp.runs.Load() < 1 {
		t.Fatalf("expected the tracker to run at least once before restart")
	}
	e1.Stop()

	// Restart on the same store.
	fp.runs.Store(0)
	e2 := New(reg, st, discardLogger())
	e2.Start()
	t.Cleanup(e2.Stop)

	// Persisted snapshot must be available immediately, before any run.
	s, ok := e2.Snapshot(tr.ID)
	if !ok {
		t.Fatalf("expected persisted snapshot to load on restart")
	}
	if s.Result == nil || s.Result.Title != "fake" {
		t.Fatalf("restored snapshot lost its result: %+v", s)
	}

	// With presence on, the tracker must NOT re-run: its 1h interval hasn't
	// elapsed since the persisted fetched_at.
	e2.Poll()
	time.Sleep(300 * time.Millisecond)
	if r := fp.runs.Load(); r != 0 {
		t.Fatalf("tracker re-ran %d time(s) on restart despite an unexpired interval", r)
	}
}
