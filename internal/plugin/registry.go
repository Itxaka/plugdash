package plugin

import (
	"fmt"
	"sort"
	"sync"
)

// Registry holds the set of available plugins keyed by ID. It is safe for
// concurrent use.
type Registry struct {
	mu      sync.RWMutex
	plugins map[string]Plugin
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{plugins: make(map[string]Plugin)}
}

// Register adds p to the registry. It panics on duplicate IDs, since that is a
// programming error discovered at startup.
func (r *Registry) Register(p Plugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[p.ID()]; exists {
		panic(fmt.Sprintf("plugin %q already registered", p.ID()))
	}
	r.plugins[p.ID()] = p
}

// Unregister removes the plugin with the given id, if present. It is used to
// drop or replace external plugins during a rescan; built-in plugins are never
// unregistered. Unknown ids are a no-op.
func (r *Registry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.plugins, id)
}

// Get returns the plugin with the given id and whether it was found.
func (r *Registry) Get(id string) (Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.plugins[id]
	return p, ok
}

// List returns all registered plugins sorted by ID.
func (r *Registry) List() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}
