// Package eol implements a plugin that tracks end-of-life / support dates for
// languages, runtimes, databases and operating systems via the free
// endoflife.date API. Each tracked product becomes a list row with a countdown
// badge (supported / EOL soon / EOL). No authentication is required.
package eol

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"plugdash/internal/plugin"
)

// eolBaseURL is the endoflife.date API root. A var so tests can point it at a stub.
var eolBaseURL = "https://endoflife.date/api"

var httpClient = &http.Client{Timeout: 15 * time.Second}

// soonWindow is how close to EOL a still-supported cycle gets the "EOL soon" warn badge.
const soonWindow = 90 * 24 * time.Hour

// Plugin tracks end-of-life dates for a set of products.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string   { return "endoflife" }
func (p *Plugin) Name() string { return "End-of-Life" }
func (p *Plugin) Description() string {
	return "Track end-of-life / support dates for languages, runtimes and OSes (endoflife.date)."
}

// RefreshInterval defaults to daily: EOL dates change rarely.
func (p *Plugin) RefreshInterval() time.Duration { return 24 * time.Hour }

func (p *Plugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:         "products",
			Label:       "Products",
			Type:        plugin.FieldList,
			Required:    true,
			Placeholder: "go\nnodejs\nkubernetes@1.29\nubuntu",
			Help:        "One endoflife.date product per line (e.g. go, nodejs, kubernetes, ubuntu, python). Append @cycle to pin a release, e.g. kubernetes@1.29.",
		},
	}
}

// badge is one tone-colored pill on a list item.
type badge struct {
	Label string `json:"label"`
	Tone  string `json:"tone"`
}

type listItem struct {
	Title     string  `json:"title"`
	Subtitle  string  `json:"subtitle"`
	URL       string  `json:"url"`
	Timestamp string  `json:"timestamp"`
	Badges    []badge `json:"badges,omitempty"`
}

// eolCycle is the subset of an endoflife.date cycle object this plugin reads.
// eol is polymorphic in the API: a date string, or a boolean.
type eolCycle struct {
	Cycle  string          `json:"cycle"`
	Latest string          `json:"latest"`
	EOL    json.RawMessage `json:"eol"`
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	specs := cfg.List("products")
	if len(specs) == 0 {
		return plugin.Result{}, fmt.Errorf("no products configured")
	}

	items := make([]listItem, 0, len(specs))
	atRisk := 0
	for _, spec := range specs {
		it, risk := evalProduct(ctx, spec)
		items = append(items, it)
		if risk {
			atRisk++
		}
	}

	return plugin.Result{
		Visualization: plugin.VizList,
		Title:         fmt.Sprintf("End-of-life — %d of %d need attention", atRisk, len(items)),
		Data:          map[string]any{"items": items},
	}, nil
}

// evalProduct resolves one "product" or "product@cycle" spec into a row, and
// reports whether it is at risk (EOL reached or within the soon window).
func evalProduct(ctx context.Context, spec string) (listItem, bool) {
	product, wantCycle, _ := strings.Cut(strings.TrimSpace(spec), "@")
	product = strings.ToLower(strings.TrimSpace(product))
	wantCycle = strings.TrimSpace(wantCycle)
	if product == "" {
		return errorItem(spec, "empty product"), false
	}

	var cycles []eolCycle
	if err := getJSON(ctx, eolBaseURL+"/"+product+".json", &cycles); err != nil {
		return errorItem(product, "error: "+err.Error()), false
	}
	if len(cycles) == 0 {
		return errorItem(product, "no release cycles found"), false
	}

	cyc, ok := pickCycle(cycles, wantCycle)
	if !ok {
		return errorItem(product, fmt.Sprintf("cycle %q not found", wantCycle)), false
	}

	isEOL, eolDate, known := parseEOL(cyc.EOL)
	title := product + " " + cyc.Cycle

	var b badge
	var risk bool
	detail := ""
	ts := ""
	switch {
	case isEOL:
		b = badge{Label: "EOL", Tone: "bad"}
		risk = true
		if known && !eolDate.IsZero() {
			detail = "EOL " + eolDate.Format("2006-01-02")
			ts = eolDate.Format(time.RFC3339)
		} else {
			detail = "end of life reached"
		}
	case known && !eolDate.IsZero():
		ts = eolDate.Format(time.RFC3339)
		detail = "EOL " + eolDate.Format("2006-01-02")
		if time.Until(eolDate) < soonWindow {
			b = badge{Label: "EOL soon", Tone: "warn"}
			risk = true
		} else {
			b = badge{Label: "supported", Tone: "ok"}
		}
	default:
		// eol: false → actively supported, no date.
		b = badge{Label: "supported", Tone: "ok"}
		detail = "no EOL announced"
	}

	if cyc.Latest != "" {
		detail = "latest " + cyc.Latest + " · " + detail
	}

	return listItem{
		Title:     title,
		Subtitle:  detail,
		URL:       "https://endoflife.date/" + product,
		Timestamp: ts,
		Badges:    []badge{b},
	}, risk
}

// pickCycle returns the requested cycle, or — when none is requested — the
// newest cycle that is not yet end-of-life (falling back to the newest overall).
// The API lists cycles newest-first.
func pickCycle(cycles []eolCycle, want string) (eolCycle, bool) {
	if want != "" {
		for _, c := range cycles {
			if c.Cycle == want {
				return c, true
			}
		}
		return eolCycle{}, false
	}
	for _, c := range cycles {
		if isEOL, _, _ := parseEOL(c.EOL); !isEOL {
			return c, true
		}
	}
	return cycles[0], true
}

// parseEOL interprets the polymorphic eol field. known is false when the value
// is missing/unparseable. When eol is the boolean true, isEOL is true with no date.
func parseEOL(raw json.RawMessage) (isEOL bool, date time.Time, known bool) {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "", "null":
		return false, time.Time{}, false
	case "false":
		return false, time.Time{}, false
	case "true":
		return true, time.Time{}, true
	}
	var ds string
	if err := json.Unmarshal(raw, &ds); err == nil {
		if t, err := time.Parse("2006-01-02", ds); err == nil {
			return !time.Now().Before(t), t, true
		}
	}
	return false, time.Time{}, false
}

func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	log := plugin.LoggerFrom(ctx)
	log.Debug("endoflife request", "url", url)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	log.Debug("endoflife response", "url", url, "status", resp.StatusCode, "bytes", len(body))
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("unknown product (HTTP 404) — check the slug at endoflife.date")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func errorItem(label, detail string) listItem {
	return listItem{
		Title:    label,
		Subtitle: detail,
		Badges:   []badge{{Label: "error", Tone: "bad"}},
	}
}
