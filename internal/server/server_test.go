package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestClearAllTrackers(t *testing.T) {
	srv := newTestServer(t)
	createTracker(t, srv, `{"plugin_id":"fake","name":"a","config":{}}`)
	createTracker(t, srv, `{"plugin_id":"fake","name":"b","config":{}}`)

	rec := do(t, srv, http.MethodPost, "/api/trackers/clear", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Cleared int `json:"cleared"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Cleared != 2 {
		t.Errorf("cleared = %d, want 2", resp.Cleared)
	}

	list, err := srv.store.ListTrackers()
	if err != nil {
		t.Fatalf("ListTrackers: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("after clear, %d trackers remain, want 0", len(list))
	}
}

func TestReloadNoConfig(t *testing.T) {
	srv := newTestServer(t) // no config path set
	rec := do(t, srv, http.MethodPost, "/api/trackers/reload", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("reload without config status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestReloadFromConfig(t *testing.T) {
	srv := newTestServer(t)
	cfgPath := t.TempDir() + "/plugdash.yaml"
	if err := os.WriteFile(cfgPath, []byte("trackers:\n  - key: r1\n    plugin: fake\n    name: Reloaded\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	srv.SetConfigPath(cfgPath)

	rec := do(t, srv, http.MethodPost, "/api/trackers/reload", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("reload status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	list, err := srv.store.ListTrackers()
	if err != nil {
		t.Fatalf("ListTrackers: %v", err)
	}
	if len(list) != 1 || list[0].Source != store.SourceFile || list[0].ConfigKey != "r1" {
		t.Fatalf("reloaded set = %+v, want one file tracker keyed r1", list)
	}

	// Reload again: idempotent, still one tracker (no duplicates).
	if rec := do(t, srv, http.MethodPost, "/api/trackers/reload", ""); rec.Code != http.StatusOK {
		t.Fatalf("second reload status = %d", rec.Code)
	}
	if list, _ := srv.store.ListTrackers(); len(list) != 1 {
		t.Errorf("reload duplicated trackers: %d, want 1", len(list))
	}
}

func TestImportTrackers(t *testing.T) {
	srv := newTestServer(t)
	yaml := "trackers:\n  - key: i1\n    plugin: fake\n    name: Imported\n    config:\n      url: https://x\n"
	rec := do(t, srv, http.MethodPost, "/api/trackers/import", yaml)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Loaded int `json:"loaded"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Loaded != 1 {
		t.Errorf("loaded = %d, want 1", resp.Loaded)
	}
	list, _ := srv.store.ListTrackers()
	if len(list) != 1 || list[0].Source != store.SourceFile {
		t.Fatalf("imported set = %+v, want one file tracker", list)
	}
}

func TestImportInvalid(t *testing.T) {
	srv := newTestServer(t)
	rec := do(t, srv, http.MethodPost, "/api/trackers/import", "trackers:\n  - name: no-plugin\n")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("import invalid status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestExportTrackers(t *testing.T) {
	srv := newTestServer(t)
	createTracker(t, srv, `{"plugin_id":"fake","name":"Exported","config":{"k":"v"}}`)

	rec := do(t, srv, http.MethodGet, "/api/trackers/export", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-yaml" {
		t.Errorf("content-type = %q, want application/x-yaml", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("content-disposition = %q, want an attachment", cd)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Exported") || !strings.Contains(body, "fake") {
		t.Errorf("export body missing tracker:\n%s", body)
	}
	if strings.Contains(body, "github_token") || strings.Contains(body, "settings") {
		t.Errorf("export leaked settings/secrets:\n%s", body)
	}
}

func TestDeleteFileTrackerAllowed(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.store.ReconcileFileTrackers([]store.FileTracker{{Key: "f1", PluginID: "fake", Name: "File One"}}); err != nil {
		t.Fatalf("ReconcileFileTrackers: %v", err)
	}
	list, _ := srv.store.ListTrackers()
	if len(list) != 1 {
		t.Fatalf("setup: %d trackers, want 1", len(list))
	}
	id := list[0].ID

	rec := do(t, srv, http.MethodDelete, "/api/trackers/"+strconv.FormatInt(id, 10), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete file tracker status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestUpdateFileTrackerForbidden(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.store.ReconcileFileTrackers([]store.FileTracker{{Key: "f1", PluginID: "fake", Name: "File One"}}); err != nil {
		t.Fatalf("ReconcileFileTrackers: %v", err)
	}
	list, _ := srv.store.ListTrackers()
	id := list[0].ID

	rec := do(t, srv, http.MethodPut, "/api/trackers/"+strconv.FormatInt(id, 10), `{"name":"hacked","config":{}}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("update file tracker status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestGetConfigStatus(t *testing.T) {
	srv := newTestServer(t)
	rec := do(t, srv, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("config status = %d, want 200", rec.Code)
	}
	var resp struct {
		Configured bool   `json:"configured"`
		Path       string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Configured {
		t.Errorf("configured = true, want false (no config set)")
	}

	srv.SetConfigPath("/tmp/x.yaml")
	rec = do(t, srv, http.MethodGet, "/api/config", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Configured || resp.Path != "/tmp/x.yaml" {
		t.Errorf("after SetConfigPath: %+v, want configured=true path=/tmp/x.yaml", resp)
	}
}
