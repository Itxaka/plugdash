package githubissuewatch

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

func TestParseRef(t *testing.T) {
	cases := []struct {
		in          string
		owner, repo string
		num         int
		wantErr     bool
	}{
		{"owner/repo#123", "owner", "repo", 123, false},
		{"  owner/repo#7 ", "owner", "repo", 7, false},
		{"https://github.com/o/r/issues/42", "o", "r", 42, false},
		{"https://github.com/o/r/pull/9/", "o", "r", 9, false},
		{"github.com/o/r/issues/5", "o", "r", 5, false},
		{"owner/repo", "", "", 0, true},   // no number
		{"owner/repo#x", "", "", 0, true}, // bad number
		{"", "", "", 0, true},
		{"https://github.com/o/r/discussions/1", "", "", 0, true}, // unsupported kind
	}
	for _, c := range cases {
		o, r, n, err := parseRef(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseRef(%q) = (%q,%q,%d), want error", c.in, o, r, n)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseRef(%q) unexpected error: %v", c.in, err)
			continue
		}
		if o != c.owner || r != c.repo || n != c.num {
			t.Errorf("parseRef(%q) = (%q,%q,%d), want (%q,%q,%d)", c.in, o, r, n, c.owner, c.repo, c.num)
		}
	}
}

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

func badgeLabels(it listItem) []string {
	out := make([]string, 0, len(it.Badges))
	for _, b := range it.Badges {
		out = append(out, b.Label)
	}
	return out
}

func hasBadge(it listItem, label string) bool {
	for _, b := range it.Badges {
		if b.Label == label {
			return true
		}
	}
	return false
}

// stubServer serves a small fixed world:
//
//	o/r#1   issue, 1 comment by "other"  → answered
//	o/r#2   issue, 0 comments            → no reply
//	o/r#3   PR, 0 comments, failing CI   → no reply + CI: failing
func stubServer(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"title":"Answered one","html_url":"https://github.com/o/r/issues/1","state":"open","comments":1,"created_at":"2026-05-01T00:00:00Z","user":{"login":"alice"}}`))
	})
	mux.HandleFunc("/repos/o/r/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"created_at":"2026-05-02T10:00:00Z","user":{"login":"bob"}}]`))
	})
	mux.HandleFunc("/repos/o/r/issues/2", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"title":"Silent one","html_url":"https://github.com/o/r/issues/2","state":"open","comments":0,"created_at":"2026-05-01T00:00:00Z","user":{"login":"alice"}}`))
	})
	mux.HandleFunc("/repos/o/r/issues/3", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"title":"A PR","html_url":"https://github.com/o/r/pull/3","state":"open","comments":0,"created_at":"2026-05-01T00:00:00Z","user":{"login":"alice"},"pull_request":{"url":"x"}}`))
	})
	mux.HandleFunc("/repos/o/r/pulls/3", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"merged":false,"head":{"sha":"deadbeef"}}`))
	})
	mux.HandleFunc("/repos/o/r/commits/deadbeef/check-runs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"total_count":2,"check_runs":[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"failure"}]}`))
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
	stubServer(t)

	res, err := New().Run(context.Background(), plugin.Config{
		"issues": "o/r#1\no/r#2\nhttps://github.com/o/r/pull/3\nnot-a-ref",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Visualization != plugin.VizList {
		t.Fatalf("visualization = %q, want list", res.Visualization)
	}
	if res.Title != "Issue watch — 2 unanswered / 4" {
		t.Errorf("title = %q, want 2 unanswered / 4", res.Title)
	}

	items := decodeItems(t, res)
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4", len(items))
	}

	// #1 answered (last comment by bob != author alice).
	if !hasBadge(items[0], "answered") {
		t.Errorf("item 1 badges = %v, want answered", badgeLabels(items[0]))
	}
	if !strings.Contains(items[0].Subtitle, "last reply") || !strings.Contains(items[0].Subtitle, "o/r#1") {
		t.Errorf("item 1 subtitle = %q", items[0].Subtitle)
	}

	// #2 unanswered, no CI badge (plain issue).
	if !hasBadge(items[1], "no reply") {
		t.Errorf("item 2 badges = %v, want no reply", badgeLabels(items[1]))
	}
	for _, b := range items[1].Badges {
		if strings.HasPrefix(b.Label, "CI") {
			t.Errorf("plain issue got a CI badge: %v", badgeLabels(items[1]))
		}
	}
	if !strings.Contains(items[1].Subtitle, "opened") {
		t.Errorf("item 2 subtitle = %q, want 'opened'", items[1].Subtitle)
	}

	// #3 PR: no reply + CI failing, subtitle marks it a PR.
	if !hasBadge(items[2], "no reply") || !hasBadge(items[2], "CI: failing") {
		t.Errorf("item 3 badges = %v, want no reply + CI: failing", badgeLabels(items[2]))
	}
	if !strings.Contains(items[2].Subtitle, "· PR ·") {
		t.Errorf("item 3 subtitle = %q, want it marked a PR", items[2].Subtitle)
	}

	// bad ref → invalid badge, error tone.
	if !hasBadge(items[3], "invalid") {
		t.Errorf("item 4 badges = %v, want invalid", badgeLabels(items[3]))
	}
}

func TestRunEmpty(t *testing.T) {
	if _, err := New().Run(context.Background(), plugin.Config{}); err == nil {
		t.Fatal("expected error for empty issues config")
	}
}

func TestCIBadge(t *testing.T) {
	cases := []struct {
		runs      checkRunsResp
		wantLabel string
		wantTone  string
	}{
		{checkRunsResp{TotalCount: 0}, "CI: no checks", "neutral"},
		{checkRunsResp{TotalCount: 1, CheckRuns: []checkRun{{Status: "completed", Conclusion: "success"}}}, "CI: passing", "ok"},
		{checkRunsResp{TotalCount: 1, CheckRuns: []checkRun{{Status: "in_progress"}}}, "CI: running", "neutral"},
		{checkRunsResp{TotalCount: 2, CheckRuns: []checkRun{{Status: "completed", Conclusion: "success"}, {Status: "completed", Conclusion: "failure"}}}, "CI: failing", "bad"},
	}
	for _, c := range cases {
		l, tone := ciBadge(c.runs)
		if l != c.wantLabel || tone != c.wantTone {
			t.Errorf("ciBadge(%+v) = (%q,%q), want (%q,%q)", c.runs, l, tone, c.wantLabel, c.wantTone)
		}
	}
}
