package osvvulns

import (
	"context"
	"encoding/json"
	"io"
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

// stub serves /v1/query: returns two vulns for package "vuln-pkg", none otherwise.
// It also records the last decoded query so the test can assert passthrough.
func stub(t *testing.T) *osvQuery {
	t.Helper()
	last := &osvQuery{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/query" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, last)
		if last.Package.Name == "vuln-pkg" {
			_, _ = w.Write([]byte(`{"vulns":[
				{"id":"GHSA-xxxx","summary":"Bad thing","aliases":["CVE-2026-1111"]},
				{"id":"GO-2026-0001","details":"Detailed\nmultiline"}
			]}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	prev := osvBaseURL
	osvBaseURL = srv.URL
	t.Cleanup(func() {
		osvBaseURL = prev
		srv.Close()
	})
	return last
}

func TestRunWithVulns(t *testing.T) {
	last := stub(t)
	res, err := New().Run(context.Background(), plugin.Config{
		"package": "vuln-pkg", "version": "1.0.0", "ecosystem": "Go",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Visualization != plugin.VizList {
		t.Fatalf("viz = %q, want list", res.Visualization)
	}
	if !strings.Contains(res.Title, "2 known") {
		t.Errorf("title = %q, want it to mention 2 known", res.Title)
	}
	// Query passthrough.
	if last.Package.Name != "vuln-pkg" || last.Version != "1.0.0" || last.Package.Ecosystem != "Go" {
		t.Errorf("query passthrough wrong: %+v", last)
	}

	items := decodeItems(t, res)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	// First vuln: CVE alias becomes the badge label.
	if items[0].Title != "GHSA-xxxx" || items[0].Badges[0].Label != "CVE-2026-1111" {
		t.Errorf("vuln 0 = %+v, want GHSA title + CVE badge", items[0])
	}
	if items[0].Badges[0].Tone != "bad" {
		t.Errorf("vuln badge tone = %q, want bad", items[0].Badges[0].Tone)
	}
	if items[0].URL != "https://osv.dev/vulnerability/GHSA-xxxx" {
		t.Errorf("vuln url = %q", items[0].URL)
	}
	// Second vuln: no summary → first line of details; no CVE alias → "vuln".
	if items[1].Subtitle != "Detailed" {
		t.Errorf("vuln 1 subtitle = %q, want first line of details", items[1].Subtitle)
	}
	if items[1].Badges[0].Label != "vuln" {
		t.Errorf("vuln 1 badge = %q, want vuln", items[1].Badges[0].Label)
	}
}

func TestRunClean(t *testing.T) {
	stub(t)
	res, err := New().Run(context.Background(), plugin.Config{
		"package": "safe-pkg", "version": "2.0.0", "ecosystem": "npm",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Title, "no known vulnerabilities") {
		t.Errorf("title = %q, want clean", res.Title)
	}
	items := decodeItems(t, res)
	if len(items) != 1 || items[0].Badges[0].Label != "clean" || items[0].Badges[0].Tone != "ok" {
		t.Fatalf("clean result = %+v, want a single clean/ok row", items)
	}
}

func TestRunMissingFields(t *testing.T) {
	stub(t)
	if _, err := New().Run(context.Background(), plugin.Config{"package": "x"}); err == nil {
		t.Fatal("expected error when version is missing")
	}
	if _, err := New().Run(context.Background(), plugin.Config{"version": "1.0.0"}); err == nil {
		t.Fatal("expected error when package is missing")
	}
}
