package httphealth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"plugdash/internal/plugin"
)

func dataMap(t *testing.T, r plugin.Result) map[string]any {
	t.Helper()
	if r.Visualization != plugin.VizStat {
		t.Fatalf("visualization = %q, want %q", r.Visualization, plugin.VizStat)
	}
	m, ok := r.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data is %T, want map[string]any", r.Data)
	}
	return m
}

func TestRun_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := New()
	res, err := p.Run(context.Background(), plugin.Config{
		"url":             srv.URL,
		"expected_status": float64(200),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	m := dataMap(t, res)
	if m["status"] != "ok" {
		t.Errorf("status = %v, want ok", m["status"])
	}
	if m["value"] != "UP" {
		t.Errorf("value = %v, want UP", m["value"])
	}
}

func TestRun_WrongStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := New()
	res, err := p.Run(context.Background(), plugin.Config{
		"url":             srv.URL,
		"expected_status": float64(200),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	m := dataMap(t, res)
	if m["status"] != "warn" {
		t.Errorf("status = %v, want warn", m["status"])
	}
	if m["value"] != "500" {
		t.Errorf("value = %v, want 500", m["value"])
	}
}

func TestRun_Down(t *testing.T) {
	// Start a server, then immediately close it so the port is unreachable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()

	p := New()
	res, err := p.Run(context.Background(), plugin.Config{
		"url":             url,
		"expected_status": float64(200),
	})
	if err != nil {
		t.Fatalf("Run returned error for unreachable endpoint: %v", err)
	}
	m := dataMap(t, res)
	if m["status"] != "error" {
		t.Errorf("status = %v, want error", m["status"])
	}
	if m["value"] != "DOWN" {
		t.Errorf("value = %v, want DOWN", m["value"])
	}
}

func TestRun_EmptyURL(t *testing.T) {
	p := New()
	if _, err := p.Run(context.Background(), plugin.Config{"url": "   "}); err == nil {
		t.Fatal("expected error for empty url, got nil")
	}
}

func TestConfigSchema(t *testing.T) {
	p := New()
	if p.ID() != "http-health" {
		t.Errorf("ID = %q, want http-health", p.ID())
	}
	fields := p.ConfigSchema()
	if len(fields) != 3 {
		t.Fatalf("got %d config fields, want 3", len(fields))
	}
	if fields[0].Key != "url" || !fields[0].Required {
		t.Errorf("first field should be required url, got %+v", fields[0])
	}
}
