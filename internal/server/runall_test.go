package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/store"
)

// runAllFakePlugin is a minimal plugin.Plugin for the run-all tests. It returns
// a VizStat result, unless its config carries {"fail": true}, in which case Run
// returns an error. Named distinctly from server_test.go's fakePlugin to avoid
// a redeclaration collision within the package.
type runAllFakePlugin struct{}

func (runAllFakePlugin) ID() string          { return "runall-fake" }
func (runAllFakePlugin) Name() string        { return "Run-All Fake Plugin" }
func (runAllFakePlugin) Description() string { return "a test plugin for run-all" }

func (runAllFakePlugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{Key: "fail", Label: "Fail", Type: plugin.FieldBool},
	}
}

func (runAllFakePlugin) RefreshInterval() time.Duration { return time.Minute }

func (runAllFakePlugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	if cfg.Bool("fail") {
		return plugin.Result{}, context.Canceled // any non-nil error
	}
	return plugin.Result{
		Visualization: plugin.VizStat,
		Title:         "Run-All Fake",
		Data:          map[string]any{"value": 1, "label": "ok"},
	}, nil
}

// newRunAllServer builds a Server with the run-all fake plugin registered and a
// real on-disk store in a fresh temp dir.
func newRunAllServer(t *testing.T) *Server {
	t.Helper()

	reg := plugin.NewRegistry()
	reg.Register(runAllFakePlugin{})

	st, err := store.Open(t.TempDir() + "/runall.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	return New(reg, st, fstest.MapFS{})
}

func TestRunAllEmpty(t *testing.T) {
	srv := newRunAllServer(t)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/run", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var results []runResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want 0", len(results))
	}
}

func TestRunAll(t *testing.T) {
	srv := newRunAllServer(t)

	// Create three trackers; the middle one is configured to fail. Order of
	// creation determines list order (store orders by created_at, id).
	mk := func(name string, fail bool) *store.Tracker {
		tr, err := srv.store.CreateTracker("runall-fake", name, map[string]any{"fail": fail}, 0)
		if err != nil {
			t.Fatalf("CreateTracker(%s): %v", name, err)
		}
		return tr
	}
	t0 := mk("first", false)
	t1 := mk("second-fails", true)
	t2 := mk("third", false)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/run", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var results []runResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}

	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}

	// Order preserved: index i corresponds to the i-th created tracker.
	wantIDs := []int64{t0.ID, t1.ID, t2.ID}
	for i, want := range wantIDs {
		if results[i].TrackerID != want {
			t.Errorf("results[%d].TrackerID = %d, want %d (order not preserved)", i, results[i].TrackerID, want)
		}
	}

	// results[0] (first): success -> non-nil Result, empty Error.
	if results[0].Error != "" {
		t.Errorf("results[0].Error = %q, want empty", results[0].Error)
	}
	if results[0].Result == nil {
		t.Errorf("results[0].Result = nil, want non-nil")
	}

	// results[1] (second-fails): failure -> non-empty Error, nil Result.
	if results[1].Error == "" {
		t.Errorf("results[1].Error is empty, want non-empty (the failing tracker)")
	}
	if results[1].Result != nil {
		t.Errorf("results[1].Result = %+v, want nil for failing tracker", results[1].Result)
	}

	// results[2] (third): success -> non-nil Result, empty Error.
	if results[2].Error != "" {
		t.Errorf("results[2].Error = %q, want empty", results[2].Error)
	}
	if results[2].Result == nil {
		t.Errorf("results[2].Result = nil, want non-nil")
	}
}
