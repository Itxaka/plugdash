package githubartifacts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// releaseJSON is a minimal GitHub release object with a couple of assets.
const releaseJSON = `{
	"name": "v1.2.3",
	"tag_name": "v1.2.3",
	"html_url": "https://github.com/o/r/releases/tag/v1.2.3",
	"assets": [
		{"name": "app-linux-amd64"},
		{"name": "checksums.txt"}
	]
}`

// newStubServer returns an httptest server that serves releaseJSON for the
// release endpoints (both tagged and latest) and points plugins.GHBaseURL at
// it for the duration of the test, restoring the original on cleanup.
func newStubServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest", "/repos/o/r/releases/tags/v1.2.3":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(releaseJSON))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	orig := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	t.Cleanup(func() {
		plugins.GHBaseURL = orig
		srv.Close()
	})
	return srv
}

// runPlugin runs the plugin with cfg and decodes the resulting Data into a
// generic map for assertions. It fails the test if Run returns an error.
func runPlugin(t *testing.T, cfg plugin.Config) map[string]any {
	t.Helper()
	res, err := New().Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
	if res.Visualization != plugin.VizChecklist {
		t.Fatalf("Visualization = %q, want %q", res.Visualization, plugin.VizChecklist)
	}
	b, err := json.Marshal(res.Data)
	if err != nil {
		t.Fatalf("marshal Data: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal Data into map: %v", err)
	}
	return m
}

// itemsByLabel indexes the checklist items by their label for easy lookup.
func itemsByLabel(t *testing.T, data map[string]any) map[string]map[string]any {
	t.Helper()
	raw, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("items is not a list: %#v", data["items"])
	}
	out := make(map[string]map[string]any, len(raw))
	for _, e := range raw {
		item, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("item is not an object: %#v", e)
		}
		label, _ := item["label"].(string)
		out[label] = item
	}
	return out
}

func TestRun_AllExpectedPresent(t *testing.T) {
	newStubServer(t)

	data := runPlugin(t, plugin.Config{
		"repo":     "o/r",
		"tag":      "v1.2.3",
		"expected": []any{"app-linux-amd64", "checksums.txt"},
	})

	if allOK, _ := data["all_ok"].(bool); !allOK {
		t.Errorf("all_ok = %v, want true", data["all_ok"])
	}
	items := itemsByLabel(t, data)
	for _, label := range []string{"app-linux-amd64", "checksums.txt"} {
		item := items[label]
		if item == nil {
			t.Fatalf("missing item for %q", label)
		}
		if ok, _ := item["ok"].(bool); !ok {
			t.Errorf("item %q ok = %v, want true", label, item["ok"])
		}
	}
}

func TestRun_MissingArtifact(t *testing.T) {
	newStubServer(t)

	data := runPlugin(t, plugin.Config{
		"repo":     "o/r",
		"tag":      "v1.2.3",
		"expected": []any{"app-linux-amd64", "does-not-exist"},
	})

	if allOK, _ := data["all_ok"].(bool); allOK {
		t.Errorf("all_ok = %v, want false", data["all_ok"])
	}
	items := itemsByLabel(t, data)

	present := items["app-linux-amd64"]
	if present == nil {
		t.Fatal("missing item for app-linux-amd64")
	}
	if ok, _ := present["ok"].(bool); !ok {
		t.Errorf("present item ok = %v, want true", present["ok"])
	}

	missing := items["does-not-exist"]
	if missing == nil {
		t.Fatal("missing item for does-not-exist")
	}
	if ok, _ := missing["ok"].(bool); ok {
		t.Errorf("missing item ok = %v, want false", missing["ok"])
	}
	if detail, _ := missing["detail"].(string); detail != "not found" {
		t.Errorf("missing item detail = %q, want %q", detail, "not found")
	}
}

func TestRun_GlobMatch(t *testing.T) {
	newStubServer(t)

	data := runPlugin(t, plugin.Config{
		"repo":     "o/r",
		"tag":      "v1.2.3",
		"expected": []any{"app-*-amd64"},
	})

	if allOK, _ := data["all_ok"].(bool); !allOK {
		t.Errorf("all_ok = %v, want true", data["all_ok"])
	}
	items := itemsByLabel(t, data)
	item := items["app-*-amd64"]
	if item == nil {
		t.Fatal("missing item for glob pattern app-*-amd64")
	}
	if ok, _ := item["ok"].(bool); !ok {
		t.Errorf("glob item ok = %v, want true", item["ok"])
	}
}

func TestRun_EmptyExpected(t *testing.T) {
	newStubServer(t)

	_, err := New().Run(context.Background(), plugin.Config{
		"repo": "o/r",
		"tag":  "v1.2.3",
	})
	if err == nil {
		t.Fatal("expected an error for empty expected list, got nil")
	}
}

func TestRun_InvalidRepo(t *testing.T) {
	newStubServer(t)

	_, err := New().Run(context.Background(), plugin.Config{
		"repo":     "not-a-valid-repo",
		"expected": []any{"app-linux-amd64"},
	})
	if err == nil {
		t.Fatal("expected an error for invalid repo, got nil")
	}
}

func TestRun_LatestTag(t *testing.T) {
	newStubServer(t)

	// Empty tag should hit the /releases/latest endpoint and succeed.
	data := runPlugin(t, plugin.Config{
		"repo":     "o/r",
		"expected": []any{"checksums.txt"},
	})
	if allOK, _ := data["all_ok"].(bool); !allOK {
		t.Errorf("all_ok = %v, want true (latest endpoint)", data["all_ok"])
	}
}
