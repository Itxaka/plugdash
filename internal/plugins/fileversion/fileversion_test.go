package fileversion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"plugdash/internal/plugin"
)

func decodeData(t *testing.T, res plugin.Result) map[string]any {
	t.Helper()
	b, _ := json.Marshal(res.Data)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func TestExtractValue(t *testing.T) {
	cases := []struct {
		body, key, want string
		found           bool
	}{
		{"go 1.22.0\n", "go", "1.22.0", true},              // space-delimited (go.mod)
		{"VERSION=1.2.3\n", "VERSION", "1.2.3", true},      // =
		{"version: \"4.5.6\"\n", "version", "4.5.6", true}, // : + quotes
		{"  KEY = v9 ,\n", "KEY", "v9", true},              // trailing comma/space
		{"other: 1\n", "missing", "", false},               // absent
	}
	for _, c := range cases {
		got, found := extractValue(c.body, c.key)
		if found != c.found || got != c.want {
			t.Errorf("extractValue(%q,%q) = (%q,%v), want (%q,%v)", c.body, c.key, got, found, c.want, c.found)
		}
	}
}

func TestRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/o/r/main/go.mod" {
			_, _ = w.Write([]byte("module example.com/x\n\ngo 1.23.4\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	prev := rawBaseURL
	rawBaseURL = srv.URL
	defer func() { rawBaseURL = prev }()

	res, err := New().Run(context.Background(), plugin.Config{
		"repo": "o/r", "path": "go.mod", "key": "go",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Visualization != plugin.VizStat {
		t.Fatalf("viz = %q, want stat", res.Visualization)
	}
	d := decodeData(t, res)
	if d["value"] != "1.23.4" || d["status"] != "ok" {
		t.Errorf("got value=%v status=%v, want 1.23.4/ok", d["value"], d["status"])
	}

	// Missing key → status error, value "not found".
	res2, _ := New().Run(context.Background(), plugin.Config{"repo": "o/r", "path": "go.mod", "key": "nope"})
	d2 := decodeData(t, res2)
	if d2["status"] != "error" || d2["value"] != "not found" {
		t.Errorf("missing key: got %v/%v, want error/not found", d2["status"], d2["value"])
	}

	// Invalid repo → error.
	if _, err := New().Run(context.Background(), plugin.Config{"repo": "bad", "path": "x", "key": "y"}); err == nil {
		t.Error("expected error for invalid repo")
	}
}
