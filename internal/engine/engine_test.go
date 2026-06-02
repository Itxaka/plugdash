package engine

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/store"
)

// fakePlugin is a minimal plugin.Plugin used to drive the engine deterministically.
// It counts Run invocations and returns a fixed Result (or a fixed error).
type fakePlugin struct {
	id       string
	interval time.Duration
	result   plugin.Result
	err      error
	runs     atomic.Int64
}

func (f *fakePlugin) ID() string                         { return f.id }
func (f *fakePlugin) Name() string                       { return "Fake " + f.id }
func (f *fakePlugin) Description() string                { return "fake plugin for tests" }
func (f *fakePlugin) ConfigSchema() []plugin.ConfigField { return nil }
func (f *fakePlugin) RefreshInterval() time.Duration {
	if f.interval > 0 {
		return f.interval
	}
	return time.Second
}

func (f *fakePlugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	f.runs.Add(1)
	if f.err != nil {
		return plugin.Result{}, f.err
	}
	return f.result, nil
}

// discardLogger returns a logger that drops all output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestStore opens a fresh temp-dir SQLite store and registers cleanup.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// waitFor polls cond every 10ms until it is true or the timeout elapses.
// It fails the test if the condition never holds. Timeout is capped small so
// tests stay fast.
func waitFor(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("timed out after %s waiting for: %s", timeout, msg)
	}
}

// startEngine wires a registry+store engine, starts it, and registers Stop cleanup.
func startEngine(t *testing.T, reg *plugin.Registry, st *store.Store) *Engine {
	t.Helper()
	e := New(reg, st, discardLogger())
	e.Start()
	t.Cleanup(e.Stop)
	return e
}

func okResult() plugin.Result {
	return plugin.Result{
		Visualization: plugin.VizStat,
		Title:         "fake",
		Data:          map[string]any{"value": 42},
	}
}

// Test 1: with no presence (no subscribers, no Poll), the engine must never run
// trackers and SnapshotAll must stay empty.
func TestNoPresenceNoRuns(t *testing.T) {
	reg := plugin.NewRegistry()
	fp := &fakePlugin{id: "fake", interval: 10 * time.Millisecond, result: okResult()}
	reg.Register(fp)

	st := newTestStore(t)
	if _, err := st.CreateTracker(fp.id, "t1", nil, 0); err != nil {
		t.Fatalf("CreateTracker: %v", err)
	}

	e := startEngine(t, reg, st)

	// Give the scheduler ample ticks; nothing should run with no presence.
	time.Sleep(2 * time.Second)

	if got := fp.runs.Load(); got != 0 {
		t.Fatalf("expected 0 runs with no presence, got %d", got)
	}
	if snaps := e.SnapshotAll(); len(snaps) != 0 {
		t.Fatalf("expected empty SnapshotAll with no presence, got %d", len(snaps))
	}
}

// Test 2: a Poll() call counts as presence, so the tracker runs and a snapshot
// appears with the expected Result.
func TestPollPresenceRuns(t *testing.T) {
	reg := plugin.NewRegistry()
	fp := &fakePlugin{id: "fake", interval: 10 * time.Millisecond, result: okResult()}
	reg.Register(fp)

	st := newTestStore(t)
	tr, err := st.CreateTracker(fp.id, "t1", nil, 0)
	if err != nil {
		t.Fatalf("CreateTracker: %v", err)
	}

	e := startEngine(t, reg, st)
	e.Poll()

	waitFor(t, 2*time.Second, "snapshot after Poll", func() bool {
		_, ok := e.Snapshot(tr.ID)
		return ok
	})

	s, ok := e.Snapshot(tr.ID)
	if !ok {
		t.Fatalf("snapshot missing after Poll")
	}
	if s.Error != "" {
		t.Fatalf("unexpected snapshot error: %q", s.Error)
	}
	if s.Result == nil {
		t.Fatalf("expected non-nil Result")
	}
	if s.Result.Visualization != plugin.VizStat || s.Result.Title != "fake" {
		t.Fatalf("Result mismatch: %+v", s.Result)
	}
	if runs := fp.runs.Load(); runs < 1 {
		t.Fatalf("expected >= 1 run after Poll, got %d", runs)
	}
}

