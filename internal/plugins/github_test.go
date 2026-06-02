package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
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

// TestGetConditionalRequest verifies the package-global ETag cache: the first
// GET stores the ETag + body, and a SEPARATE fresh client (as every plugin run
// builds) sends If-None-Match and reuses the cached body on a 304. This is the
// whole point — plugins make a new GHClient per run, so the cache must survive
// across instances.
func TestGetConditionalRequest(t *testing.T) {
	var calls, conditional atomic.Int64
	const etag = `"etag-abc"`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("If-None-Match") == etag {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"v":7}`))
	}))
	defer srv.Close()

	old := GHBaseURL
	GHBaseURL = srv.URL
	defer func() { GHBaseURL = old }()

	// Unique token namespaces this test's cache/rate entries from other tests.
	const tok = "etag-test-token"
	path := "/repos/o/r/etag"

	var out map[string]int
	if err := NewGHClient(tok).Get(context.Background(), path, &out); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if out["v"] != 7 {
		t.Fatalf("first Get body = %+v, want v=7", out)
	}

	// A brand-new client must still hit the shared cache and get a 304.
	out = nil
	if err := NewGHClient(tok).Get(context.Background(), path, &out); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if out["v"] != 7 {
		t.Fatalf("304 path did not reuse cached body: %+v", out)
	}
	if c := conditional.Load(); c != 1 {
		t.Fatalf("expected exactly one 304 response, got %d (total calls=%d)", c, calls.Load())
	}
}

// TestRateLimitBackoff verifies that once GitHub reports the budget exhausted,
// the next call for that token fails fast WITHOUT hitting the network.
func TestRateLimitBackoff(t *testing.T) {
	var calls atomic.Int64
	reset := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", reset)
		http.Error(w, "API rate limit exceeded", http.StatusForbidden)
	}))
	defer srv.Close()

	old := GHBaseURL
	GHBaseURL = srv.URL
	defer func() { GHBaseURL = old }()

	const tok = "ratelimit-test-token"
	defer func() { // don't leak back-off state into other tests
		ghRate.Lock()
		delete(ghRate.until, tokenFingerprint(tok))
		ghRate.Unlock()
	}()

	c := NewGHClient(tok)
	var out map[string]any
	if err := c.Get(context.Background(), "/x", &out); err == nil {
		t.Fatalf("expected rate-limit error on first call")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("first call should reach the server once, got %d", got)
	}
	// Second call must short-circuit.
	if err := c.Get(context.Background(), "/x", &out); err == nil {
		t.Fatalf("expected fast-fail error while rate-limited")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("second call must NOT reach the server, total calls=%d", got)
	}
}
