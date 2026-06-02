package githubstars

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

func TestRunBuildsCumulativeAscendingSeries(t *testing.T) {
	// 5 stars across 3 distinct days.
	stars := `[
		{"starred_at":"2024-01-01T08:00:00Z","user":{"login":"a"}},
		{"starred_at":"2024-01-01T12:00:00Z","user":{"login":"b"}},
		{"starred_at":"2024-01-02T09:00:00Z","user":{"login":"c"}},
		{"starred_at":"2024-01-03T01:00:00Z","user":{"login":"d"}},
		{"starred_at":"2024-01-03T02:00:00Z","user":{"login":"e"}}
	]`
	const wantTotal = 5

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/stargazers" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github.star+json" {
			t.Errorf("Accept = %q, want star+json", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("api version header = %q, want 2022-11-28", got)
		}
		if r.URL.Query().Get("page") == "1" {
			fmt.Fprint(w, stars)
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	old := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	t.Cleanup(func() { plugins.GHBaseURL = old })

	p := New()
	res, err := p.Run(context.Background(), plugin.Config{"repo": "o/r"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Visualization != plugin.VizTimeseries {
		t.Errorf("visualization = %q, want %q", res.Visualization, plugin.VizTimeseries)
	}

	d := decodeData(t, res)
	if d.Total != wantTotal {
		t.Errorf("total = %v, want %d", d.Total, wantTotal)
	}
	if d.Label != "Stars" {
		t.Errorf("label = %q, want %q", d.Label, "Stars")
	}
	if len(d.Points) != 3 {
		t.Fatalf("got %d points, want 3 (distinct days)", len(d.Points))
	}

	// Cumulative & ascending: values non-decreasing, times ascending.
	var prevV float64
	var prevT string
	for i, pt := range d.Points {
		if pt.V < prevV {
			t.Errorf("point %d v=%v decreased from %v", i, pt.V, prevV)
		}
		if i > 0 && pt.T <= prevT {
			t.Errorf("point %d t=%q not after %q", i, pt.T, prevT)
		}
		prevV, prevT = pt.V, pt.T
	}

	// Expected cumulative buckets: day1=2, day2=3, day3=5.
	want := []float64{2, 3, 5}
	for i, wv := range want {
		if d.Points[i].V != wv {
			t.Errorf("point %d v=%v, want %v", i, d.Points[i].V, wv)
		}
	}

	last := d.Points[len(d.Points)-1]
	if last.V != d.Total {
		t.Errorf("last point v=%v, want total %v", last.V, d.Total)
	}
}

func TestRunZeroStars(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	old := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	t.Cleanup(func() { plugins.GHBaseURL = old })

	p := New()
	res, err := p.Run(context.Background(), plugin.Config{"repo": "o/r"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	d := decodeData(t, res)
	if d.Total != 0 {
		t.Errorf("total = %v, want 0", d.Total)
	}
	if len(d.Points) != 0 {
		t.Errorf("got %d points, want 0", len(d.Points))
	}
}

func TestRunCommitsMetric(t *testing.T) {
	// 3 commits across 2 days; the commits endpoint must be hit (not stargazers).
	commits := `[
		{"commit":{"committer":{"date":"2024-02-01T10:00:00Z"}}},
		{"commit":{"committer":{"date":"2024-02-01T11:00:00Z"}}},
		{"commit":{"committer":{"date":"2024-02-02T09:00:00Z"}}}
	]`
	var hitPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		if r.URL.Path == "/repos/o/r/commits" && r.URL.Query().Get("page") == "1" {
			fmt.Fprint(w, commits)
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	old := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	t.Cleanup(func() { plugins.GHBaseURL = old })

	res, err := New().Run(context.Background(), plugin.Config{"repo": "o/r", "metric": "commits"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if hitPath != "/repos/o/r/commits" {
		t.Errorf("hit %q, want the commits endpoint", hitPath)
	}
	d := decodeData(t, res)
	if d.Label != "Commits" {
		t.Errorf("label = %q, want Commits", d.Label)
	}
	if d.Total != 3 {
		t.Errorf("total = %v, want 3", d.Total)
	}
	// day1=2, day2=3 cumulative.
	if len(d.Points) != 2 || d.Points[0].V != 2 || d.Points[1].V != 3 {
		t.Errorf("unexpected points %+v", d.Points)
	}
}

func TestRunUnknownMetric(t *testing.T) {
	if _, err := New().Run(context.Background(), plugin.Config{"repo": "o/r", "metric": "bogus"}); err == nil {
		t.Fatal("expected error for unknown metric")
	}
}

func TestRunInvalidRepo(t *testing.T) {
	p := New()
	_, err := p.Run(context.Background(), plugin.Config{"repo": "notavalidrepo"})
	if err == nil {
		t.Fatal("expected error for invalid repo, got nil")
	}
}

func TestRunNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	old := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	t.Cleanup(func() { plugins.GHBaseURL = old })

	p := New()
	_, err := p.Run(context.Background(), plugin.Config{"repo": "o/r"})
	if err == nil {
		t.Fatal("expected error on non-200, got nil")
	}
}

func TestDownsampleKeepsEnds(t *testing.T) {
	in := make([]point, 1000)
	for i := range in {
		in[i] = point{T: fmt.Sprintf("d%04d", i), V: float64(i + 1)}
	}
	out := downsample(in, maxPoints)
	if len(out) > maxPoints {
		t.Errorf("got %d points, want <= %d", len(out), maxPoints)
	}
	if out[0] != in[0] {
		t.Errorf("first point not preserved")
	}
	if out[len(out)-1] != in[len(in)-1] {
		t.Errorf("last point not preserved")
	}
}
