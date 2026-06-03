package githubworkflow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// decodeData round-trips the Result's Data through JSON into a typed struct so
// the test inspects exactly what the frontend would receive.
func decodeData(t *testing.T, res plugin.Result) timeseriesData {
	t.Helper()
	raw, err := json.Marshal(res.Data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	var d timeseriesData
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	return d
}

func TestRunBuildsSuccessRateAndDurationSeries(t *testing.T) {
	// 4 runs across two days:
	//   - 2024-01-01 success, started 10:00 ended 10:05 -> 5.0 min
	//   - 2024-01-01 failure, started 12:00 ended 12:08 -> 8.0 min
	//   - 2024-01-02 success, started 09:00 ended 09:03 -> 3.0 min
	//   - 2024-01-02 in_progress (must be excluded from rate + points)
	// completed = 3, success = 2 -> rate = 66.67%.
	body := `{
		"total_count": 4,
		"workflow_runs": [
			{"name":"CI","status":"completed","conclusion":"failure","run_started_at":"2024-01-01T12:00:00Z","updated_at":"2024-01-01T12:08:00Z","html_url":"https://example/2","created_at":"2024-01-01T12:00:00Z"},
			{"name":"CI","status":"completed","conclusion":"success","run_started_at":"2024-01-01T10:00:00Z","updated_at":"2024-01-01T10:05:00Z","html_url":"https://example/1","created_at":"2024-01-01T10:00:00Z"},
			{"name":"CI","status":"in_progress","conclusion":"","run_started_at":"2024-01-02T11:00:00Z","updated_at":"2024-01-02T11:01:00Z","html_url":"https://example/4","created_at":"2024-01-02T11:00:00Z"},
			{"name":"CI","status":"completed","conclusion":"success","run_started_at":"2024-01-02T09:00:00Z","updated_at":"2024-01-02T09:03:00Z","html_url":"https://example/3","created_at":"2024-01-02T09:00:00Z"}
		]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/actions/runs" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	old := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	t.Cleanup(func() { plugins.GHBaseURL = old })

	res, err := New().Run(context.Background(), plugin.Config{"repo": "o/r"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Visualization != plugin.VizTimeseries {
		t.Errorf("visualization = %q, want %q", res.Visualization, plugin.VizTimeseries)
	}

	d := decodeData(t, res)

	if d.Label != "% success" {
		t.Errorf("label = %q, want %q", d.Label, "% success")
	}

	// 2/3 success -> ~66.67%.
	if d.Total < 66 || d.Total >= 68 {
		t.Errorf("total (success rate) = %v, want ~66.67", d.Total)
	}

	// Only the 3 completed runs become points.
	if len(d.Points) != 3 {
		t.Fatalf("got %d points, want 3 (completed only)", len(d.Points))
	}

	// Ascending by run-start time (T is RFC3339, lexicographically sortable).
	var prevT string
	for i, pt := range d.Points {
		if i > 0 && pt.T <= prevT {
			t.Errorf("point %d t=%q not after %q", i, pt.T, prevT)
		}
		prevT = pt.T
	}

	// Durations by start order: 5.0 (10:00), 8.0 (12:00), 3.0 (09:00 next day).
	want := []float64{5.0, 8.0, 3.0}
	for i, wv := range want {
		if d.Points[i].V != wv {
			t.Errorf("point %d v=%v, want %v", i, d.Points[i].V, wv)
		}
	}

	// The in_progress run must not appear.
	if d.Points[0].T != "2024-01-01T10:00:00Z" {
		t.Errorf("first point t=%q, want first completed run", d.Points[0].T)
	}
}

func TestRunNoCompletedRuns(t *testing.T) {
	body := `{
		"total_count": 1,
		"workflow_runs": [
			{"name":"CI","status":"in_progress","conclusion":"","run_started_at":"2024-01-02T11:00:00Z","updated_at":"2024-01-02T11:01:00Z"}
		]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	old := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	t.Cleanup(func() { plugins.GHBaseURL = old })

	res, err := New().Run(context.Background(), plugin.Config{"repo": "o/r"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	d := decodeData(t, res)
	if d.Total != 0 {
		t.Errorf("total = %v, want 0 (no completed runs)", d.Total)
	}
	if len(d.Points) != 0 {
		t.Errorf("got %d points, want 0", len(d.Points))
	}
}

func TestRunInvalidRepo(t *testing.T) {
	_, err := New().Run(context.Background(), plugin.Config{"repo": "notavalidrepo"})
	if err == nil {
		t.Fatal("expected error for invalid repo, got nil")
	}
}

func TestRunAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	old := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	t.Cleanup(func() { plugins.GHBaseURL = old })

	_, err := New().Run(context.Background(), plugin.Config{"repo": "o/r"})
	if err == nil {
		t.Fatal("expected error on non-200, got nil")
	}
}
