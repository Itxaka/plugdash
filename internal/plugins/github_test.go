package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeRepo(t *testing.T) {
	valid := []struct {
		in    string
		owner string
		repo  string
	}{
		{"owner/repo", "owner", "repo"},
		{"https://github.com/owner/repo", "owner", "repo"},
		{"github.com/owner/repo.git", "owner", "repo"},
		{"owner/repo/", "owner", "repo"},
		{"  owner/repo  ", "owner", "repo"},
		{"http://github.com/owner/repo", "owner", "repo"},
	}
	for _, tc := range valid {
		owner, repo, err := NormalizeRepo(tc.in)
		if err != nil {
			t.Errorf("NormalizeRepo(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if owner != tc.owner || repo != tc.repo {
			t.Errorf("NormalizeRepo(%q) = (%q, %q), want (%q, %q)", tc.in, owner, repo, tc.owner, tc.repo)
		}
	}

	invalid := []string{"", "noslash", "/"}
	for _, in := range invalid {
		if _, _, err := NormalizeRepo(in); err == nil {
			t.Errorf("NormalizeRepo(%q) expected error, got nil", in)
		}
	}
}

func TestListReleases(t *testing.T) {
	releasesJSON := `[
		{"name":"Release 1","tag_name":"v3.0.0","published_at":"2026-03-01T10:00:00Z"},
		{"name":"Release 2","tag_name":"v2.0.0","published_at":"2026-02-01T10:00:00Z"},
		{"name":"Release 3","tag_name":"v1.0.0","published_at":"2026-01-01T10:00:00Z"}
	]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/releases" {
			t.Errorf("ListReleases hit unexpected path %q", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(releasesJSON))
	}))
	defer srv.Close()

	old := GHBaseURL
	GHBaseURL = srv.URL
	defer func() { GHBaseURL = old }()

	c := NewGHClient("")

	// All three when limit >= 3.
	all, err := c.ListReleases(context.Background(), "o", "r", 5)
	if err != nil {
		t.Fatalf("ListReleases error: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListReleases returned %d releases, want 3", len(all))
	}

	// Field decoding.
	if all[0].TagName != "v3.0.0" {
		t.Errorf("first release TagName = %q, want v3.0.0", all[0].TagName)
	}
	if all[0].PublishedAt.IsZero() {
		t.Errorf("first release PublishedAt was not parsed (zero)")
	}
	if got := all[0].PublishedAt.Format("2006-01-02"); got != "2026-03-01" {
		t.Errorf("first release PublishedAt = %q, want date 2026-03-01", got)
	}

	// limit truncates.
	two, err := c.ListReleases(context.Background(), "o", "r", 2)
	if err != nil {
		t.Fatalf("ListReleases(limit=2) error: %v", err)
	}
	if len(two) != 2 {
		t.Fatalf("ListReleases(limit=2) returned %d releases, want 2", len(two))
	}
	if two[0].TagName != "v3.0.0" || two[1].TagName != "v2.0.0" {
		t.Errorf("ListReleases(limit=2) truncated wrong: got %q, %q", two[0].TagName, two[1].TagName)
	}
}

func TestReleaseByTag(t *testing.T) {
	cases := []struct {
		tag      string
		wantPath string
	}{
		{"", "/repos/o/r/releases/latest"},
		{"latest", "/repos/o/r/releases/latest"},
		{"LATEST", "/repos/o/r/releases/latest"},
		{"v1.0.0", "/repos/o/r/releases/tags/v1.0.0"},
	}

	for _, tc := range cases {
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			rel := Release{Name: "rel", TagName: "v1.0.0"}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(rel)
		}))

		old := GHBaseURL
		GHBaseURL = srv.URL

		c := NewGHClient("")
		rel, err := c.ReleaseByTag(context.Background(), "o", "r", tc.tag)

		GHBaseURL = old
		srv.Close()

		if err != nil {
			t.Errorf("ReleaseByTag(tag=%q) error: %v", tc.tag, err)
			continue
		}
		if rel == nil {
			t.Errorf("ReleaseByTag(tag=%q) returned nil release", tc.tag)
			continue
		}
		if gotPath != tc.wantPath {
			t.Errorf("ReleaseByTag(tag=%q) hit path %q, want %q", tc.tag, gotPath, tc.wantPath)
		}
	}
}

func TestGetNon200ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	old := GHBaseURL
	GHBaseURL = srv.URL
	defer func() { GHBaseURL = old }()

	c := NewGHClient("")
	var out map[string]any
	if err := c.Get(context.Background(), "/repos/o/r/missing", &out); err == nil {
		t.Fatalf("Get on 404 expected error, got nil")
	}
}
