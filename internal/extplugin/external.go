// Package extplugin lets plugdash discover and run plugins that ship as
// standalone executables, in any language, instead of being compiled into the
// binary. An external plugin is a program named "plugdash-plugin-<name>" that
// speaks a tiny stdio protocol:
//
//	plugdash-plugin-foo describe   # prints plugin metadata JSON to stdout
//	plugdash-plugin-foo run        # reads config JSON on stdin, writes a
//	                               # Result JSON to stdout
//
// The ExternalPlugin adapter implements plugin.Plugin by shelling out, so the
// rest of plugdash (registry, server, frontend) treats external and built-in
// plugins identically.
package extplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"plugdash/internal/plugin"
)

// logPluginStderr emits each non-empty line a plugin wrote to stderr as a debug
// log entry, so an external plugin can log simply by printing to stderr.
func logPluginStderr(log *slog.Logger, stderr string) {
	stderr = strings.TrimRight(stderr, "\n")
	if stderr == "" {
		return
	}
	for _, line := range strings.Split(stderr, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			log.Debug("plugin stderr", "line", line)
		}
	}
}

const (
	// describeTimeout bounds how long a plugin may take to report its metadata.
	describeTimeout = 5 * time.Second
	// maxOutput caps stdout/stderr captured from a plugin, guarding against a
	// runaway process exhausting memory.
	maxOutput = 8 << 20 // 8 MiB
	// waitDelay bounds how long exec will wait, after the context is cancelled
	// and the process killed, for lingering child processes to release the
	// output pipes before forcibly returning.
	waitDelay = 2 * time.Second
)

// describeOutput is the JSON a plugin emits from `describe`. The field shapes
// mirror what the server exposes at /api/plugins so a plugin author has a
// single contract to target.
type describeOutput struct {
	ID                     string               `json:"id"`
	Name                   string               `json:"name"`
	Description            string               `json:"description"`
	RefreshIntervalSeconds int                  `json:"refresh_interval_seconds"`
	Schema                 []plugin.ConfigField `json:"schema"`
}

// ExternalPlugin adapts a plugin executable to the plugin.Plugin interface. Its
// metadata is captured once at discovery time (via `describe`); each Run shells
// out afresh, matching plugdash's stateless, just-in-time model.
type ExternalPlugin struct {
	path string
	meta describeOutput
}

// Path returns the executable path backing this plugin.
func (e *ExternalPlugin) Path() string { return e.path }

func (e *ExternalPlugin) ID() string                         { return e.meta.ID }
func (e *ExternalPlugin) Name() string                       { return e.meta.Name }
func (e *ExternalPlugin) Description() string                { return e.meta.Description }
func (e *ExternalPlugin) ConfigSchema() []plugin.ConfigField { return e.meta.Schema }

// IsExternal marks this as an external plugin so the server can flag it in the
// API and UI. The server detects it via an interface assertion, avoiding an
// import of this package.
func (e *ExternalPlugin) IsExternal() bool { return true }

// RefreshInterval converts the declared seconds to a Duration. A plugin that
// declares <= 0 gets a conservative 1h default so it is never polled tighter
// than that by accident.
func (e *ExternalPlugin) RefreshInterval() time.Duration {
	if e.meta.RefreshIntervalSeconds <= 0 {
		return time.Hour
	}
	return time.Duration(e.meta.RefreshIntervalSeconds) * time.Second
}

// Run executes `<plugin> run`, feeding the tracker config as JSON on stdin and
// decoding the Result from stdout. A non-zero exit (or timeout) becomes an
// error carrying the trimmed stderr. The supplied ctx governs the timeout; the
// process is killed if ctx is cancelled.
func (e *ExternalPlugin) Run(ctx context.Context, cfg plugin.Config) (plugin.Result, error) {
	in, err := json.Marshal(cfg)
	if err != nil {
		return plugin.Result{}, fmt.Errorf("marshal config: %w", err)
	}

	var out, errBuf limitedBuffer
	out.limit = maxOutput
	errBuf.limit = maxOutput

	log := plugin.LoggerFrom(ctx).With("plugin", e.meta.ID, "path", e.path)
	debug := log.Enabled(ctx, slog.LevelDebug)

	cmd := exec.CommandContext(ctx, e.path, "run")
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	// If the plugin (or a child it spawned) keeps an output pipe open after the
	// context is cancelled, don't block forever waiting on it: kill and return.
	cmd.WaitDelay = waitDelay
	// Let the plugin know it may be verbose; its stderr is captured as its log.
	if debug {
		cmd.Env = append(cmd.Environ(), "PLUGDASH_DEBUG=1")
	}

	log.Debug("running external plugin", "config", string(in))
	runErr := cmd.Run()
	// The plugin's stderr is its log channel: surface it (debug on success,
	// it is also folded into the error message on failure).
	logPluginStderr(log, errBuf.String())

	if runErr != nil {
		if ctx.Err() != nil {
			return plugin.Result{}, fmt.Errorf("plugin %q timed out", e.meta.ID)
		}
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = runErr.Error()
		}
		return plugin.Result{}, fmt.Errorf("plugin %q failed: %s", e.meta.ID, msg)
	}

	var res plugin.Result
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &res); err != nil {
		return plugin.Result{}, fmt.Errorf("plugin %q produced invalid result JSON: %w", e.meta.ID, err)
	}
	return res, nil
}

// describe runs `<path> describe` and parses its metadata. It validates that an
// id is present, since the id keys the registry.
func describe(ctx context.Context, path string) (describeOutput, error) {
	var out, errBuf limitedBuffer
	out.limit = maxOutput
	errBuf.limit = maxOutput

	cmd := exec.CommandContext(ctx, path, "describe")
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	cmd.WaitDelay = waitDelay
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return describeOutput{}, fmt.Errorf("describe failed: %s", msg)
	}

	var meta describeOutput
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &meta); err != nil {
		return describeOutput{}, fmt.Errorf("invalid describe JSON: %w", err)
	}
	if strings.TrimSpace(meta.ID) == "" {
		return describeOutput{}, fmt.Errorf("describe output missing required \"id\"")
	}
	if meta.Name == "" {
		meta.Name = meta.ID
	}
	return meta, nil
}

// limitedBuffer is a bytes.Buffer that silently discards writes past a byte
// limit, so a misbehaving plugin cannot exhaust memory.
type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.Buffer.Len(); remaining > 0 {
		if len(p) > remaining {
			_, _ = b.Buffer.Write(p[:remaining])
		} else {
			_, _ = b.Buffer.Write(p)
		}
	}
	// Report the full length as written so exec does not error on a short write.
	return len(p), nil
}
