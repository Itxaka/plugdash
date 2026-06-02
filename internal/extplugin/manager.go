package extplugin

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"plugdash/internal/plugin"
)

// namePrefix is required on every external plugin executable. It avoids
// accidentally executing unrelated files that happen to live in the plugins
// directory.
const namePrefix = "plugdash-plugin-"

// Manager owns the lifecycle of external plugins: it discovers executables in a
// directory, registers them into the shared registry, and can rescan to pick up
// added/removed/changed plugins without restarting plugdash. Built-in plugins
// in the same registry are never touched.
type Manager struct {
	dir string
	reg *plugin.Registry

	mu sync.Mutex
	// owned maps the id of each currently-registered external plugin to the
	// executable path it came from.
	owned map[string]string
}

// NewManager returns a Manager that will manage external plugins found in dir,
// registering them into reg.
func NewManager(dir string, reg *plugin.Registry) *Manager {
	return &Manager{dir: dir, reg: reg, owned: map[string]string{}}
}

// Dir returns the configured plugins directory.
func (m *Manager) Dir() string { return m.dir }

// Load performs the initial discovery + registration. It is equivalent to a
// first Rescan and returns the number of external plugins registered.
func (m *Manager) Load() (int, error) {
	added, _, err := m.Rescan()
	return added, err
}

// Rescan re-reads the plugins directory and reconciles the registry: newly
// discovered binaries are registered, vanished ones unregistered, and existing
// external plugins refreshed (re-described). It returns how many were added and
// removed. A plugin whose `describe` fails is skipped with a logged warning and
// never aborts the scan. An id that collides with a built-in plugin is skipped.
func (m *Manager) Rescan() (added, removed int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	discovered, warnings := discoverDir(m.dir)
	for _, w := range warnings {
		log.Printf("extplugin: %v", w)
	}

	seen := make(map[string]string, len(discovered))
	for _, p := range discovered {
		id := p.ID()
		if existing, ok := m.reg.Get(id); ok {
			if _, isExternal := existing.(*ExternalPlugin); !isExternal {
				log.Printf("extplugin: skipping %s: id %q collides with a built-in plugin", p.path, id)
				continue
			}
			// Replace the existing external plugin (metadata/path may have changed).
			m.reg.Unregister(id)
			if _, wasOwned := m.owned[id]; !wasOwned {
				added++
			}
		} else {
			added++
		}
		m.reg.Register(p)
		seen[id] = p.path
	}

	// Unregister external plugins that disappeared since the last scan.
	for id := range m.owned {
		if _, ok := seen[id]; !ok {
			m.reg.Unregister(id)
			removed++
		}
	}

	m.owned = seen
	return added, removed, nil
}

// discoverDir scans dir for executable files named "plugdash-plugin-*", runs
// `describe` on each, and returns the resulting plugins plus a list of
// non-fatal warnings for entries that were skipped. A missing directory yields
// no plugins and no warnings.
func discoverDir(dir string) (plugins []*ExternalPlugin, warnings []error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("read plugins dir %s: %w", dir, err)}
	}

	// Deterministic order so id-collision resolution is stable.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasPrefix(name, namePrefix) {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := ent.Info()
		if err != nil {
			warnings = append(warnings, fmt.Errorf("stat %s: %w", path, err))
			continue
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			warnings = append(warnings, fmt.Errorf("skipping %s: not an executable file", path))
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), describeTimeout)
		meta, derr := describe(ctx, path)
		cancel()
		if derr != nil {
			warnings = append(warnings, fmt.Errorf("skipping %s: %w", path, derr))
			continue
		}
		plugins = append(plugins, &ExternalPlugin{path: path, meta: meta})
	}
	return plugins, warnings
}
