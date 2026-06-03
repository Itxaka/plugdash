package depfreshness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"plugdash/internal/plugin"
)

func decodeItems(t *testing.T, res plugin.Result) []listItem {
	t.Helper()
	b, _ := json.Marshal(res.Data)
	var wrap struct {
		Items []listItem `json:"items"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return wrap.Items
}

func badgeOf(it listItem) string {
	if len(it.Badges) == 0 {
		return ""
	}
	return it.Badges[0].Label
}

const goMod = `module example.com/x

go 1.22

require (
	github.com/foo/bar v1.2.3
	github.com/baz/Qux v2.0.0
	golang.org/x/sys v0.1.0 // indirect
)

require single.com/mod v0.5.0
`

const pkgJSON = `{
	"dependencies": {"left-pad": "1.0.0"},
	"devDependencies": {"jest": "^29.0.0"}
}`

// oneServer routes raw-file, Go proxy and npm registry requests by URL suffix.
func oneServer(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/main/go.mod"):
			_, _ = w.Write([]byte(goMod))
		case strings.HasSuffix(p, "/main/package.json"):
			_, _ = w.Write([]byte(pkgJSON))
		// Go module proxy: /<module>/@latest
		case strings.HasSuffix(p, "/@latest"):
			mod := strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/@latest")
			ver := map[string]string{
				"github.com/foo/bar":  "v1.2.3", // up to date
				"github.com/baz/!qux": "v2.1.0", // minor behind
				"single.com/mod":      "v1.0.0", // major behind (v0 -> v1)
			}[mod]
			if ver == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"Version":"` + ver + `"}`))
		// npm registry: /<pkg>/latest
		case strings.HasSuffix(p, "/latest"):
			pkg := strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/latest")
			ver := map[string]string{
				"left-pad": "1.3.0",  // outdated
				"jest":     "29.0.0", // up to date (^29.0.0 normalizes to 29.0.0)
			}[pkg]
			if ver == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"version":"` + ver + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	pr, pp, pn := rawBaseURL, goProxyBaseURL, npmRegistryBase
	rawBaseURL, goProxyBaseURL, npmRegistryBase = srv.URL, srv.URL, srv.URL
	t.Cleanup(func() {
		rawBaseURL, goProxyBaseURL, npmRegistryBase = pr, pp, pn
		srv.Close()
	})
}

func TestRunGoMod(t *testing.T) {
	oneServer(t)
	res, err := New().Run(context.Background(), plugin.Config{
		"repo": "o/r", "file": "go.mod",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Visualization != plugin.VizList {
		t.Fatalf("viz = %q, want list", res.Visualization)
	}
	items := decodeItems(t, res)
	// bar, Qux, mod — indirect skipped.
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 (indirect excluded): %+v", len(items), items)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.Title] = badgeOf(it)
	}
	if got["github.com/foo/bar"] != "up to date" {
		t.Errorf("bar = %q, want up to date", got["github.com/foo/bar"])
	}
	if got["github.com/baz/Qux"] != "outdated" {
		t.Errorf("Qux = %q, want outdated", got["github.com/baz/Qux"])
	}
	if got["single.com/mod"] != "major behind" {
		t.Errorf("mod = %q, want major behind", got["single.com/mod"])
	}
	if !strings.Contains(res.Title, "2 of 3 outdated") {
		t.Errorf("title = %q, want '2 of 3 outdated'", res.Title)
	}
}

func TestRunPackageJSON(t *testing.T) {
	oneServer(t)
	res, err := New().Run(context.Background(), plugin.Config{
		"repo": "o/r", "file": "package.json",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	items := decodeItems(t, res)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.Title] = badgeOf(it)
	}
	if got["left-pad"] != "outdated" {
		t.Errorf("left-pad = %q, want outdated", got["left-pad"])
	}
	if got["jest"] != "up to date" {
		t.Errorf("jest = %q, want up to date", got["jest"])
	}
}

func TestRunAllGreen(t *testing.T) {
	// A go.mod whose every (direct) dep matches the latest version.
	const allGreenMod = "module x\n\ngo 1.22\n\nrequire github.com/foo/bar v1.2.3\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/main/go.mod"):
			_, _ = w.Write([]byte(allGreenMod))
		case strings.HasSuffix(r.URL.Path, "/@latest"):
			_, _ = w.Write([]byte(`{"Version":"v1.2.3"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	pr, pp := rawBaseURL, goProxyBaseURL
	rawBaseURL, goProxyBaseURL = srv.URL, srv.URL
	t.Cleanup(func() { rawBaseURL, goProxyBaseURL = pr, pp; srv.Close() })

	res, err := New().Run(context.Background(), plugin.Config{"repo": "o/r", "file": "go.mod"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Title, "0 of 1 outdated") {
		t.Errorf("title = %q, want '0 of 1 outdated'", res.Title)
	}
	items := decodeItems(t, res)
	// A leading non-collapsed celebratory row + the collapsed dep.
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (summary + dep): %+v", len(items), items)
	}
	if items[0].Collapsed || badgeOf(items[0]) != "all current" {
		t.Errorf("first row should be the visible summary, got %+v", items[0])
	}
	if !items[1].Collapsed {
		t.Errorf("the up-to-date dep should be collapsed, got %+v", items[1])
	}
}

func TestRunUnsupportedFile(t *testing.T) {
	oneServer(t)
	if _, err := New().Run(context.Background(), plugin.Config{"repo": "o/r", "file": "requirements.txt"}); err == nil {
		t.Fatal("expected error for unsupported manifest")
	}
}

func TestRunInvalidRepo(t *testing.T) {
	if _, err := New().Run(context.Background(), plugin.Config{"repo": "bad", "file": "go.mod"}); err == nil {
		t.Fatal("expected error for invalid repo")
	}
}

func TestVersionStatus(t *testing.T) {
	if versionStatus("1.2.3", "1.2.3") != statusCurrent {
		t.Error("equal should be current")
	}
	if versionStatus("1.2.3", "1.4.0") != statusMinor {
		t.Error("same major should be minor")
	}
	if versionStatus("0.5.0", "1.0.0") != statusMajor {
		t.Error("differing major should be major")
	}
}
