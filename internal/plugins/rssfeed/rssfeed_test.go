package rssfeed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"plugdash/internal/plugin"
)

const rssXML = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Example RSS</title>
    <item>
      <title>First RSS Post</title>
      <link>https://example.com/rss/1</link>
      <pubDate>Mon, 02 Jan 2006 15:04:05 -0700</pubDate>
      <author>alice@example.com</author>
    </item>
    <item>
      <title>Second RSS Post</title>
      <link>https://example.com/rss/2</link>
      <pubDate>Tue, 03 Jan 2006 15:04:05 -0700</pubDate>
    </item>
  </channel>
</rss>`

const atomXML = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Example Atom</title>
  <entry>
    <title>First Atom Entry</title>
    <link href="https://example.com/atom/1" rel="alternate"/>
    <published>2006-01-02T15:04:05Z</published>
    <author><name>Bob</name></author>
  </entry>
  <entry>
    <title>Second Atom Entry</title>
    <link href="https://example.com/atom/2"/>
    <updated>2006-01-03T15:04:05Z</updated>
  </entry>
</feed>`

// decodeItems marshals Result.Data to JSON and back into a typed shape so tests
// can assert on items without depending on the concrete internal struct.
func decodeItems(t *testing.T, data any) []listItem {
	t.Helper()
	b, err := json.Marshal(data)
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

func serve(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunRSS(t *testing.T) {
	srv := serve(t, rssXML)
	p := New()
	res, err := p.Run(context.Background(), plugin.Config{"url": srv.URL, "count": float64(10)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Visualization != plugin.VizList {
		t.Fatalf("visualization = %q, want %q", res.Visualization, plugin.VizList)
	}
	if res.Title != "Example RSS" {
		t.Fatalf("title = %q, want %q", res.Title, "Example RSS")
	}
	items := decodeItems(t, res.Data)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].Title != "First RSS Post" || items[0].URL != "https://example.com/rss/1" {
		t.Errorf("item0 = %+v", items[0])
	}
	if items[0].Subtitle != "alice@example.com" {
		t.Errorf("item0 subtitle = %q, want author", items[0].Subtitle)
	}
	if items[0].Timestamp != "2006-01-02" {
		t.Errorf("item0 timestamp = %q, want 2006-01-02", items[0].Timestamp)
	}
	if items[1].Title != "Second RSS Post" || items[1].URL != "https://example.com/rss/2" {
		t.Errorf("item1 = %+v", items[1])
	}
}

func TestRunAtom(t *testing.T) {
	srv := serve(t, atomXML)
	p := New()
	res, err := p.Run(context.Background(), plugin.Config{"url": srv.URL, "count": float64(10)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Visualization != plugin.VizList {
		t.Fatalf("visualization = %q, want %q", res.Visualization, plugin.VizList)
	}
	if res.Title != "Example Atom" {
		t.Fatalf("title = %q, want %q", res.Title, "Example Atom")
	}
	items := decodeItems(t, res.Data)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].Title != "First Atom Entry" || items[0].URL != "https://example.com/atom/1" {
		t.Errorf("item0 = %+v", items[0])
	}
	if items[0].Subtitle != "Bob" {
		t.Errorf("item0 subtitle = %q, want author name", items[0].Subtitle)
	}
	if items[0].Timestamp != "2006-01-02" {
		t.Errorf("item0 timestamp = %q, want 2006-01-02", items[0].Timestamp)
	}
	if items[1].Title != "Second Atom Entry" || items[1].URL != "https://example.com/atom/2" {
		t.Errorf("item1 = %+v", items[1])
	}
}

func TestRunCountTruncates(t *testing.T) {
	srv := serve(t, rssXML)
	p := New()
	res, err := p.Run(context.Background(), plugin.Config{"url": srv.URL, "count": float64(1)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	items := decodeItems(t, res.Data)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (truncated by count)", len(items))
	}
	if items[0].Title != "First RSS Post" {
		t.Errorf("kept wrong item: %+v", items[0])
	}
}

func TestRunDefaultCount(t *testing.T) {
	srv := serve(t, rssXML)
	p := New()
	// count omitted -> defaults to 5; both items still present.
	res, err := p.Run(context.Background(), plugin.Config{"url": srv.URL})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	items := decodeItems(t, res.Data)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
}

func TestRunEmptyURL(t *testing.T) {
	p := New()
	if _, err := p.Run(context.Background(), plugin.Config{}); err == nil {
		t.Fatal("expected error on empty url, got nil")
	}
}

func TestRunHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	p := New()
	if _, err := p.Run(context.Background(), plugin.Config{"url": srv.URL}); err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
}
