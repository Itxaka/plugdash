// Package server exposes the plugdash HTTP API and serves the static frontend.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"plugdash/internal/config"
	"plugdash/internal/engine"
	"plugdash/internal/plugin"
	"plugdash/internal/store"
)

// PluginRescanner re-discovers external plugins on demand. The external plugin
// manager satisfies it; the server only needs this narrow view, so it avoids
// importing the extplugin package.
type PluginRescanner interface {
	Rescan() (added, removed int, err error)
	Dir() string
}

// externalMarker is implemented by external plugins so the server can flag them
// in the API without importing the extplugin package.
type externalMarker interface {
	IsExternal() bool
}

// Server wires the registry, store and static assets into an http.Handler.
type Server struct {
	reg       *plugin.Registry
	store     *store.Store
	static    fs.FS
	mux       *http.ServeMux
	rescanner PluginRescanner
	logger    *slog.Logger
	logs      *LogRing
	level     *slog.LevelVar
	engine    *engine.Engine // when set, runs are server-driven + cached
	// configPath is the declarative --config file the server was started with
	// (empty if none). It backs POST /api/trackers/reload, which re-reads it.
	configPath string
	// themesDir holds user-supplied theme CSS files (one *.css per theme),
	// served at /api/themes(.css). Empty if not configured.
	themesDir string
}

// SetConfigPath records the declarative config file path so /api/trackers/reload
// can re-reconcile from it and /api/config can report it. Empty disables reload.
func (s *Server) SetConfigPath(path string) { s.configPath = path }

// SetThemesDir records the directory of user theme CSS files served to the UI.
func (s *Server) SetThemesDir(dir string) { s.themesDir = dir }

// SetEngine wires the server-side run engine. When set, /api/run and
// /api/trackers/{id}/run serve cached snapshots and /api/stream pushes live
// updates; without it the server falls back to running trackers per request.
func (s *Server) SetEngine(e *engine.Engine) { s.engine = e }

// New builds a Server. static is the filesystem containing the frontend assets
// (typically an embedded web/ directory).
func New(reg *plugin.Registry, st *store.Store, static fs.FS) *Server {
	s := &Server{
		reg:    reg,
		store:  st,
		static: static,
		mux:    http.NewServeMux(),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), // overridden via SetLogger
	}
	s.routes()
	return s
}

// SetLogging wires the structured logger, its in-memory ring (served at
// /api/logs), and the dynamic level (toggled by the debug setting).
func (s *Server) SetLogging(logger *slog.Logger, ring *LogRing, level *slog.LevelVar) {
	if logger != nil {
		s.logger = logger
	}
	s.logs = ring
	s.level = level
}

// runCtx attaches a tracker-scoped logger to ctx so the plugin and the shared
// HTTP/registry helpers log under the tracker's identity.
func (s *Server) runCtx(ctx context.Context, t *store.Tracker) context.Context {
	return plugin.WithLogger(ctx, s.logger.With("tracker_id", t.ID, "plugin", t.PluginID, "tracker", t.Name))
}

