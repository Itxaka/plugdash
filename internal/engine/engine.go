// Package engine runs trackers on the server, on each tracker's own cadence,
// and caches the latest result so any number of connected clients share a single
// upstream call. It is presence-gated: it only runs while at least one client is
// connected (subscribed to the SSE stream), and goes fully idle otherwise — there
// is no point polling external APIs when nobody is looking, and no history is
// kept (plugdash is real-time only).
package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"plugdash/internal/plugin"
	"plugdash/internal/store"
)

const (
	// tick is how often the scheduler evaluates which trackers are due. It is
	// fine-grained; actual cadence is each tracker's effective interval.
	tick = 1 * time.Second
	// maxConcurrent caps simultaneous tracker runs.
	maxConcurrent = 8
	// runTimeout bounds a single tracker run.
	runTimeout = 30 * time.Second
	// subBuffer is the per-subscriber send buffer; a slow client drops frames
	// rather than blocking the engine.
	subBuffer = 64
	// coldStartSpread bounds how far first runs are staggered. Trackers that
	// have never run become due at startedAt+offset (offset derived from the
	// tracker id, capped to its interval), so same-interval trackers don't fire
	// in one synchronized burst — yet every widget still gets its first data
	// within this window. Their later runs anchor to these offset first-run
	// times and stay de-aligned. Kept short so slow-interval widgets don't sit
	// blank for long on first paint.
	coldStartSpread = 10 * time.Second
)

// Snapshot is the cached outcome of a tracker run, served to clients and pushed
// over SSE. It mirrors the old runResponse plus a FetchedAt timestamp.
type Snapshot struct {
	TrackerID              int64          `json:"tracker_id"`
	Name                   string         `json:"name"`
	PluginID               string         `json:"plugin_id"`
	RefreshIntervalSeconds int            `json:"refresh_interval_seconds"`
	Result                 *plugin.Result `json:"result,omitempty"`
	Error                  string         `json:"error,omitempty"`
	FetchedAt              time.Time      `json:"fetched_at"`
}

// Engine schedules tracker runs and fans results out to subscribers.
type Engine struct {
	reg    *plugin.Registry
	store  *store.Store
	logger *slog.Logger

	mu       sync.Mutex
	snaps    map[int64]*Snapshot
	lastRun  map[int64]time.Time
	running  map[int64]bool
	trackers []*store.Tracker
	subs     map[chan []byte]struct{}

	lastPoll  time.Time // last cached-poll fallback request (counts as presence)
	startedAt time.Time // when Start ran; anchors first-run stagger

	sem    chan struct{}
	wake   chan struct{} // nudge the scheduler (new subscriber / reconcile / force)
	forceC chan int64
	stop   chan struct{}
}

// pollPresenceTTL is how long a cached-poll request keeps the engine considered
// "watched" — covers clients that fell back from SSE to polling /api/run.
const pollPresenceTTL = 20 * time.Second

// New builds an engine. Call Reconcile to load the initial tracker set, then
// Start to begin scheduling.
func New(reg *plugin.Registry, st *store.Store, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(noopWriter{}, nil))
	}
	return &Engine{
		reg:     reg,
		store:   st,
		logger:  logger,
		snaps:   map[int64]*Snapshot{},
		lastRun: map[int64]time.Time{},
		running: map[int64]bool{},
		subs:    map[chan []byte]struct{}{},
		sem:     make(chan struct{}, maxConcurrent),
		wake:    make(chan struct{}, 1),
		forceC:  make(chan int64, 64),
		stop:    make(chan struct{}),
	}
}

type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }

// Start launches the scheduler loop. Stop ends it.
func (e *Engine) Start() {
	e.startedAt = time.Now()
	_ = e.Reconcile()
	go e.loop()
}

// Stop ends the scheduler loop.
func (e *Engine) Stop() { close(e.stop) }

// clientCount returns the effective number of present clients: open SSE
// subscribers plus a recent cached-poll fallback request.
func (e *Engine) clientCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := len(e.subs)
	if !e.lastPoll.IsZero() && time.Since(e.lastPoll) < pollPresenceTTL {
		n++
	}
	return n
}

