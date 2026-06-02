package dockerimage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"plugdash/internal/plugin"
	"plugdash/internal/plugins"
)

// ociIndex is a minimal OCI image index advertising amd64 + arm64.
const ociIndex = `{
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "manifests": [
    {"platform": {"os": "linux", "architecture": "amd64"}},
    {"platform": {"os": "linux", "architecture": "arm64"}}
  ]
}`

// newRegistry starts an httptest registry that serves a multi-arch index for
// "good" tags and 404 for everything else, plus a permissive /token endpoint.
// It returns the server and the image ref (host:port/myrepo) to feed the plugin.
func newRegistry(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"x"}`))
		case strings.HasPrefix(r.URL.Path, "/v2/myrepo/manifests/"):
			tag := strings.TrimPrefix(r.URL.Path, "/v2/myrepo/manifests/")
			if tag == "good" || tag == "latest" {
				w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
				_, _ = w.Write([]byte(ociIndex))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	return srv, host + "/myrepo"
}

func items(t *testing.T, r plugin.Result) []any {
	t.Helper()
	if r.Visualization != plugin.VizChecklist {
		t.Fatalf("visualization = %q, want %q", r.Visualization, plugin.VizChecklist)
	}
	m, ok := r.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data is %T, want map[string]any", r.Data)
	}
	its, ok := m["items"].([]checklistItem)
	if !ok {
		t.Fatalf("items is %T, want []checklistItem", m["items"])
	}
	out := make([]any, len(its))
	for i := range its {
		out[i] = its[i]
	}
	return out
}

func allOK(t *testing.T, r plugin.Result) bool {
	t.Helper()
	m := r.Data.(map[string]any)
	v, ok := m["all_ok"].(bool)
	if !ok {
		t.Fatalf("all_ok is %T, want bool", m["all_ok"])
	}
	return v
}

func TestRun_ArchPresent(t *testing.T) {
	_, image := newRegistry(t)
	res, err := New().Run(context.Background(), plugin.Config{
		"image":  image,
		"tags":   []string{"good"},
		"arches": []string{"amd64"},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	its := items(t, res)
	if len(its) != 1 {
		t.Fatalf("got %d items, want 1", len(its))
	}
	it := its[0].(checklistItem)
	if !it.OK {
		t.Errorf("expected amd64 ok, got %+v", it)
	}
	if it.Detail != "found: good" {
		t.Errorf("detail = %q, want found: good", it.Detail)
	}
	if !allOK(t, res) {
		t.Errorf("all_ok = false, want true")
	}
}

func TestRun_MissingTag(t *testing.T) {
	_, image := newRegistry(t)
	res, err := New().Run(context.Background(), plugin.Config{
		"image":  image,
		"tags":   []string{"nope"},
		"arches": []string{"amd64"},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	it := items(t, res)[0].(checklistItem)
	if it.OK {
		t.Errorf("expected missing tag not ok, got %+v", it)
	}
	if it.Detail != "missing tag" {
		t.Errorf("detail = %q, want missing tag", it.Detail)
	}
	if allOK(t, res) {
		t.Errorf("all_ok = true, want false")
	}
}

func TestRun_ArchNotInList(t *testing.T) {
	_, image := newRegistry(t)
	res, err := New().Run(context.Background(), plugin.Config{
		"image":  image,
		"tags":   []string{"good"},
		"arches": []string{"ppc64le"},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	it := items(t, res)[0].(checklistItem)
	if it.OK {
		t.Errorf("expected arch-not-in-list not ok, got %+v", it)
	}
	if it.Detail != "tag good present, arch not in manifest list" {
		t.Errorf("detail = %q, want arch-not-in-list message", it.Detail)
	}
}

func TestRun_Aggregation(t *testing.T) {
	_, image := newRegistry(t)
	res, err := New().Run(context.Background(), plugin.Config{
		"image":  image,
		"tags":   []string{"good", "nope"},
		"arches": []string{"amd64", "arm64"},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	its := items(t, res)
	// good x {amd64,arm64} ok; nope x {amd64,arm64} not ok.
	if len(its) != 4 {
		t.Fatalf("got %d items, want 4", len(its))
	}
	okCount := 0
	for _, raw := range its {
		if raw.(checklistItem).OK {
			okCount++
		}
	}
	if okCount != 2 {
		t.Errorf("ok count = %d, want 2", okCount)
	}
	if allOK(t, res) {
		t.Errorf("all_ok = true, want false")
	}
}

func TestRun_NoArches(t *testing.T) {
	_, image := newRegistry(t)
	res, err := New().Run(context.Background(), plugin.Config{
		"image": image,
		"tags":  []string{"good", "nope"},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	its := items(t, res)
	if len(its) != 2 {
		t.Fatalf("got %d items, want 2", len(its))
	}
	good := its[0].(checklistItem)
	if !good.OK || good.Detail != "found: good" {
		t.Errorf("good item = %+v, want ok/found", good)
	}
	bad := its[1].(checklistItem)
	if bad.OK || bad.Detail != "not found" {
		t.Errorf("bad item = %+v, want not-ok/not found", bad)
	}
}

// TestRun_TagSourceVPrefix verifies that a tag derived from a GitHub repo's
// latest release is checked in both its vX.Y.Z and X.Y.Z forms: the release tag
// "vgood" is absent from the registry verbatim, but the stripped "good" exists,
// so the check passes against the stripped variant.
func TestRun_TagSourceVPrefix(t *testing.T) {
	_, image := newRegistry(t)

	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"vgood","html_url":"http://x"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer gh.Close()
	orig := plugins.GHBaseURL
	plugins.GHBaseURL = gh.URL
	defer func() { plugins.GHBaseURL = orig }()

	res, err := New().Run(context.Background(), plugin.Config{
		"image":      image,
		"tag_source": "owner/repo",
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	its := items(t, res)
	if len(its) != 1 {
		t.Fatalf("got %d items, want 1", len(its))
	}
	it := its[0].(checklistItem)
	if !it.OK {
		t.Errorf("expected ok via stripped variant, got %+v", it)
	}
	if it.Detail != "found: good" {
		t.Errorf("detail = %q, want found: good (stripped match)", it.Detail)
	}
	if !strings.Contains(it.Label, "vgood") || !strings.Contains(it.Label, "owner/repo") {
		t.Errorf("label = %q, want it to mention vgood and owner/repo", it.Label)
	}
	if !allOK(t, res) {
		t.Errorf("all_ok = false, want true")
	}
}

// TestRun_TagSourceOrManualRequired ensures Run errors when neither a manual tag
// nor a tag source is provided.
func TestRun_TagSourceOrManualRequired(t *testing.T) {
	_, image := newRegistry(t)
	if _, err := New().Run(context.Background(), plugin.Config{"image": image}); err == nil {
		t.Errorf("expected error when no tags and no tag_source given")
	}
}

func TestRun_MissingImage(t *testing.T) {
	if _, err := New().Run(context.Background(), plugin.Config{"tags": []string{"latest"}}); err == nil {
		t.Errorf("expected error for missing image")
	}
}
