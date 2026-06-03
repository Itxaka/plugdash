package githubprs

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

func hasBadge(it listItem, label string) bool {
	for _, b := range it.Badges {
		if b.Label == label {
			return true
		}
	}
	return false
}

// stub serves one repo (o/r) with two open PRs:
//
//	#1 by alice, reviewer bob requested, approved review, CI passing
//	#2 by bob, draft, changes requested, CI failing
func stub(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		// list endpoint (no trailing number)
		_, _ = w.Write([]byte(`[
			{"number":1,"title":"Add feature","html_url":"https://github.com/o/r/pull/1","draft":false,
			 "created_at":"2026-05-01T00:00:00Z","updated_at":"2026-05-03T00:00:00Z",
			 "user":{"login":"alice"},"head":{"sha":"sha1"},"requested_reviewers":[{"login":"bob"}]},
			{"number":2,"title":"WIP cleanup","html_url":"https://github.com/o/r/pull/2","draft":true,
			 "created_at":"2026-05-02T00:00:00Z","updated_at":"2026-05-02T00:00:00Z",
			 "user":{"login":"bob"},"head":{"sha":"sha2"},"requested_reviewers":[]}
		]`))
	})
	mux.HandleFunc("/repos/o/r/pulls/1/reviews", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"state":"APPROVED","user":{"login":"carol"}}]`))
	})
	mux.HandleFunc("/repos/o/r/pulls/2/reviews", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"state":"CHANGES_REQUESTED","user":{"login":"carol"}}]`))
	})
	mux.HandleFunc("/repos/o/r/commits/sha1/check-runs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"total_count":1,"check_runs":[{"status":"completed","conclusion":"success"}]}`))
	})
	mux.HandleFunc("/repos/o/r/commits/sha2/check-runs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"total_count":1,"check_runs":[{"status":"completed","conclusion":"failure"}]}`))
	})
	srv := httptest.NewServer(mux)
	prev := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	t.Cleanup(func() {
		plugins.GHBaseURL = prev
		srv.Close()
	})
}

func TestRun(t *testing.T) {
	stub(t)
	res, err := New().Run(context.Background(), plugin.Config{"repos": "o/r"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Visualization != plugin.VizList {
		t.Fatalf("visualization = %q, want list", res.Visualization)
	}
	if res.Title != "Open PRs — 2" {
		t.Errorf("title = %q, want 'Open PRs — 2'", res.Title)
	}
	items := decodeItems(t, res)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	// Newest-updated first → #1 (updated 05-03) before #2 (05-02).
	if !strings.Contains(items[0].Subtitle, "o/r#1") {
		t.Errorf("items[0] = %q, want #1 first (newest updated)", items[0].Subtitle)
	}
	if !hasBadge(items[0], "approved") || !hasBadge(items[0], "CI: passing") {
		t.Errorf("item #1 badges wrong: %+v", items[0].Badges)
	}
	if !strings.Contains(items[0].Subtitle, "@alice") {
		t.Errorf("item #1 subtitle = %q, want @alice", items[0].Subtitle)
	}

	// #2: draft + changes requested + CI failing.
	if !hasBadge(items[1], "draft") || !hasBadge(items[1], "changes requested") || !hasBadge(items[1], "CI: failing") {
		t.Errorf("item #2 badges wrong: %+v", items[1].Badges)
	}
}

func TestRunAuthorFilter(t *testing.T) {
	stub(t)
	res, err := New().Run(context.Background(), plugin.Config{"repos": "o/r", "author": "alice"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	items := decodeItems(t, res)
	if len(items) != 1 || !strings.Contains(items[0].Subtitle, "@alice") {
		t.Fatalf("author filter: got %d items %+v, want only alice's", len(items), items)
	}
}

func TestRunReviewerFilter(t *testing.T) {
	stub(t)
	res, err := New().Run(context.Background(), plugin.Config{"repos": "o/r", "reviewer": "bob"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	items := decodeItems(t, res)
	// Only #1 has bob as a requested reviewer.
	if len(items) != 1 || !strings.Contains(items[0].Subtitle, "o/r#1") {
		t.Fatalf("reviewer filter: got %d items %+v, want only #1", len(items), items)
	}
}

func TestRunEmptyRepos(t *testing.T) {
	if _, err := New().Run(context.Background(), plugin.Config{}); err == nil {
		t.Fatal("expected error for empty repos config")
	}
}

func TestRunBadRepoIsErrorRow(t *testing.T) {
	stub(t)
	res, err := New().Run(context.Background(), plugin.Config{"repos": "not-a-repo"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	items := decodeItems(t, res)
	if len(items) != 1 || !hasBadge(items[0], "error") {
		t.Fatalf("bad repo: got %+v, want a single error row", items)
	}
}
