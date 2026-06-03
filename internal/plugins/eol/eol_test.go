package eol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// stub serves /go.json with three cycles: a supported one (eol false), one that
// is EOL in ~30 days (soon), and one already EOL. Other products 404.
func stub(t *testing.T) {
	t.Helper()
	soon := time.Now().Add(30 * 24 * time.Hour).Format("2006-01-02")
	body := fmt.Sprintf(`[
		{"cycle":"stable","latest":"1.22.3","eol":false},
		{"cycle":"soon","latest":"1.21.10","eol":%q},
		{"cycle":"old","latest":"1.20.14","eol":"2020-01-01"}
	]`, soon)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/go.json" {
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	prev := eolBaseURL
	eolBaseURL = srv.URL
	t.Cleanup(func() {
		eolBaseURL = prev
		srv.Close()
	})
}

func TestRun(t *testing.T) {
	stub(t)
	res, err := New().Run(context.Background(), plugin.Config{
		"products": "go\ngo@soon\ngo@old\ngo@missing\nbogus",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Visualization != plugin.VizList {
		t.Fatalf("viz = %q, want list", res.Visualization)
	}
	items := decodeItems(t, res)
	if len(items) != 5 {
		t.Fatalf("got %d items, want 5", len(items))
	}

	// "go" with no cycle → newest non-EOL = "stable" → supported.
	if badgeOf(items[0]) != "supported" {
		t.Errorf("item go badge = %q, want supported", badgeOf(items[0]))
	}
	if badgeOf(items[1]) != "EOL soon" {
		t.Errorf("item go@soon badge = %q, want EOL soon", badgeOf(items[1]))
	}
	if badgeOf(items[2]) != "EOL" {
		t.Errorf("item go@old badge = %q, want EOL", badgeOf(items[2]))
	}
	if badgeOf(items[3]) != "error" {
		t.Errorf("item go@missing badge = %q, want error (cycle not found)", badgeOf(items[3]))
	}
	if badgeOf(items[4]) != "error" {
		t.Errorf("item bogus badge = %q, want error (404)", badgeOf(items[4]))
	}

	// soon + old are at risk → title reflects 2 of 5.
	if res.Title != "End-of-life — 2 of 5 need attention" {
		t.Errorf("title = %q", res.Title)
	}
}

func TestRunEmpty(t *testing.T) {
	if _, err := New().Run(context.Background(), plugin.Config{}); err == nil {
		t.Fatal("expected error for empty products")
	}
}

func TestParseEOL(t *testing.T) {
	past := "2000-01-01"
	future := time.Now().Add(365 * 24 * time.Hour).Format("2006-01-02")

	if e, _, k := parseEOL(json.RawMessage(`false`)); e || k {
		t.Errorf("false → isEOL=%v known=%v, want false/false", e, k)
	}
	if e, _, k := parseEOL(json.RawMessage(`true`)); !e || !k {
		t.Errorf("true → isEOL=%v known=%v, want true/true", e, k)
	}
	if e, _, k := parseEOL(json.RawMessage(fmt.Sprintf("%q", past))); !e || !k {
		t.Errorf("past date → isEOL=%v known=%v, want true/true", e, k)
	}
	if e, _, k := parseEOL(json.RawMessage(fmt.Sprintf("%q", future))); e || !k {
		t.Errorf("future date → isEOL=%v known=%v, want false/true", e, k)
	}
	if _, _, k := parseEOL(json.RawMessage(`null`)); k {
		t.Errorf("null → known=true, want false")
	}
}

func TestPickCycle(t *testing.T) {
	cycles := []eolCycle{
		{Cycle: "3", EOL: json.RawMessage(`"2000-01-01"`)},
		{Cycle: "2", EOL: json.RawMessage(`false`)},
		{Cycle: "1", EOL: json.RawMessage(`false`)},
	}
	// No want → first non-EOL = "2".
	if c, ok := pickCycle(cycles, ""); !ok || c.Cycle != "2" {
		t.Errorf("pickCycle(\"\") = %q ok=%v, want 2", c.Cycle, ok)
	}
	// Explicit want.
	if c, ok := pickCycle(cycles, "3"); !ok || c.Cycle != "3" {
		t.Errorf("pickCycle(3) = %q ok=%v, want 3", c.Cycle, ok)
	}
	// Missing.
	if _, ok := pickCycle(cycles, "9"); ok {
		t.Errorf("pickCycle(9) ok=true, want false")
	}
	// All EOL → fallback to newest (index 0).
	allEOL := []eolCycle{{Cycle: "x", EOL: json.RawMessage(`"2000-01-01"`)}}
	if c, ok := pickCycle(allEOL, ""); !ok || c.Cycle != "x" {
		t.Errorf("pickCycle(all EOL) = %q ok=%v, want x", c.Cycle, ok)
	}
}
