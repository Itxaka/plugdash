package githubactions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// decoded mirrors the checklist Data shape for assertions.
type decoded struct {
	Items []struct {
		Label  string `json:"label"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
		URL    string `json:"url"`
		Links  []struct {
			Label string `json:"label"`
			URL   string `json:"url"`
			OK    bool   `json:"ok"`
		} `json:"links"`
	} `json:"items"`
	AllOK bool `json:"all_ok"`
}

// decode round-trips Result.Data through JSON into a typed struct.
func decode(t *testing.T, res plugin.Result) decoded {
	t.Helper()
	b, err := json.Marshal(res.Data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	var d decoded
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	return d
}

// startStub serves the repo + check-runs endpoints. checkRunsBody is the JSON
// returned for the check-runs path.
func startStub(t *testing.T, checkRunsBody string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"default_branch":"main","html_url":"https://github.com/o/r"}`))
	})
	mux.HandleFunc("/repos/o/r/commits/main/check-runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(checkRunsBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRunAllSuccess(t *testing.T) {
	srv := startStub(t, `{"total_count":2,"check_runs":[
		{"name":"build","status":"completed","conclusion":"success","html_url":"https://x/1"},
		{"name":"test","status":"completed","conclusion":"success","html_url":"https://x/2"}
	]}`)
	old := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	defer func() { plugins.GHBaseURL = old }()

	res, err := New().Run(context.Background(), plugin.Config{"repos": "o/r"})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	d := decode(t, res)
	if len(d.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(d.Items))
	}
	if !d.Items[0].OK {
		t.Errorf("want ok=true, got false (detail %q)", d.Items[0].Detail)
	}
	if d.Items[0].Detail != "passing" {
		t.Errorf("want detail passing, got %q", d.Items[0].Detail)
	}
	if !d.AllOK {
		t.Errorf("want all_ok=true")
	}
	wantLinks := []struct {
		Label string
		URL   string
		OK    bool
	}{
		{"build", "https://x/1", true},
		{"test", "https://x/2", true},
	}
	if len(d.Items[0].Links) != len(wantLinks) {
		t.Fatalf("want %d links, got %d", len(wantLinks), len(d.Items[0].Links))
	}
	for i, w := range wantLinks {
		got := d.Items[0].Links[i]
		if got.Label != w.Label || got.URL != w.URL || got.OK != w.OK {
			t.Errorf("link %d = {%q %q %v}, want {%q %q %v}",
				i, got.Label, got.URL, got.OK, w.Label, w.URL, w.OK)
		}
	}
}

func TestRunFailure(t *testing.T) {
	srv := startStub(t, `{"total_count":2,"check_runs":[
		{"name":"build","status":"completed","conclusion":"success","html_url":"https://x/1"},
		{"name":"lint","status":"completed","conclusion":"failure","html_url":"https://x/fail"}
	]}`)
	old := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	defer func() { plugins.GHBaseURL = old }()

	res, err := New().Run(context.Background(), plugin.Config{"repos": "o/r"})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	d := decode(t, res)
	if d.Items[0].OK {
		t.Errorf("want ok=false for failure")
	}
	if !strings.Contains(d.Items[0].Detail, "failing") {
		t.Errorf("want detail to mention failing, got %q", d.Items[0].Detail)
	}
	if d.Items[0].URL != "https://x/fail" {
		t.Errorf("want failing check url, got %q", d.Items[0].URL)
	}
	if d.AllOK {
		t.Errorf("want all_ok=false")
	}
	wantLinks := []struct {
		Label string
		URL   string
		OK    bool
	}{
		{"build", "https://x/1", true},
		{"lint", "https://x/fail", false},
	}
	if len(d.Items[0].Links) != len(wantLinks) {
		t.Fatalf("want %d links, got %d", len(wantLinks), len(d.Items[0].Links))
	}
	for i, w := range wantLinks {
		got := d.Items[0].Links[i]
		if got.Label != w.Label || got.URL != w.URL || got.OK != w.OK {
			t.Errorf("link %d = {%q %q %v}, want {%q %q %v}",
				i, got.Label, got.URL, got.OK, w.Label, w.URL, w.OK)
		}
	}
}

func TestRunNoChecks(t *testing.T) {
	srv := startStub(t, `{"total_count":0,"check_runs":[]}`)
	old := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	defer func() { plugins.GHBaseURL = old }()

	res, err := New().Run(context.Background(), plugin.Config{"repos": "o/r"})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	d := decode(t, res)
	if d.Items[0].OK {
		t.Errorf("want ok=false for no checks")
	}
	if d.Items[0].Detail != "no checks" {
		t.Errorf("want detail 'no checks', got %q", d.Items[0].Detail)
	}
}

func TestRunEmptyReposError(t *testing.T) {
	if _, err := New().Run(context.Background(), plugin.Config{}); err == nil {
		t.Fatalf("want error for empty repos config")
	}
	if _, err := New().Run(context.Background(), plugin.Config{"repos": ""}); err == nil {
		t.Fatalf("want error for blank repos config")
	}
}
