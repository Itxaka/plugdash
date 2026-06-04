package githubreviewrequested

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

func decodeItems(t *testing.T, res plugin.Result) []listItem {
	t.Helper()
	b, err := json.Marshal(res.Data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	var wrap struct {
		Items []listItem `json:"items"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	return wrap.Items
}

// searchItems is the canonical two-PR search response used by the stubs.
const searchItems = `{"total_count":2,"items":[
	{"number":1,"title":"Add feature","html_url":"https://github.com/o/r/pull/1",
	 "created_at":"2026-05-01T00:00:00Z","updated_at":"2026-05-03T00:00:00Z",
	 "repository_url":"https://api.github.com/repos/o/r","user":{"login":"alice"}},
	{"number":2,"title":"Fix bug","html_url":"https://github.com/o/r/pull/2",
	 "created_at":"2026-05-02T00:00:00Z","updated_at":"2026-05-02T00:00:00Z",
	 "repository_url":"https://api.github.com/repos/o/r","user":{"login":"bob"}}
]}`

func TestPriorityOrdering(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	// Search order: alice (normal), renovate[bot] (bot), carol (to be high-prio).
	body := `{"items":[
		{"number":1,"title":"a","html_url":"https://github.com/o/r/pull/1","updated_at":"2026-05-03T00:00:00Z","repository_url":"https://api.github.com/repos/o/r","user":{"login":"alice"}},
		{"number":2,"title":"b","html_url":"https://github.com/o/r/pull/2","updated_at":"2026-05-02T00:00:00Z","repository_url":"https://api.github.com/repos/o/r","user":{"login":"renovate[bot]"}},
		{"number":3,"title":"c","html_url":"https://github.com/o/r/pull/3","updated_at":"2026-05-01T00:00:00Z","repository_url":"https://api.github.com/repos/o/r","user":{"login":"carol"}}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	prev := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	defer func() { plugins.GHBaseURL = prev }()

	res, err := New().Run(context.Background(), plugin.Config{
		"login":         "octocat",
		"high_priority": "carol",
		"low_priority":  "*[bot]",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	items := decodeItems(t, res)
	if len(items) != 3 {
		t.Fatalf("want 3 items, got %d", len(items))
	}
	// Expected: carol (high), alice (normal), renovate[bot] (low).
	wantOrder := []string{"@carol", "@alice", "@renovate[bot]"}
	for i, want := range wantOrder {
		if !strings.Contains(items[i].Subtitle, want) {
			t.Errorf("items[%d] subtitle %q, want it to contain %q", i, items[i].Subtitle, want)
		}
	}
}

func TestAuthorTier(t *testing.T) {
	high, hb := prioritySet([]string{"Alice"})
	low, lb := prioritySet([]string{"*[bot]", "@spammer"})
	cases := map[string]int{
		"alice":         0,
		"bob":           1,
		"renovate[bot]": 2,
		"spammer":       2,
	}
	for login, want := range cases {
		if got := authorTier(login, high, hb, low, lb); got != want {
			t.Errorf("authorTier(%q) = %d, want %d", login, got, want)
		}
	}
}

// startStub spins up a stub server. userStatus is the status the /user endpoint
// returns (200 to resolve "octocat", or 401 to simulate a missing/invalid token).
func startStub(t *testing.T, userStatus int) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if userStatus != http.StatusOK {
			w.WriteHeader(userStatus)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		_, _ = w.Write([]byte(`{"login":"octocat"}`))
	})
	mux.HandleFunc("/search/issues", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(searchItems))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	prev := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	t.Cleanup(func() { plugins.GHBaseURL = prev })
}

func TestExplicitLogin(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	startStub(t, http.StatusOK)

	p := New()
	res, err := p.Run(context.Background(), plugin.Config{"login": "octocat"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	items := decodeItems(t, res)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if !strings.Contains(items[0].Subtitle, "o/r#1") {
		t.Errorf("subtitle = %q, want it to contain o/r#1", items[0].Subtitle)
	}
}

func TestEmptyLoginResolvesViaUser(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "x") // token present so /user is reachable
	startStub(t, http.StatusOK)

	p := New()
	res, err := p.Run(context.Background(), plugin.Config{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	items := decodeItems(t, res)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
}

func TestEmptyLoginNoTokenFails(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	startStub(t, http.StatusUnauthorized)

	p := New()
	if _, err := p.Run(context.Background(), plugin.Config{}); err == nil {
		t.Fatal("expected an error when login is empty and /user fails")
	}
}
