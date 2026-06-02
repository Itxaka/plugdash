package extplugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"plugdash/internal/plugin"
)

// writeScript creates an executable shell script in dir and returns its path.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// WriteFile perm is subject to umask; force the executable bits.
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	return path
}

// goodPlugin emits valid describe metadata and a static run Result. On run it
// also writes the raw stdin config to $PLUGDASH_TEST_OUT (when set) so a test
// can confirm config passthrough without trying to embed JSON-in-JSON.
const goodPlugin = `#!/bin/sh
case "$1" in
describe)
  printf '%s' '{"id":"ext-demo","name":"Demo","description":"d","refresh_interval_seconds":45,"schema":[{"key":"x","label":"X","type":"string"}]}'
  ;;
run)
  cfg=$(cat)
  if [ -n "$PLUGDASH_TEST_OUT" ]; then printf '%s' "$cfg" > "$PLUGDASH_TEST_OUT"; fi
  printf '%s' '{"visualization":"stat","title":"T","data":{"value":1,"label":"ok"}}'
  ;;
esac
`

func TestDiscoverAndRun(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "plugdash-plugin-demo", goodPlugin)

	plugins, warnings := discoverDir(dir)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(plugins) != 1 {
		t.Fatalf("want 1 plugin, got %d", len(plugins))
	}
	p := plugins[0]
	if p.ID() != "ext-demo" || p.Name() != "Demo" {
		t.Fatalf("bad metadata: id=%q name=%q", p.ID(), p.Name())
	}
	if p.RefreshInterval() != 45*time.Second {
		t.Fatalf("want 45s interval, got %v", p.RefreshInterval())
	}
	if !p.IsExternal() {
		t.Fatalf("IsExternal should be true")
	}
	if len(p.ConfigSchema()) != 1 || p.ConfigSchema()[0].Key != "x" {
		t.Fatalf("bad schema: %+v", p.ConfigSchema())
	}

	cfgOut := filepath.Join(dir, "cfg-seen.json")
	t.Setenv("PLUGDASH_TEST_OUT", cfgOut)

	res, err := p.Run(context.Background(), plugin.Config{"x": "hello"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Visualization != plugin.VizStat || res.Title != "T" {
		t.Fatalf("bad result: viz=%q title=%q", res.Visualization, res.Title)
	}
	// Confirm the config JSON reached the plugin on stdin.
	seen, err := os.ReadFile(cfgOut)
	if err != nil {
		t.Fatalf("config was not passed through: %v", err)
	}
	if !contains(string(seen), `"x":"hello"`) {
		t.Fatalf("config passthrough wrong, plugin saw: %s", seen)
	}
}

func TestRefreshIntervalDefault(t *testing.T) {
	p := &ExternalPlugin{meta: describeOutput{ID: "x"}}
	if p.RefreshInterval() != time.Hour {
		t.Fatalf("want 1h default, got %v", p.RefreshInterval())
	}
}

func TestRunErrorFromStderr(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "plugdash-plugin-fail", `#!/bin/sh
case "$1" in
describe) printf '%s' '{"id":"failer","name":"F"}' ;;
run) echo "boom happened" >&2; exit 3 ;;
esac
`)
	p := &ExternalPlugin{path: path, meta: describeOutput{ID: "failer"}}
	_, err := p.Run(context.Background(), plugin.Config{})
	if err == nil {
		t.Fatal("want error from non-zero exit")
	}
	if got := err.Error(); got == "" || !contains(got, "boom happened") {
		t.Fatalf("error should carry stderr, got %q", got)
	}
}

func TestRunTimeout(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "plugdash-plugin-slow", `#!/bin/sh
case "$1" in
describe) printf '%s' '{"id":"slow","name":"S"}' ;;
run) sleep 5 ;;
esac
`)
	p := &ExternalPlugin{path: path, meta: describeOutput{ID: "slow"}}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := p.Run(ctx, plugin.Config{})
	if err == nil {
		t.Fatal("want timeout error")
	}
	// With cmd.WaitDelay the call must return shortly after the ctx fires even
	// though the child `sleep` holds the output pipe open.
	if time.Since(start) > 4*time.Second {
		t.Fatalf("run did not honor ctx timeout (took %v)", time.Since(start))
	}
}