// Test 3: Subscribe replays the current snapshot as an SSE frame, and subscribing
// itself counts as presence (so runs keep happening). unsub must not panic.
func TestSubscribeReplayAndPresence(t *testing.T) {
	reg := plugin.NewRegistry()
	fp := &fakePlugin{id: "fake", interval: 10 * time.Millisecond, result: okResult()}
	reg.Register(fp)

	st := newTestStore(t)
	tr, err := st.CreateTracker(fp.id, "t1", nil, 0)
	if err != nil {
		t.Fatalf("CreateTracker: %v", err)
	}

	e := startEngine(t, reg, st)

	// Pre-seed a snapshot via the Poll presence path.
	e.Poll()
	waitFor(t, 2*time.Second, "pre-seeded snapshot", func() bool {
		_, ok := e.Snapshot(tr.ID)
		return ok
	})

	// Subscribe: the channel should immediately yield the replayed frame.
	ch, unsub := e.Subscribe()

	var frame []byte
	select {
	case frame = <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("did not receive replayed frame from Subscribe")
	}
	if !bytes.Contains(frame, []byte("event: snapshot")) {
		t.Fatalf("replayed frame missing SSE event header: %q", frame)
	}
	if !bytes.Contains(frame, []byte("\"tracker_id\":")) {
		t.Fatalf("replayed frame missing snapshot JSON: %q", frame)
	}

	// Subscribing counts as presence: let the Poll TTL be irrelevant by relying
	// only on the subscriber. Runs should continue to climb.
	before := fp.runs.Load()
	waitFor(t, 2*time.Second, "runs continue while subscribed", func() bool {
		return fp.runs.Load() > before
	})

	// Unsubscribe must not panic.
	unsub()
	// Double unsub must also be safe (it guards on map membership).
	unsub()
}

// Test 4: Force runs a tracker regardless of presence (no subscribers, no Poll).
func TestForceIgnoresPresence(t *testing.T) {
	reg := plugin.NewRegistry()
	// Long interval so only Force would trigger a run.
	fp := &fakePlugin{id: "fake", interval: time.Hour, result: okResult()}
	reg.Register(fp)

	st := newTestStore(t)
	tr, err := st.CreateTracker(fp.id, "t1", nil, 0)
	if err != nil {
		t.Fatalf("CreateTracker: %v", err)
	}

	e := startEngine(t, reg, st)

	// Sanity: no presence, so nothing runs on its own.
	time.Sleep(200 * time.Millisecond)
	if got := fp.runs.Load(); got != 0 {
		t.Fatalf("expected 0 runs before Force, got %d", got)
	}

	e.Force(tr.ID)

	waitFor(t, 2*time.Second, "snapshot after Force", func() bool {
		_, ok := e.Snapshot(tr.ID)
		return ok
	})
	if got := fp.runs.Load(); got < 1 {
		t.Fatalf("expected >= 1 run after Force, got %d", got)
	}
}

// Test 5: Reconcile drops snapshots for trackers removed from the store.
func TestReconcileDropsRemoved(t *testing.T) {
	reg := plugin.NewRegistry()
	fp := &fakePlugin{id: "fake", interval: 10 * time.Millisecond, result: okResult()}
	reg.Register(fp)

	st := newTestStore(t)
	t1, err := st.CreateTracker(fp.id, "t1", nil, 0)
	if err != nil {
		t.Fatalf("CreateTracker t1: %v", err)
	}
	t2, err := st.CreateTracker(fp.id, "t2", nil, 0)
	if err != nil {
		t.Fatalf("CreateTracker t2: %v", err)
	}

	e := startEngine(t, reg, st)
	e.Poll()

	// Both trackers should run and produce snapshots.
	waitFor(t, 2*time.Second, "both snapshots present", func() bool {
		_, ok1 := e.Snapshot(t1.ID)
		_, ok2 := e.Snapshot(t2.ID)
		return ok1 && ok2
	})

	// Delete t2 from the store and reconcile.
	if err := st.DeleteTracker(t2.ID); err != nil {
		t.Fatalf("DeleteTracker: %v", err)
	}
	if err := e.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// t2 snapshot must be gone; t1 must remain.
	if _, ok := e.Snapshot(t2.ID); ok {
		t.Fatalf("expected t2 snapshot dropped after Reconcile")
	}
	if _, ok := e.Snapshot(t1.ID); !ok {
		t.Fatalf("expected t1 snapshot retained after Reconcile")
	}
	for _, s := range e.SnapshotAll() {
		if s.TrackerID == t2.ID {
			t.Fatalf("SnapshotAll still includes deleted tracker t2")
		}
	}
}

// Test 6: a plugin that returns an error yields a snapshot with a non-empty Error
// and nil Result.
func TestErrorCapture(t *testing.T) {
	reg := plugin.NewRegistry()
	fp := &fakePlugin{
		id:       "fake",
		interval: 10 * time.Millisecond,
		err:      context.DeadlineExceeded, // any non-nil error
	}
	reg.Register(fp)

	st := newTestStore(t)
	tr, err := st.CreateTracker(fp.id, "t1", nil, 0)
	if err != nil {
		t.Fatalf("CreateTracker: %v", err)
	}

	e := startEngine(t, reg, st)
	e.Poll()

	waitFor(t, 2*time.Second, "error snapshot", func() bool {
		s, ok := e.Snapshot(tr.ID)
		return ok && s.Error != ""
	})

	s, ok := e.Snapshot(tr.ID)
	if !ok {
		t.Fatalf("snapshot missing")
	}
	if s.Error == "" {
		t.Fatalf("expected non-empty Error on failing plugin")
	}
	if s.Result != nil {
		t.Fatalf("expected nil Result on failing plugin, got %+v", s.Result)
	}
}
