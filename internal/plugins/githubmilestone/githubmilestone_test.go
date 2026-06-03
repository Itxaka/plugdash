package githubmilestone

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

func gaugeData(t *testing.T, res plugin.Result) map[string]any {
	t.Helper()
	if res.Visualization != plugin.VizGauge {
		t.Fatalf("viz = %q, want gauge", res.Visualization)
	}
	b, _ := json.Marshal(res.Data)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func stub(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	// By number.
	mux.HandleFunc("/repos/o/r/milestones/4", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"number":4,"title":"v1.0","state":"open","open_issues":3,"closed_issues":7,"html_url":"https://github.com/o/r/milestone/4"}`))
	})
	// By title (list).
	mux.HandleFunc("/repos/o/r/milestones", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"number":4,"title":"v1.0","state":"open","open_issues":3,"closed_issues":7},
			{"number":5,"title":"v2.0","state":"open","open_issues":1,"closed_issues":1}
		]`))
	})
	srv := httptest.NewServer(mux)
	prev := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	t.Cleanup(func() {
		plugins.GHBaseURL = prev
		srv.Close()
	})
}

func TestRunByNumber(t *testing.T) {
	stub(t)
	res, err := New().Run(context.Background(), plugin.Config{"repo": "o/r", "milestone": "4"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Title != "o/r · v1.0" {
		t.Errorf("title = %q", res.Title)
	}
	d := gaugeData(t, res)
	if int(d["value"].(float64)) != 7 || int(d["max"].(float64)) != 10 {
		t.Errorf("value/max = %v/%v, want 7/10", d["value"], d["max"])
	}
	if d["status"] != "warn" {
		t.Errorf("status = %v, want warn (incomplete)", d["status"])
	}
}

func TestRunByTitle(t *testing.T) {
	stub(t)
	res, err := New().Run(context.Background(), plugin.Config{"repo": "o/r", "milestone": "v2.0"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	d := gaugeData(t, res)
	// v2.0: 1 open + 1 closed = 2 total, 1 closed.
	if int(d["max"].(float64)) != 2 || int(d["value"].(float64)) != 1 {
		t.Errorf("value/max = %v/%v, want 1/2", d["value"], d["max"])
	}
}

func TestRunMissingTitle(t *testing.T) {
	stub(t)
	if _, err := New().Run(context.Background(), plugin.Config{"repo": "o/r", "milestone": "nope"}); err == nil {
		t.Fatal("expected error for unknown milestone title")
	}
}

func TestRunEmptyConfig(t *testing.T) {
	if _, err := New().Run(context.Background(), plugin.Config{"repo": "o/r"}); err == nil {
		t.Fatal("expected error for missing milestone")
	}
}