func TestDiscoverSkipsBadAndNonMatching(t *testing.T) {
	dir := t.TempDir()
	// invalid describe JSON
	writeScript(t, dir, "plugdash-plugin-bad", `#!/bin/sh
echo "not json"
`)
	// missing id
	writeScript(t, dir, "plugdash-plugin-noid", `#!/bin/sh
printf '%s' '{"name":"x"}'
`)
	// non-executable, matching name
	noexec := filepath.Join(dir, "plugdash-plugin-noexec")
	if err := os.WriteFile(noexec, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// non-matching name (ignored entirely, no warning)
	writeScript(t, dir, "random-tool", goodPlugin)
	// one good plugin
	writeScript(t, dir, "plugdash-plugin-demo", goodPlugin)

	plugins, warnings := discoverDir(dir)
	if len(plugins) != 1 || plugins[0].ID() != "ext-demo" {
		t.Fatalf("want only the good plugin, got %d: %+v", len(plugins), plugins)
	}
	// bad, noid, noexec → 3 warnings; random-tool produces none.
	if len(warnings) != 3 {
		t.Fatalf("want 3 warnings, got %d: %v", len(warnings), warnings)
	}
}

func TestDiscoverMissingDir(t *testing.T) {
	plugins, warnings := discoverDir(filepath.Join(t.TempDir(), "does-not-exist"))
	if len(plugins) != 0 || len(warnings) != 0 {
		t.Fatalf("missing dir should be silent, got %d plugins %d warnings", len(plugins), len(warnings))
	}
}

func TestManagerRescanAddRemove(t *testing.T) {
	dir := t.TempDir()
	reg := plugin.NewRegistry()
	mgr := NewManager(dir, reg)

	if n, err := mgr.Load(); err != nil || n != 0 {
		t.Fatalf("empty load: n=%d err=%v", n, err)
	}

	// Add a plugin and rescan.
	writeScript(t, dir, "plugdash-plugin-demo", goodPlugin)
	added, removed, err := mgr.Rescan()
	if err != nil || added != 1 || removed != 0 {
		t.Fatalf("after add: added=%d removed=%d err=%v", added, removed, err)
	}
	if _, ok := reg.Get("ext-demo"); !ok {
		t.Fatal("plugin not registered after rescan")
	}

	// Rescan again with no changes: nothing added/removed.
	added, removed, _ = mgr.Rescan()
	if added != 0 || removed != 0 {
		t.Fatalf("no-op rescan churned: added=%d removed=%d", added, removed)
	}

	// Remove the binary and rescan: it should be unregistered.
	if err := os.Remove(filepath.Join(dir, "plugdash-plugin-demo")); err != nil {
		t.Fatal(err)
	}
	added, removed, _ = mgr.Rescan()
	if added != 0 || removed != 1 {
		t.Fatalf("after remove: added=%d removed=%d", added, removed)
	}
	if _, ok := reg.Get("ext-demo"); ok {
		t.Fatal("plugin still registered after removal")
	}
}

func TestManagerSkipsBuiltinCollision(t *testing.T) {
	dir := t.TempDir()
	reg := plugin.NewRegistry()
	reg.Register(builtinFake{})

	// External plugin claims the same id as the built-in.
	writeScript(t, dir, "plugdash-plugin-clash", `#!/bin/sh
case "$1" in
describe) printf '%s' '{"id":"builtin-x","name":"Imposter"}' ;;
run) printf '%s' '{"visualization":"stat","data":{"value":0}}' ;;
esac
`)
	mgr := NewManager(dir, reg)
	added, _, _ := mgr.Rescan()
	if added != 0 {
		t.Fatalf("collision should not register, added=%d", added)
	}
	got, _ := reg.Get("builtin-x")
	if _, isExt := got.(*ExternalPlugin); isExt {
		t.Fatal("built-in plugin was overwritten by external")
	}
}

// builtinFake is a minimal in-process plugin used to test id collisions.
type builtinFake struct{}

func (builtinFake) ID() string                         { return "builtin-x" }
func (builtinFake) Name() string                       { return "Builtin X" }
func (builtinFake) Description() string                { return "" }
func (builtinFake) ConfigSchema() []plugin.ConfigField { return nil }
func (builtinFake) RefreshInterval() time.Duration     { return time.Minute }
func (builtinFake) Run(context.Context, plugin.Config) (plugin.Result, error) {
	return plugin.Result{}, nil
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
