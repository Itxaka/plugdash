package githubrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// decodeData round-trips a Result's Data through JSON into the timeseries shape.
func decodeData(t *testing.T, data any) timeseriesData {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	var ts timeseriesData
	if err := json.Unmarshal(raw, &ts); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	return ts
}

func TestRunCommitsPerWeek(t *testing.T) {
	// Commits across two non-adjacent ISO weeks:
	//  - week of Mon 2024-01-01: three commits (Mon, Tue, Sun 2024-01-07)
	//  - week of Mon 2024-01-15: two commits (Mon, Wed)
	// The week of 2024-01-08 has none, so a zero-fill gap is expected.
	dates := []string{
		"2024-01-01T10:00:00Z",
		"2024-01-02T11:00:00Z",
		"2024-01-07T12:00:00Z",
		"2024-01-15T09:00:00Z",
		"2024-01-17T15:00:00Z",
	}
	const wantTotal = 5

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/commits" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			var b strings.Builder
			b.WriteString("[")
			for i, d := range dates {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"commit":{"committer":{"date":%q}}}`, d)
			}
			b.WriteString("]")
			_, _ = w.Write([]byte(b.String()))
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	old := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	t.Cleanup(func() { plugins.GHBaseURL = old })

	p := New()
	res, err := p.Run(context.Background(), plugin.Config{
		"repo":   "o/r",
		"metric": "commits",
		"period": "week",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Visualization != plugin.VizTimeseries {
		t.Errorf("Visualization = %q, want %q", res.Visualization, plugin.VizTimeseries)
	}

	ts := decodeData(t, res.Data)

	if !strings.Contains(ts.Label, "Commits per week") {
		t.Errorf("label = %q, want it to contain %q", ts.Label, "Commits per week")
	}
	if ts.Total != wantTotal {
		t.Errorf("total = %v, want %d", ts.Total, wantTotal)
	}

	// Per-period counts must sum to the total.
	var sum float64
	for _, pt := range ts.Points {
		sum += pt.V
	}
	if sum != wantTotal {
		t.Errorf("sum of point values = %v, want %d", sum, wantTotal)
	}

	// Points ascending by time.
	for i := 1; i < len(ts.Points); i++ {
		if ts.Points[i-1].T >= ts.Points[i].T {
			t.Errorf("points not strictly ascending: %q then %q", ts.Points[i-1].T, ts.Points[i].T)
		}
	}

	// Three weeks: 2024-01-01, 2024-01-08 (zero gap), 2024-01-15.
	if len(ts.Points) != 3 {
		t.Fatalf("got %d points, want 3 (with zero-filled gap): %+v", len(ts.Points), ts.Points)
	}
	want := []point{
		{T: "2024-01-01", V: 3},
		{T: "2024-01-08", V: 0},
		{T: "2024-01-15", V: 2},
	}
	for i, wpt := range want {
		if ts.Points[i] != wpt {
			t.Errorf("point[%d] = %+v, want %+v", i, ts.Points[i], wpt)
		}
	}
}

func TestRunUnknownMetricErrors(t *testing.T) {
	p := New()
	_, err := p.Run(context.Background(), plugin.Config{
		"repo":   "o/r",
		"metric": "bogus",
	})
	if err == nil {
		t.Fatal("expected error for unknown metric, got nil")
	}
}

func TestRunInvalidRepoErrors(t *testing.T) {
	p := New()
	_, err := p.Run(context.Background(), plugin.Config{
		"repo":   "not-a-valid-repo",
		"metric": "commits",
	})
	if err == nil {
		t.Fatal("expected error for invalid repo, got nil")
	}
}
