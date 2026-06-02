package githubrepostats

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

func TestRunReturnsTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"stargazers_count": 123,
			"forks_count": 45,
			"open_issues_count": 6,
			"subscribers_count": 7,
			"language": "Go",
			"description": "a repo",
			"html_url": "https://github.com/o/r"
		}`))
	}))
	defer srv.Close()

	orig := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	defer func() { plugins.GHBaseURL = orig }()

	res, err := New().Run(context.Background(), plugin.Config{"repo": "o/r"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.Visualization != plugin.VizTable {
		t.Fatalf("Visualization = %q, want %q", res.Visualization, plugin.VizTable)
	}

	// Round-trip through JSON to inspect the data generically.
	raw, err := json.Marshal(res.Data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	var decoded struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}

	want := map[string]float64{
		"Stars":       123,
		"Forks":       45,
		"Open issues": 6,
		"Watchers":    7,
	}
	got := map[string]float64{}
	for _, row := range decoded.Rows {
		if len(row) != 2 {
			t.Fatalf("row %v has %d cols, want 2", row, len(row))
		}
		metric, ok := row[0].(string)
		if !ok {
			t.Fatalf("row metric %v is not a string", row[0])
		}
		if n, ok := row[1].(float64); ok {
			got[metric] = n
		}
	}
	for metric, n := range want {
		if got[metric] != n {
			t.Errorf("%s = %v, want %v", metric, got[metric], n)
		}
	}

	// Language row should be present with the expected string value.
	foundLang := false
	for _, row := range decoded.Rows {
		if row[0] == "Language" && row[1] == "Go" {
			foundLang = true
		}
	}
	if !foundLang {
		t.Errorf("expected Language row with value Go, rows=%v", decoded.Rows)
	}
}

func TestRunInvalidRepo(t *testing.T) {
	_, err := New().Run(context.Background(), plugin.Config{"repo": "not-a-repo"})
	if err == nil {
		t.Fatal("expected error for invalid repo, got nil")
	}
}
