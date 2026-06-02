// Package rssfeed implements a plugin that reports the latest entries from an
// RSS 2.0 or Atom feed as a list widget.
package rssfeed

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"time"

	"plugdash/internal/plugin"
)

// maxFeedBytes caps how much of a feed body we read, to avoid unbounded memory
// use on hostile or runaway responses (~5MB).
const maxFeedBytes = 5 << 20

// Plugin reports the latest N entries of an RSS/Atom feed.
type Plugin struct{}

// New returns the plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string          { return "rss-feed" }
func (p *Plugin) Name() string        { return "RSS / Atom Feed" }
func (p *Plugin) Description() string { return "Show the latest entries from an RSS or Atom feed." }

// RefreshInterval defaults to 15m: feeds update occasionally.
func (p *Plugin) RefreshInterval() time.Duration { return 15 * time.Minute }

func (p *Plugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{
			Key:         "url",
			Label:       "Feed URL",
			Type:        plugin.FieldString,
			Required:    true,
			Placeholder: "https://blog.golang.org/feed.atom",
			Help:        "URL of an RSS 2.0 or Atom feed.",
		},
		{
			Key:     "count",
			Label:   "Number of entries",
			Type:    plugin.FieldNumber,
			Default: 5,
			Help:    "How many recent entries to show (default 5).",
		},
	}
}

// listItem matches the shape the frontend "list" visualization expects.
type listItem struct {
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle"`
	URL       string `json:"url"`
	Timestamp string `json:"timestamp"`
}

// atomLink models an Atom <link href="..." rel="..."> element.
type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

// feed is a combined struct able to unmarshal both RSS 2.0 and Atom. RSS lives
// under <channel><item>; Atom uses top-level <entry> elements.
type feed struct {
	// RSS 2.0
	ChannelTitle string    `xml:"channel>title"`
	Items        []rssItem `xml:"channel>item"`
	// Atom
	FeedTitle string      `xml:"title"`
	Entries   []atomEntry `xml:"entry"`
}

type rssItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	PubDate string `xml:"pubDate"`
	Author  string `xml:"author"`
}

type atomEntry struct {
	Title     string     `xml:"title"`
	Links     []atomLink `xml:"link"`
	Updated   string     `xml:"updated"`
	Published string     `xml:"published"`
	Author    struct {
		Name string `xml:"name"`
	} `xml:"author"`
}

func (p *Plugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	url := cfg.String("url")
	if url == "" {
		return plugin.Result{}, fmt.Errorf("rssfeed: url is required")
	}

	count := cfg.Int("count")
	if count <= 0 {
		count = 5
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return plugin.Result{}, fmt.Errorf("rssfeed: build request: %w", err)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return plugin.Result{}, fmt.Errorf("rssfeed: fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return plugin.Result{}, fmt.Errorf("rssfeed: fetch %s: unexpected status %s", url, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes))
	if err != nil {
		return plugin.Result{}, fmt.Errorf("rssfeed: read body: %w", err)
	}

	var f feed
	if err := xml.Unmarshal(body, &f); err != nil {
		return plugin.Result{}, fmt.Errorf("rssfeed: parse feed: %w", err)
	}

	items := make([]listItem, 0, count)

	// RSS 2.0 items.
	for _, it := range f.Items {
		items = append(items, listItem{
			Title:     it.Title,
			URL:       it.Link,
			Subtitle:  firstNonEmpty(it.Author, it.PubDate),
			Timestamp: parseDate(it.PubDate),
		})
	}

	// Atom entries.
	for _, e := range f.Entries {
		dateText := firstNonEmpty(e.Published, e.Updated)
		items = append(items, listItem{
			Title:     e.Title,
			URL:       atomHref(e.Links),
			Subtitle:  firstNonEmpty(e.Author.Name, dateText),
			Timestamp: parseDate(dateText),
		})
	}

	if len(items) > count {
		items = items[:count]
	}

	title := firstNonEmpty(f.ChannelTitle, f.FeedTitle)

	return plugin.Result{
		Visualization: plugin.VizList,
		Title:         title,
		Data:          map[string]any{"items": items},
	}, nil
}

// atomHref picks the best link href from an Atom entry's links: prefer
// rel="alternate" (or empty rel, which defaults to alternate), else the first.
func atomHref(links []atomLink) string {
	for _, l := range links {
		if l.Rel == "" || l.Rel == "alternate" {
			if l.Href != "" {
				return l.Href
			}
		}
	}
	for _, l := range links {
		if l.Href != "" {
			return l.Href
		}
	}
	return ""
}

// parseDate normalizes a feed date string to "2006-01-02" when parseable,
// trying common RSS and Atom date layouts. Returns "" if unparseable.
func parseDate(s string) string {
	if s == "" {
		return ""
	}
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
