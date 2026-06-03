package githubdependabot

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

// stub serves the Dependabot alerts endpoint for o/r (two alerts) and o/empty
// (no alerts).
func stub(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/dependabot/alerts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"number":1,"state":"open","html_url":"https://github.com/o/r/security/dependabot/1",
			 "security_advisory":{"summary":"Critical RCE","severity":"critical","cve_id":"CVE-2026-0001"},
			 "security_vulnerability":{"package":{"name":"libfoo","ecosystem":"npm"}}},
			{"number":2,"state":"open","html_url":"https://github.com/o/r/security/dependabot/2",
			 "security_advisory":{"summary":"DoS in parser","severity":"medium","cve_id":""},
			 "security_vulnerability":{"package":{"name":"barlib","ecosystem":"Go"}}}
		]`))
	})
	mux.HandleFunc("/repos/o/empty/dependabot/alerts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	prev := plugins.GHBaseURL
	plugins.GHBaseURL = srv.URL
	t.Cleanup(func() {
		srv.Close()
		plugins.GHBaseURL = prev
	})
}

func TestRunListsAlerts(t *testing.T) {
	stub(t)

	res, err := New().Run(context.Background(), plugin.Config{"repo": "o/r"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Visualization != plugin.VizList {
		t.Fatalf("visualization = %q, want list", res.Visualization)
	}

	items := decodeItems(t, res)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	first := items[0]
	if len(first.Badges) != 1 {
		t.Fatalf("first item badges = %d, want 1", len(first.Badges))
	}
	if first.Badges[0].Label != "CVE-2026-0001" {
		t.Errorf("first badge label = %q, want CVE-2026-0001", first.Badges[0].Label)
	}
	if first.Badges[0].Tone != "bad" {
		t.Errorf("first badge tone = %q, want bad", first.Badges[0].Tone)
	}
	if !strings.Contains(first.Subtitle, "libfoo") || !strings.Contains(first.Subtitle, "critical") {
		t.Errorf("first subtitle = %q, want package name + severity", first.Subtitle)
	}

	second := items[1]
	if len(second.Badges) != 1 {
		t.Fatalf("second item badges = %d, want 1", len(second.Badges))
	}
	if second.Badges[0].Label != "medium" {
		t.Errorf("second badge label = %q, want medium", second.Badges[0].Label)
	}
	if second.Badges[0].Tone != "warn" {
		t.Errorf("second badge tone = %q, want warn", second.Badges[0].Tone)
	}
	if !strings.Contains(second.Subtitle, "barlib") || !strings.Contains(second.Subtitle, "medium") {
		t.Errorf("second subtitle = %q, want package name + severity", second.Subtitle)
	}
}

func TestRunEmpty(t *testing.T) {
	stub(t)

	res, err := New().Run(context.Background(), plugin.Config{"repo": "o/empty"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Visualization != plugin.VizList {
		t.Fatalf("visualization = %q, want list", res.Visualization)
	}
	if !strings.Contains(res.Title, "no open alerts") {
		t.Errorf("title = %q, want it to mention 'no open alerts'", res.Title)
	}
	items := decodeItems(t, res)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if len(items[0].Badges) != 1 || items[0].Badges[0].Label != "clean" || items[0].Badges[0].Tone != "ok" {
		t.Errorf("empty-case badge = %+v, want clean/ok", items[0].Badges)
	}
}

func TestRunInvalidRepo(t *testing.T) {
	stub(t)

	if _, err := New().Run(context.Background(), plugin.Config{"repo": "not-a-repo"}); err == nil {
		t.Fatal("expected error for invalid repo, got nil")
	}
}
