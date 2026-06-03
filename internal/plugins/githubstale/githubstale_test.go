package githubstale

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

// searchPayload returns two stale items (updated long in the past): one issue
// and one PR (carries a pull_request field).
const searchPayload = `{
	"items": [
		{
			"number": 42,
			"title": "An ancient issue",
			"html_url": "https://github.com/o/r/issues/42",
			"updated_at": "2020-01-01T00:00:00Z",
			"repository_url": "https://api.github.com/repos/o/r"
		},
		{
			"number": 7,
			"title": "An ancient pull request",
			"html_url": "https://github.com/o/r/pull/7",
			"updated_at": "2020-01-01T00:00:00Z",
			"repository_url": "https://api.github.com/repos/o/r",
			"pull_request": {"url": "https://api.github.com/repos/o/r/pulls/7"}
		}
	]
}`

func newStub(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/search/issues") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(searchPayload))
			return
		}
		http.NotFound(w, r)
	}))
	prev := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	t.Cleanup(func() {
		plugins.GHBaseURL = prev
		srv.Close()
	})
}

// decodeItems pulls the list items out of a Result's Data via JSON round-trip.
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

func TestRun_StaleItems(t *testing.T) {
	newStub(t)
	res, err := New().Run(context.Background(), plugin.Config{"repos": "o/r"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.Visualization != plugin.VizList {
		t.Fatalf("visualization = %q, want %q", res.Visualization, plugin.VizList)
	}
	items := decodeItems(t, res)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2: %+v", len(items), items)
	}
	if res.Title != "Stale items — 2" {
		t.Errorf("result title = %q, want %q", res.Title, "Stale items — 2")
	}

	var issueItem, prItem *listItem
	for i := range items {
		switch {
		case strings.HasSuffix(items[i].Subtitle, "PR"):
			prItem = &items[i]
		case strings.HasSuffix(items[i].Subtitle, "issue"):
			issueItem = &items[i]
		}
	}
	if issueItem == nil {
		t.Fatalf("no item with 'issue' subtitle: %+v", items)
	}
	if prItem == nil {
		t.Fatalf("no item with 'PR' subtitle: %+v", items)
	}
	if !strings.Contains(prItem.Subtitle, "PR") {
		t.Errorf("PR subtitle = %q, want it to contain %q", prItem.Subtitle, "PR")
	}
	if !strings.Contains(issueItem.Subtitle, "issue") {
		t.Errorf("issue subtitle = %q, want it to contain %q", issueItem.Subtitle, "issue")
	}

	for _, it := range items {
		if len(it.Badges) != 1 {
			t.Fatalf("item %q has %d badges, want 1", it.Title, len(it.Badges))
		}
		if !strings.HasPrefix(it.Badges[0].Label, "stale ") {
			t.Errorf("badge label = %q, want it to start with %q", it.Badges[0].Label, "stale ")
		}
	}
}

func TestRun_EmptyReposError(t *testing.T) {
	newStub(t)
	if _, err := New().Run(context.Background(), plugin.Config{}); err == nil {
		t.Fatal("expected error for empty repos config, got nil")
	}
}