// Poll records a cached-poll fallback request, keeping the engine scheduling for
// clients that can't use SSE. Call from the cached /api/run handler.
func (e *Engine) Poll() {
	e.mu.Lock()
	first := e.lastPoll.IsZero() || time.Since(e.lastPoll) >= pollPresenceTTL
	e.lastPoll = time.Now()
	e.mu.Unlock()
	if first {
		e.nudge() // a returning poller should trigger due runs promptly
	}
}

// loop is the scheduler. While clients are connected it runs due trackers each
// tick; with no clients it idles (no upstream calls). It also services force
// requests immediately (those run regardless of presence — a user explicitly
// asked).
func (e *Engine) loop() {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case id := <-e.forceC:
			e.runDue(true, id)
		case <-e.wake:
			if e.clientCount() > 0 {
				e.runDue(false, 0)
			}
		case <-t.C:
			if e.clientCount() > 0 {
				e.runDue(false, 0)
			}
		}
	}
}

// runDue starts runs for trackers that are due. When forceID != 0 it runs only
// that tracker, ignoring its interval. Otherwise it runs every tracker whose
// effective interval has elapsed since its last run (or that never ran).
func (e *Engine) runDue(force bool, forceID int64) {
	now := time.Now()
	e.mu.Lock()
	trackers := e.trackers
	e.mu.Unlock()

	for _, t := range trackers {
		if forceID != 0 && t.ID != forceID {
			continue
		}
		interval := e.effectiveInterval(t)
		e.mu.Lock()
		last := e.lastRun[t.ID]
		var due bool
		switch {
		case force:
			due = true
		case last.IsZero():
			// Never ran: hold off until this tracker's staggered slot so a fleet
			// of same-interval trackers doesn't stampede on the first tick.
			due = now.Sub(e.startedAt) >= phaseOffset(t.ID, interval)
		default:
			due = now.Sub(last) >= time.Duration(interval)*time.Second
		}
		if due && !e.running[t.ID] {
			e.running[t.ID] = true
		} else {
			due = false
		}
		e.mu.Unlock()
		if !due {
			continue
		}
		go e.runOne(t)
	}
}

// phaseOffset returns a stable per-tracker delay within [0, spread), where
// spread is coldStartSpread capped to the tracker's own interval. A multiplicative
// hash of the id spreads trackers across the window deterministically (same id →
// same slot across restarts), avoiding a synchronized first-run burst without any
// randomness (which would also defeat resume/replay determinism).
func phaseOffset(id int64, intervalSec int) time.Duration {
	// A 0/sub-second interval means "run as fast as possible" — never stagger it.
	if intervalSec <= 0 {
		return 0
	}
	spread := coldStartSpread
	if d := time.Duration(intervalSec) * time.Second; d < spread {
		spread = d
	}
	if spread <= 0 {
		return 0
	}
	mixed := uint64(id) * 2654435761 // Knuth multiplicative hash
	return time.Duration(mixed % uint64(spread))
}

// effectiveInterval is the tracker override if set, else the plugin default.
func (e *Engine) effectiveInterval(t *store.Tracker) int {
	if t.RefreshIntervalSeconds > 0 {
		return t.RefreshIntervalSeconds
	}
	if p, ok := e.reg.Get(t.PluginID); ok {
		return int(p.RefreshInterval().Seconds())
	}
	return 60
}

