// Package httphealth implements a plugin that checks whether an HTTP endpoint
// is reachable and returns the expected status code, rendering the result as a
// single big stat (UP / DOWN / unexpected status).
package httphealth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"plugdash/internal/plugin"
)

// Plugin checks the reachability and status of an HTTP endpoint.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "http-health" }
func (p *Plugin) Name() string { return "HTTP Health Check" }
func (p *Plugin) Description() string {
	return "Check that an HTTP endpoint is reachable and returns the expected status."
}

// RefreshInterval defaults to 30s: health is volatile and the check is cheap.
func (p *Plugin) RefreshInterval() time.Duration { return 30 * time.Second }

func (p *Plugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:         "url",
			Label:       "URL",
			Type:        plugin.FieldString,
			Required:    true,
			Placeholder: "https://example.com/health",
		},
		{
			Key:     "expected_status",
			Label:   "Expected status",
			Type:    plugin.FieldNumber,
			Default: 200,
			Help:    "Expected HTTP status code",
		},
		{
			Key:     "timeout_seconds",
			Label:   "Timeout (seconds)",
			Type:    plugin.FieldNumber,
			Default: 10,
		},
	}
}

// Run performs a GET against the configured URL and reports whether it is up
// and returns the expected status. A request that fails or returns an
// unexpected status is a valid result (not a plugin error); Run only returns an
// error when the URL is missing.
func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	url := strings.TrimSpace(cfg.String("url"))
	if url == "" {
		return plugin.Result{}, fmt.Errorf("url is required")
	}
	if !strings.Contains(url, "://") {
		url = "https://" + url
	}

	expected := cfg.Int("expected_status")
	if expected == 0 {
		expected = 200
	}

	timeout := cfg.Int("timeout_seconds")
	if timeout <= 0 {
		timeout = 10
	}

	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return stat("DOWN", url+" — "+err.Error(), "error"), nil
	}

	log := plugin.LoggerFrom(ctx)
	log.Debug("http health request", "url", url, "expected_status", expected)
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		log.Debug("http health request failed", "url", url, "error", err.Error())
		return stat("DOWN", url+" — "+err.Error(), "error"), nil
	}
	defer resp.Body.Close()
	log.Debug("http health response", "url", url, "status", resp.StatusCode, "ms", latency.Milliseconds())

	if resp.StatusCode == expected {
		label := fmt.Sprintf("%s · %d · %dms", url, resp.StatusCode, latency.Milliseconds())
		return stat("UP", label, "ok"), nil
	}

	label := fmt.Sprintf("%s (expected %d)", url, expected)
	return stat(fmt.Sprintf("%d", resp.StatusCode), label, "warn"), nil
}

// stat builds a VizStat Result with the {value, label, status} data shape.
func stat(value, label, status string) plugin.Result {
	return plugin.Result{
		Visualization: plugin.VizStat,
		Data: map[string]any{
			"value":  value,
			"label":  label,
			"status": status,
		},
	}
}
