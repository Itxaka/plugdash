package githubreleases

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// releasesJSON is a stub GitHub /repos/.../releases response. The first entry
// has no name (so the tag is used as the title), is a draft. The second has a
// name (preferred over the tag) and is a prerelease. A third entry exists to
// verify that count limits the result.
const releasesJSON = `[
	{
		"name": "",
		"tag_name": "v1.0.0",
		"html_url": "https://github.com/owner/repo/releases/v1.0.0",
		"draft": true,
		"prerelease": false,
		"published_at": "2024-01-02T03:04:05Z",
		"assets": [{"name": "a.zip"}]
	},
	{
		"name": "Shiny Release",
		"tag_name": "v0.9.0",
		"html_url": "https://github.com/owner/repo/releases/v0.9.0",
		"draft": false,
		"prerelease": true,
		"published_at": "2023-12-01T00:00:00Z",
		"assets": []
	},
	{
		"name": "Old Release",
		"tag_name": "v0.8.0",
		"html_url": "https://github.com/owner/repo/releases/v0.8.0",
		"draft": false,
		"prerelease": false,
		"published_at": "2023-11-01T00:00:00Z",
		"assets": []
	}
]`

// newStubServer returns an httptest server that serves releasesJSON for the
// releases endpoint and 404 otherwise.
func newStubServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/releases" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(releasesJSON))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
}

func TestRunReturnsListResult(t *testing.T) {
	srv := newStubServer(t)
	defer srv.Close()

	orig := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	defer func() { plugins.GHBaseURL = orig }()

	cfg := plugin.Config{"repo": "owner/repo", "count": 2, "show_prereleases": true}
	result, err := New().Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if result.Visualization != plugin.VizList {
		t.Errorf("Visualization = %q, want %q", result.Visualization, plugin.VizList)
	}

	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data is %T, want map[string]any", result.Data)
	}
	if _, ok := data["items"]; !ok {
		t.Fatalf("Data missing key %q", "items")
	}

	// Decode through JSON to keep assertions resilient to the concrete item type.
	items := decodeItems(t, result.Data)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (count should limit results)", len(items))
	}

	// First item: draft, empty name -> tag used as title.
	first := items[0]
	if got := str(first["title"]); got != "v1.0.0" {
		t.Errorf("first title = %q, want %q (tag used when name empty)", got, "v1.0.0")
	}
	if got := str(first["badge"]); got != "draft" {
		t.Errorf("first badge = %q, want %q", got, "draft")
	}

	// Second item: prerelease, name preferred over tag.
	second := items[1]
	if got := str(second["title"]); got != "Shiny Release" {
		t.Errorf("second title = %q, want %q (name preferred over tag)", got, "Shiny Release")
	}
	if got := str(second["badge"]); got != "prerelease" {
		t.Errorf("second badge = %q, want %q", got, "prerelease")
	}
}

func TestRunCountDefaultsAndCaps(t *testing.T) {
	srv := newStubServer(t)
	defer srv.Close()

	orig := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	defer func() { plugins.GHBaseURL = orig }()

	// count omitted -> default path still works; with prereleases enabled all
	// stub entries are returned.
	cfg := plugin.Config{"repo": "owner/repo", "show_prereleases": true}
	result, err := New().Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	items := decodeItems(t, result.Data)
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 with default count", len(items))
	}
}

// TestRunHidesPrereleasesByDefault verifies the default (show_prereleases off)
// drops drafts/prereleases, leaving only the stable release, marked "latest".
func TestRunHidesPrereleasesByDefault(t *testing.T) {
	srv := newStubServer(t)
	defer srv.Close()
	orig := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	defer func() { plugins.GHBaseURL = orig }()

	result, err := New().Run(context.Background(), plugin.Config{"repo": "owner/repo"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	items := decodeItems(t, result.Data)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (only the stable release)", len(items))
	}
	if got := str(items[0]["title"]); got != "Old Release" {
		t.Errorf("title = %q, want the stable %q", got, "Old Release")
	}
	if got := str(items[0]["badge"]); got != "latest" {
		t.Errorf("badge = %q, want %q", got, "latest")
	}
}

func TestRunInvalidRepo(t *testing.T) {
	// No server needed: NormalizeRepo should reject the repo before any request.
	cfg := plugin.Config{"repo": "bad"}
	_, err := New().Run(context.Background(), cfg)
	if err == nil {
		t.Fatal("Run with invalid repo returned nil error, want error")
	}
}

// decodeItems marshals Data and pulls out the "items" slice as generic maps.
func decodeItems(t *testing.T, data any) []map[string]any {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal Data: %v", err)
	}
	var decoded struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal Data: %v", err)
	}
	return decoded.Items
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
