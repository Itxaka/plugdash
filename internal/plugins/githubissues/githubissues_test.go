package githubissues

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

// issuesPayload returns a mix: a zero-comment issue, a 3-comment issue, and a
// PR (carries a pull_request field) with zero comments. Newest first by
// created_at so order assertions are unambiguous.
const issuesPayload = `[
	{
		"number": 10,
		"title": "Newer issue with no reply",
		"html_url": "https://github.com/o/r/issues/10",
		"comments": 0,
		"created_at": "2026-05-30T10:00:00Z",
		"user": {"login": "alice"}
	},
	{
		"number": 9,
		"title": "A pull request",
		"html_url": "https://github.com/o/r/pull/9",
		"comments": 0,
		"created_at": "2026-05-29T10:00:00Z",
		"pull_request": {"url": "https://api.github.com/repos/o/r/pulls/9"},
		"user": {"login": "bob"}
	},
	{
		"number": 8,
		"title": "Older issue with discussion",
		"html_url": "https://github.com/o/r/issues/8",
		"comments": 3,
		"created_at": "2026-05-28T10:00:00Z",
		"user": {"login": "carol"}
	}
]`

func newStub(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/o/r/issues") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(issuesPayload))
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

func TestRun_UnansweredOnly(t *testing.T) {
	newStub(t)
	p := New()
	res, err := p.Run(context.Background(), plugin.Config{"repos": "o/r"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if res.Visualization != plugin.VizList {
		t.Fatalf("visualization = %q, want %q", res.Visualization, plugin.VizList)
	}
	items := decodeItems(t, res)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1: %+v", len(items), items)
	}
	got := items[0]
	if got.Title != "Newer issue with no reply" {
		t.Errorf("title = %q", got.Title)
	}
	if got.URL != "https://github.com/o/r/issues/10" {
		t.Errorf("url = %q", got.URL)
	}
	if got.Badge != "no reply" {
		t.Errorf("badge = %q, want %q", got.Badge, "no reply")
	}
	if !strings.Contains(got.Subtitle, "o/r #10") {
		t.Errorf("subtitle = %q, want it to contain %q", got.Subtitle, "o/r #10")
	}
	if strings.Contains(got.Subtitle, "comments") {
		t.Errorf("subtitle should not mention comments in unanswered-only mode: %q", got.Subtitle)
	}
	if res.Title != "Issues needing attention — 1" {
		t.Errorf("result title = %q", res.Title)
	}
}

func TestRun_IncludeAnswered(t *testing.T) {
	newStub(t)
	p := New()
	res, err := p.Run(context.Background(), plugin.Config{
		"repos":           "o/r",
		"unanswered_only": false,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	items := decodeItems(t, res)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (PR excluded): %+v", len(items), items)
	}
	// Newest first: issue #10 then issue #8.
	if items[0].Title != "Newer issue with no reply" {
		t.Errorf("items[0].Title = %q, want newest first", items[0].Title)
	}
	if items[1].Title != "Older issue with discussion" {
		t.Errorf("items[1].Title = %q", items[1].Title)
	}
	// PR must not appear.
	for _, it := range items {
		if strings.Contains(it.URL, "/pull/") {
			t.Errorf("PR leaked into items: %q", it.URL)
		}
	}
	// Answered issue carries comment count and no "no reply" badge.
	if !strings.Contains(items[1].Subtitle, "3 comments") {
		t.Errorf("items[1].Subtitle = %q, want it to mention 3 comments", items[1].Subtitle)
	}
	if items[1].Badge == "no reply" {
		t.Errorf("answered issue should not have a 'no reply' badge")
	}
	// Zero-comment issue still gets the badge.
	if items[0].Badge != "no reply" {
		t.Errorf("items[0].Badge = %q, want %q", items[0].Badge, "no reply")
	}
}

func TestRun_EmptyReposError(t *testing.T) {
	newStub(t)
	p := New()
	if _, err := p.Run(context.Background(), plugin.Config{}); err == nil {
		t.Fatal("expected error for empty repos config, got nil")
	}
}
