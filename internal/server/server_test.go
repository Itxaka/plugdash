package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/store"
)

// fakePlugin is a minimal plugin.Plugin used to drive the server tests. It
// returns a simple VizStat result, unless its config carries {"fail": true},
// in which case Run returns an error.
type fakePlugin struct{}

func (fakePlugin) ID() string          { return "fake" }
func (fakePlugin) Name() string        { return "Fake Plugin" }
func (fakePlugin) Description() string { return "a test plugin" }

func (fakePlugin) ConfigSchema() []plugin.ConfigField {
	return []plugin.ConfigField{
		{Key: "fail", Label: "Fail", Type: plugin.FieldBool},
	}
}

func (fakePlugin) RefreshInterval() time.Duration { return time.Minute }

func (fakePlugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	if cfg.Bool("fail") {
		return plugin.Result{}, context.Canceled // any non-nil error
	}
	return plugin.Result{
		Visualization: plugin.VizStat,
		Title:         "Fake",
		Data:          map[string]any{"value": 42, "label": "answer"},
	}, nil
}

// newTestServer builds a Server backed by a fresh registry (with the fake
// plugin registered), a real on-disk store in a temp dir, and an empty static
// filesystem.
func newTestServer(t *testing.T) *Server {
	t.Helper()

	reg := plugin.NewRegistry()
	reg.Register(fakePlugin{})

	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	return New(reg, st, fstest.MapFS{})
}

// do issues a request against the server and returns the recorder.
func do(t *testing.T, srv *Server, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

// createTracker POSTs a tracker and returns its decoded id.
func createTracker(t *testing.T, srv *Server, body string) int64 {
	t.Helper()
	rec := do(t, srv, http.MethodPost, "/api/trackers", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tracker: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var tr store.Tracker
	if err := json.Unmarshal(rec.Body.Bytes(), &tr); err != nil {
		t.Fatalf("decode created tracker: %v (body %s)", err, rec.Body.String())
	}
	if tr.ID == 0 {
		t.Fatalf("created tracker has zero id: %s", rec.Body.String())
	}
	return tr.ID
}

func TestListPlugins(t *testing.T) {
	srv := newTestServer(t)
	rec := do(t, srv, http.MethodGet, "/api/plugins", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []pluginDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	found := false
	for _, p := range got {
		if p.ID == "fake" {
			found = true
		}
	}
	if !found {
		t.Fatalf("plugin list missing fake plugin: %s", rec.Body.String())
	}
}

func TestCreateTracker(t *testing.T) {
	srv := newTestServer(t)
	id := createTracker(t, srv, `{"plugin_id":"fake","name":"t1","config":{}}`)
	if id <= 0 {
		t.Fatalf("got id %d, want positive", id)
	}
}

func TestCreateTrackerUnknownPlugin(t *testing.T) {
	srv := newTestServer(t)
	rec := do(t, srv, http.MethodPost, "/api/trackers", `{"plugin_id":"nope","name":"t1","config":{}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestCreateTrackerInvalidJSON(t *testing.T) {
	srv := newTestServer(t)
	rec := do(t, srv, http.MethodPost, "/api/trackers", `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestListTrackers(t *testing.T) {
	srv := newTestServer(t)
	id := createTracker(t, srv, `{"plugin_id":"fake","name":"t1","config":{}}`)

	rec := do(t, srv, http.MethodGet, "/api/trackers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []store.Tracker
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	found := false
	for _, tr := range got {
		if tr.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("tracker list missing created tracker %d: %s", id, rec.Body.String())
	}
}

func TestRunTrackerSuccess(t *testing.T) {
	srv := newTestServer(t)
	id := createTracker(t, srv, `{"plugin_id":"fake","name":"t1","config":{}}`)

	rec := do(t, srv, http.MethodGet, "/api/trackers/"+strconv.FormatInt(id, 10)+"/run", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp runResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if resp.Error != "" {
		t.Fatalf("unexpected error in run response: %q", resp.Error)
	}
	if resp.Result == nil {
		t.Fatalf("result is nil: %s", rec.Body.String())
	}
	if resp.Result.Visualization != plugin.VizStat {
		t.Fatalf("visualization = %q, want %q", resp.Result.Visualization, plugin.VizStat)
	}
}

func TestRunTrackerError(t *testing.T) {
	srv := newTestServer(t)
	id := createTracker(t, srv, `{"plugin_id":"fake","name":"boom","config":{"fail":true}}`)

	rec := do(t, srv, http.MethodGet, "/api/trackers/"+strconv.FormatInt(id, 10)+"/run", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (run errors are still 200)", rec.Code)
	}
	var resp runResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if resp.Error == "" {
		t.Fatalf("expected non-empty error field, got: %s", rec.Body.String())
	}
}

func TestRunTrackerMissing(t *testing.T) {
	srv := newTestServer(t)
	rec := do(t, srv, http.MethodGet, "/api/trackers/99999/run", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteTracker(t *testing.T) {
	srv := newTestServer(t)
	id := createTracker(t, srv, `{"plugin_id":"fake","name":"t1","config":{}}`)
	path := "/api/trackers/" + strconv.FormatInt(id, 10)

	rec := do(t, srv, http.MethodDelete, path, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}

	rec = do(t, srv, http.MethodDelete, path, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}