// applyDebugLevel flips the dynamic log level when the debug setting changes.
func (s *Server) applyDebugLevel(debug bool) {
	if s.level == nil {
		return
	}
	if debug {
		s.level.Set(slog.LevelDebug)
	} else {
		s.level.Set(slog.LevelInfo)
	}
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// SetPluginRescanner wires in the external plugin manager so /api/plugins/rescan
// can trigger a re-scan. If never set, that endpoint reports external plugins
// are not enabled.
func (s *Server) SetPluginRescanner(p PluginRescanner) { s.rescanner = p }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/plugins", s.handleListPlugins)
	s.mux.HandleFunc("POST /api/plugins/rescan", s.handleRescanPlugins)
	s.mux.HandleFunc("GET /api/trackers", s.handleListTrackers)
	s.mux.HandleFunc("POST /api/trackers", s.handleCreateTracker)
	s.mux.HandleFunc("PUT /api/trackers/{id}", s.handleUpdateTracker)
	s.mux.HandleFunc("DELETE /api/trackers/{id}", s.handleDeleteTracker)
	s.mux.HandleFunc("POST /api/trackers/clear", s.handleClearTrackers)
	s.mux.HandleFunc("POST /api/trackers/reload", s.handleReloadTrackers)
	s.mux.HandleFunc("POST /api/trackers/import", s.handleImportTrackers)
	s.mux.HandleFunc("GET /api/trackers/export", s.handleExportTrackers)
	s.mux.HandleFunc("GET /api/config", s.handleGetConfig)
	s.mux.HandleFunc("GET /api/themes", s.handleListThemes)
	s.mux.HandleFunc("GET /api/themes.css", s.handleThemesCSS)
	s.mux.HandleFunc("GET /api/trackers/{id}/run", s.handleRunTracker)
	s.mux.HandleFunc("GET /api/run", s.handleRunAll)
	s.mux.HandleFunc("GET /api/stream", s.handleStream)
	s.mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	s.mux.HandleFunc("PUT /api/settings", s.handleSaveSettings)
	s.mux.HandleFunc("GET /api/logs", s.handleGetLogs)
	s.mux.HandleFunc("DELETE /api/logs", s.handleClearLogs)
	s.mux.Handle("/", http.FileServer(http.FS(s.static)))
}

// --- plugins ---

type pluginDTO struct {
	ID                     string               `json:"id"`
	Name                   string               `json:"name"`
	Description            string               `json:"description"`
	RefreshIntervalSeconds int                  `json:"refresh_interval_seconds"`
	External               bool                 `json:"external"`
	Width                  int                  `json:"width"`
	Height                 int                  `json:"height"`
	Schema                 []plugin.ConfigField `json:"schema"`
}

func (s *Server) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	plugins := s.reg.List()
	out := make([]pluginDTO, 0, len(plugins))
	for _, p := range plugins {
		external := false
		if em, ok := p.(externalMarker); ok {
			external = em.IsExternal()
		}
		size := plugin.SizeOf(p)
		out = append(out, pluginDTO{
			ID:                     p.ID(),
			Name:                   p.Name(),
			Description:            p.Description(),
			RefreshIntervalSeconds: int(p.RefreshInterval().Seconds()),
			External:               external,
			Width:                  size.Width,
			Height:                 size.Height,
			Schema:                 p.ConfigSchema(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRescanPlugins(w http.ResponseWriter, r *http.Request) {
	if s.rescanner == nil {
		writeError(w, http.StatusNotImplemented, "external plugins are not enabled")
		return
	}
	added, removed, err := s.rescanner.Rescan()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"added":   added,
		"removed": removed,
		"dir":     s.rescanner.Dir(),
	})
}

// --- logs ---

func (s *Server) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		writeJSON(w, http.StatusOK, []LogEntry{})
		return
	}
	entries := s.logs.Entries()
	debug := s.level != nil && s.level.Level() <= slog.LevelDebug
	writeJSON(w, http.StatusOK, map[string]any{
		"debug":   debug,
		"entries": entries,
	})
}