// runOne executes a single tracker, caches the snapshot, and broadcasts it.
func (e *Engine) runOne(t *store.Tracker) {
	e.sem <- struct{}{}
	defer func() {
		<-e.sem
		e.mu.Lock()
		e.running[t.ID] = false
		e.lastRun[t.ID] = time.Now()
		e.mu.Unlock()
	}()

	snap := &Snapshot{
		TrackerID:              t.ID,
		Name:                   t.Name,
		PluginID:               t.PluginID,
		RefreshIntervalSeconds: e.effectiveInterval(t),
		FetchedAt:              time.Now(),
	}

	p, ok := e.reg.Get(t.PluginID)
	if !ok {
		snap.Error = "plugin not found: " + t.PluginID
	} else {
		ctx := plugin.WithLogger(context.Background(),
			e.logger.With("tracker_id", t.ID, "plugin", t.PluginID, "tracker", t.Name))
		ctx, cancel := context.WithTimeout(ctx, runTimeout)
		log := plugin.LoggerFrom(ctx)
		start := time.Now()
		log.Debug("tracker run start")
		res, err := p.Run(ctx, plugin.Config(t.Config))
		log.Debug("tracker run done", "ms", time.Since(start).Milliseconds(), "error", errString(err))
		cancel()
		snap.FetchedAt = time.Now()
		if err != nil {
			snap.Error = err.Error()
		} else {
			snap.Result = &res
		}
	}

	e.mu.Lock()
	e.snaps[t.ID] = snap
	e.mu.Unlock()
	e.broadcast(snap)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Reconcile reloads the tracker set from the store, dropping cached snapshots
// for trackers that no longer exist or whose config changed. New trackers will
// run on the next tick. Call after any tracker create/update/delete.
func (e *Engine) Reconcile() error {
	trackers, err := e.store.ListTrackers()
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	keep := make(map[int64]bool, len(trackers))
	prev := map[int64]*store.Tracker{}
	for _, t := range e.trackers {
		prev[t.ID] = t
	}
	for _, t := range trackers {
		keep[t.ID] = true
		// If config/interval changed, invalidate so it re-runs and isn't served
		// stale against the old config.
		if old, ok := prev[t.ID]; ok && trackerChanged(old, t) {
			delete(e.snaps, t.ID)
			delete(e.lastRun, t.ID)
		}
	}
	for id := range e.snaps {
		if !keep[id] {
			delete(e.snaps, id)
			delete(e.lastRun, id)
		}
	}
	e.trackers = trackers
	e.nudge()
	return nil
}

func trackerChanged(a, b *store.Tracker) bool {
	if a.RefreshIntervalSeconds != b.RefreshIntervalSeconds || a.PluginID != b.PluginID {
		return true
	}
	ab, _ := json.Marshal(a.Config)
	bb, _ := json.Marshal(b.Config)
	return string(ab) != string(bb)
}

// Force enqueues an immediate run of one tracker (used by the per-widget refresh
// button), regardless of presence or interval.
func (e *Engine) Force(id int64) {
	select {
	case e.forceC <- id:
	default:
	}
}

// nudge wakes the scheduler without blocking.
func (e *Engine) nudge() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Snapshot returns the cached snapshot for a tracker, if any.
func (e *Engine) Snapshot(id int64) (*Snapshot, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.snaps[id]
	return s, ok
}

// SnapshotAll returns the cached snapshots for the current tracker set, in the
// trackers' order. Trackers that have not run yet are omitted.
func (e *Engine) SnapshotAll() []*Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*Snapshot, 0, len(e.trackers))
	for _, t := range e.trackers {
		if s, ok := e.snaps[t.ID]; ok {
			out = append(out, s)
		}
	}
	return out
}

// Subscribe registers an SSE client. It returns a channel of pre-encoded SSE
// frames, pre-loaded with the current snapshots so the client renders
// immediately, and an unsubscribe func. Subscribing counts as presence and wakes
// the scheduler (so a first viewer triggers an immediate refresh of due work).
func (e *Engine) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, subBuffer)
	e.mu.Lock()
	// Replay current snapshots to the new subscriber.
	for _, t := range e.trackers {
		if s, ok := e.snaps[t.ID]; ok {
			if frame := encodeFrame(s); frame != nil {
				select {
				case ch <- frame:
				default:
				}
			}
		}
	}
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	e.nudge()

	unsub := func() {
		e.mu.Lock()
		if _, ok := e.subs[ch]; ok {
			delete(e.subs, ch)
			close(ch)
		}
		e.mu.Unlock()
	}
	return ch, unsub
}

// broadcast sends a snapshot frame to every subscriber (non-blocking).
func (e *Engine) broadcast(s *Snapshot) {
	frame := encodeFrame(s)
	if frame == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for ch := range e.subs {
		select {
		case ch <- frame:
		default: // slow client: drop this frame
		}
	}
}

// encodeFrame renders a snapshot as an SSE "snapshot" event frame.
func encodeFrame(s *Snapshot) []byte {
	data, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	out := make([]byte, 0, len(data)+32)
	out = append(out, "event: snapshot\ndata: "...)
	out = append(out, data...)
	out = append(out, "\n\n"...)
	return out
}
