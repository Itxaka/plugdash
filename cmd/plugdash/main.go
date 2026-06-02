// Command plugdash runs the plugin dashboard server.
package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"plugdash/internal/extplugin"
	"plugdash/internal/plugin"
	"plugdash/internal/plugins/dockerimage"
	"plugdash/internal/plugins/githubactions"
	"plugdash/internal/plugins/githubartifacts"
	"plugdash/internal/plugins/githubissues"
	"plugdash/internal/plugins/githubrate"
	"plugdash/internal/plugins/githubreleases"
	"plugdash/internal/plugins/githubrepostats"
	"plugdash/internal/plugins/githubstars"
	"plugdash/internal/plugins/httphealth"
	"plugdash/internal/plugins/rssfeed"
	"plugdash/internal/server"
	"plugdash/internal/store"
	"plugdash/web"
)

// version is overridden at release build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	dbPath := flag.String("db", "plugdash.db", "path to the SQLite database file")
	pluginsDir := flag.String("plugins-dir", "", "directory of external plugin executables (default: $PLUGDASH_PLUGINS_DIR or ~/.config/plugdash/plugins)")
	debug := flag.Bool("debug", false, "enable verbose debug logging (also via PLUGDASH_DEBUG=1 or the Settings toggle)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("plugdash", version)
		return
	}

	if abs, err := filepath.Abs(*dbPath); err == nil {
		*dbPath = abs
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Structured logging: a dynamic level (toggled by -debug / PLUGDASH_DEBUG /
	// the persisted setting / the Settings UI) feeding both stderr and an
	// in-memory ring buffer served at /api/logs.
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	logRing := server.NewLogRing(1000)
	base := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	logger := slog.New(server.NewRingHandler(base, logRing))

	debugOn := *debug || os.Getenv("PLUGDASH_DEBUG") != ""
	if s, serr := st.GetSettings(); serr == nil {
		if s.Debug {
			debugOn = true
		}
		// A token saved in Settings authenticates all GitHub plugins.
		if s.GitHubToken != "" && os.Getenv("GITHUB_TOKEN") == "" {
			_ = os.Setenv("GITHUB_TOKEN", s.GitHubToken)
		}
	}
	if debugOn {
		level.Set(slog.LevelDebug)
	}

	reg := plugin.NewRegistry()
	reg.Register(githubreleases.New())
	reg.Register(githubartifacts.New())
	reg.Register(githubrepostats.New())
	reg.Register(httphealth.New())
	reg.Register(rssfeed.New())
	reg.Register(dockerimage.New())
	reg.Register(githubactions.New())
	reg.Register(githubstars.New())
	reg.Register(githubrate.New())
	reg.Register(githubissues.New())

	srv := server.New(reg, st, web.FS())
	srv.SetLogging(logger, logRing, level)

	// External plugins: discover executables in the plugins directory and
	// register them alongside the built-ins. The directory is resolved from the
	// -plugins-dir flag, then $PLUGDASH_PLUGINS_DIR, then ~/.config/plugdash/plugins.
	if dir := resolvePluginsDir(*pluginsDir); dir != "" {
		mgr := extplugin.NewManager(dir, reg)
		n, err := mgr.Load()
		if err != nil {
			log.Printf("external plugins: %v", err)
		} else if n > 0 {
			log.Printf("external plugins: loaded %d from %s", n, dir)
		}
		srv.SetPluginRescanner(mgr)
	}

	log.Printf("plugdash listening on %s (db: %s)", *addr, *dbPath)
	if err := http.ListenAndServe(*addr, srv); err != nil {
		log.Printf("server error: %v", err)
		os.Exit(1)
	}
}

// resolvePluginsDir picks the external plugins directory: an explicit flag wins,
// then the PLUGDASH_PLUGINS_DIR env var, then a default under the user config
// dir. It returns "" only if no default location can be determined.
func resolvePluginsDir(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("PLUGDASH_PLUGINS_DIR"); env != "" {
		return env
	}
	if cfg, err := os.UserConfigDir(); err == nil {
		return filepath.Join(cfg, "plugdash", "plugins")
	}
	return ""
}