func (s *Server) handleClearLogs(w http.ResponseWriter, r *http.Request) {
	if s.logs != nil {
		s.logs.Clear()
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- settings ---

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var in store.Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	saved, err := s.store.SaveSettings(in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.applyDebugLevel(saved.Debug)
	// A globally-configured token becomes GITHUB_TOKEN for all GitHub plugins.
	if saved.GitHubToken != "" {
		_ = os.Setenv("GITHUB_TOKEN", saved.GitHubToken)
	}
	// Emit an immediate entry so the Logs tab visibly reflects the change without
	// waiting for the next tracker run (Info is captured at both levels).
	s.logger.Info("settings updated", "debug", saved.Debug, "autorefresh", saved.AutoRefreshEnabled, "github_token_set", saved.GitHubToken != "")
	writeJSON(w, http.StatusOK, saved)
}

// --- trackers ---

func (s *Server) handleListTrackers(w http.ResponseWriter, r *http.Request) {
	trackers, err := s.store.ListTrackers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if trackers == nil {
		trackers = []*store.Tracker{}
	}
	writeJSON(w, http.StatusOK, trackers)
}

type createTrackerReq struct {
	PluginID               string         `json:"plugin_id"`
	Name                   string         `json:"name"`
	Config                 map[string]any `json:"config"`
	RefreshIntervalSeconds int            `json:"refresh_interval_seconds"`
}

func (s *Server) handleCreateTracker(w http.ResponseWriter, r *http.Request) {
	var req createTrackerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.PluginID) == "" {
		writeError(w, http.StatusBadRequest, "plugin_id is required")
		return
	}
	if _, ok := s.reg.Get(req.PluginID); !ok {
		writeError(w, http.StatusBadRequest, "unknown plugin: "+req.PluginID)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = req.PluginID
	}
	t, err := s.store.CreateTracker(req.PluginID, req.Name, req.Config, req.RefreshIntervalSeconds)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reconcileEngine()
	writeJSON(w, http.StatusCreated, t)
}

// reconcileEngine refreshes the engine's tracker set after a CRUD change.
func (s *Server) reconcileEngine() {
	if s.engine != nil {
		_ = s.engine.Reconcile()
	}
}

func (s *Server) handleUpdateTracker(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req createTrackerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if existing, gerr := s.store.GetTracker(id); gerr == nil {
		if existing.Source == store.SourceFile {
			writeError(w, http.StatusForbidden, "tracker is managed by config and cannot be edited from the UI")
			return
		}
		if name == "" {
			// Keep the existing name rather than blanking it.
			name = existing.Name
		}
	}
	t, err := s.store.UpdateTracker(id, name, req.Config, req.RefreshIntervalSeconds)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tracker not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reconcileEngine()
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDeleteTracker(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// File-managed trackers are deletable (Clear and the "reload from file" flow
	// rely on this); only editing them stays blocked, since a reload would just
	// overwrite an edit. The on-disk config is untouched, so reload/restart
	// restores anything removed here.
	if err := s.store.DeleteTracker(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tracker not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reconcileEngine()
	w.WriteHeader(http.StatusNoContent)
}

// maxImportBytes caps the size of an uploaded/pasted config to keep a hostile
// or accidental large body from exhausting memory.
const maxImportBytes = 1 << 20 // 1 MiB

// handleClearTrackers deletes every tracker (db and file alike). The on-disk
// config is left untouched, so a reload or restart restores file trackers.
func (s *Server) handleClearTrackers(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.ClearTrackers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reconcileEngine()
	s.logger.Info("trackers cleared", "count", n)
	writeJSON(w, http.StatusOK, map[string]any{"cleared": n})
}

// handleReloadTrackers re-reads the declarative --config file from disk and
// reconciles its trackers (idempotent: dedup by key, no duplicates). It is a
// no-op-safe way to restore file trackers after they were cleared or removed.
func (s *Server) handleReloadTrackers(w http.ResponseWriter, r *http.Request) {
	if s.configPath == "" {
		writeError(w, http.StatusConflict, "no config file configured (start plugdash with --config to enable reload)")
		return
	}
	c, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.ReconcileFileTrackers(c.FileTrackers()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reconcileEngine()
	s.logger.Info("trackers reloaded from config", "path", s.configPath, "count", len(c.Trackers))
	writeJSON(w, http.StatusOK, map[string]any{"trackers": len(c.Trackers)})
}

// handleImportTrackers loads trackers from a config document supplied in the
// request body (uploaded file or pasted text). They are reconciled as
// file-sourced trackers, so they are session-only: a restart's startup reconcile
// (against the original --config, or nothing) reverts them.
func (s *Server) handleImportTrackers(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxImportBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read body")
		return
	}
	c, err := config.Parse(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.ReconcileFileTrackers(c.FileTrackers()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reconcileEngine()
	s.logger.Info("trackers imported", "count", len(c.Trackers))
	writeJSON(w, http.StatusOK, map[string]any{"loaded": len(c.Trackers)})
}

// handleExportTrackers serializes the current trackers as a downloadable config
// YAML (trackers only — no settings, so secrets never leave in a dump).
func (s *Server) handleExportTrackers(w http.ResponseWriter, r *http.Request) {
	trackers, err := s.store.ListTrackers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out, err := config.Marshal(trackers)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="plugdash-trackers.yaml"`)
	_, _ = w.Write(out)
}

// handleGetConfig reports whether a declarative config file is configured, so
// the UI can enable or disable the "reload from file" action.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": s.configPath != "",
		"path":       s.configPath,
	})
}

// --- user themes ---
//
// Each *.css file in the themes directory is one theme. The file name (minus
// .css, restricted to [A-Za-z0-9_-]) is the theme id, and the file must target
// `[data-theme="<id>"]` (the variables it overrides — see docs). The id keys the
// picker; the label comes from a `/* plugdash-theme: Name */` header, else the
// prettified id.

const maxThemeBytes = 512 << 10 // 512 KiB per theme file

type themeDTO struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// themeID validates/normalizes a file's base name into a CSS-safe theme id,
// returning ok=false for names with unsupported characters.
func themeID(filename string) (string, bool) {
	id := strings.TrimSuffix(filename, ".css")
	if id == "" {
		return "", false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "", false
		}
	}
	return id, true
}

// themeLabel extracts a `/* plugdash-theme: Name */` header from the CSS, else
// prettifies the id (foo-bar -> "Foo Bar").
func themeLabel(css, id string) string {
	const marker = "plugdash-theme:"
	if i := strings.Index(css, marker); i >= 0 {
		rest := css[i+len(marker):]
		if end := strings.Index(rest, "*/"); end >= 0 {
			if name := strings.TrimSpace(rest[:end]); name != "" {
				return name
			}
		}
	}
	words := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	if len(words) == 0 {
		return id
	}
	return strings.Join(words, " ")
}

// themeFiles returns the *.css theme files in themesDir, sorted by name, paired
// with a valid id. A missing/empty dir yields none.
func (s *Server) themeFiles() []struct{ id, path string } {
	var out []struct{ id, path string }
	if s.themesDir == "" {
		return out
	}
	entries, err := os.ReadDir(s.themesDir)
	if err != nil {
		return out
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".css") {
			continue
		}
		id, ok := themeID(e.Name())
		if !ok {
			continue
		}
		out = append(out, struct{ id, path string }{id, filepath.Join(s.themesDir, e.Name())})
	}
	return out
}

// handleListThemes lists the user themes (id + label) for the Settings picker.
func (s *Server) handleListThemes(w http.ResponseWriter, r *http.Request) {
	out := []themeDTO{}
	for _, f := range s.themeFiles() {
		head := make([]byte, 256)
		if fh, err := os.Open(f.path); err == nil {
			n, _ := fh.Read(head)
			head = head[:n]
			fh.Close()
		}
		out = append(out, themeDTO{ID: f.id, Label: themeLabel(string(head), f.id)})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleThemesCSS serves every user theme file concatenated as one stylesheet,
// linked from the page so the themes are available to apply. Always 200 (empty
// when no themes dir / no files).
func (s *Server) handleThemesCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	for _, f := range s.themeFiles() {
		fh, err := os.Open(f.path)
		if err != nil {
			continue
		}
		_, _ = w.Write([]byte("\n/* theme: " + f.id + " */\n"))
		_, _ = io.Copy(w, io.LimitReader(fh, maxThemeBytes))
		fh.Close()
	}
}

// runResponse carries either a plugin Result or an error string for a single
// tracker run.
type runResponse struct {
	TrackerID              int64          `json:"tracker_id"`
	Name                   string         `json:"name"`
	PluginID               string         `json:"plugin_id"`
	RefreshIntervalSeconds int            `json:"refresh_interval_seconds"`
	Result                 *plugin.Result `json:"result,omitempty"`
	Error                  string         `json:"error,omitempty"`
}

func (s *Server) handleRunTracker(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	// Engine path: serve the cached snapshot; ?force=true enqueues an immediate
	// run (the live result arrives over /api/stream).
	if s.engine != nil {
		if r.URL.Query().Get("force") == "true" {
			s.engine.Force(id)
		}
		if snap, ok := s.engine.Snapshot(id); ok {
			writeJSON(w, http.StatusOK, snap)
			return
		}
		// Not run yet: confirm the tracker exists, then report it's pending.
		if _, gerr := s.store.GetTracker(id); errors.Is(gerr, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tracker not found")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"tracker_id": id, "pending": true})
		return
	}

	t, err := s.store.GetTracker(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tracker not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := runTracker(s.runCtx(r.Context(), t), s.reg, t)
	writeJSON(w, http.StatusOK, resp)
}

// handleStream is the SSE endpoint: it pushes cached snapshots immediately on
// connect, then streams each tracker result as the engine produces it. An open
// connection counts as a present client, so the engine schedules while at least
// one stream is connected and idles otherwise.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusNotImplemented, "live updates not enabled")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx)

	ch, unsub := s.engine.Subscribe()
	defer unsub()

	// A periodic comment keeps the connection alive through idle proxies.
	ka := time.NewTicker(25 * time.Second)
	defer ka.Stop()

	_, _ = w.Write([]byte("retry: 3000\n\n"))
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ka.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case frame, open := <-ch:
			if !open {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleRunAll runs every configured tracker concurrently and returns their
// results in a single JSON array. Concurrency is capped so a large number of
// trackers does not open an unbounded number of goroutines or connections.
// Results preserve tracker order; per-tracker failures are captured in each
// runResponse rather than failing the whole request.
func (s *Server) handleRunAll(w http.ResponseWriter, r *http.Request) {
	// Engine path: serve the shared cached snapshots (one upstream call serves
	// every client) instead of running per request.
	if s.engine != nil {
		s.engine.Poll() // count this as presence (SSE-fallback pollers)
		snaps := s.engine.SnapshotAll()
		if snaps == nil {
			snaps = []*engine.Snapshot{}
		}
		writeJSON(w, http.StatusOK, snaps)
		return
	}

	trackers, err := s.store.ListTrackers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	results := make([]runResponse, len(trackers))

	const maxConcurrent = 8
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for i, t := range trackers {
		wg.Add(1)
		go func(i int, t *store.Tracker) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = runTracker(s.runCtx(r.Context(), t), s.reg, t)
		}(i, t)
	}
	wg.Wait()

	writeJSON(w, http.StatusOK, results)
}

// runTracker executes a single tracker, capturing any error into the response
// rather than failing the whole request. It applies a per-run timeout.
func runTracker(ctx context.Context, reg *plugin.Registry, t *store.Tracker) runResponse {
	resp := runResponse{TrackerID: t.ID, Name: t.Name, PluginID: t.PluginID}
	p, ok := reg.Get(t.PluginID)
	if !ok {
		resp.Error = "plugin not found: " + t.PluginID
		return resp
	}
	// Effective cadence: the tracker's override if set, else the plugin default.
	if t.RefreshIntervalSeconds > 0 {
		resp.RefreshIntervalSeconds = t.RefreshIntervalSeconds
	} else {
		resp.RefreshIntervalSeconds = int(p.RefreshInterval().Seconds())
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	log := plugin.LoggerFrom(ctx)
	start := time.Now()
	log.Debug("tracker run start")
	res, err := p.Run(ctx, plugin.Config(t.Config))
	log.Debug("tracker run done", "ms", time.Since(start).Milliseconds(), "error", errString(err))
	if err != nil {
		resp.Error = err.Error()
		return resp
	}
	resp.Result = &res
	return resp
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// errString renders an error for structured logging, "" when nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
