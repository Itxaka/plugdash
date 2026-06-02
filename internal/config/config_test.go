package config

import (
	"os"
	"path/filepath"
	"testing"
)

const validYAML = `
settings:
  github_token: ghp_abc123
  debug: true
trackers:
  - name: "Kubernetes Releases"
    plugin: github-activity
    refresh_interval_seconds: 300
    config:
      repo: kubernetes/kubernetes
  - key: custom-key
    name: "Some Other"
    plugin: github-activity
`

func TestParseValid(t *testing.T) {
	c, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Settings.GitHubToken != "ghp_abc123" {
		t.Errorf("GitHubToken = %q, want ghp_abc123", c.Settings.GitHubToken)
	}
	if !c.Settings.Debug {
		t.Error("Debug = false, want true")
	}
	if len(c.Trackers) != 2 {
		t.Fatalf("len(Trackers) = %d, want 2", len(c.Trackers))
	}
	first := c.Trackers[0]
	if first.Plugin != "github-activity" {
		t.Errorf("Trackers[0].Plugin = %q", first.Plugin)
	}
	if first.Name != "Kubernetes Releases" {
		t.Errorf("Trackers[0].Name = %q", first.Name)
	}
	if first.RefreshIntervalSeconds != 300 {
		t.Errorf("Trackers[0].RefreshIntervalSeconds = %d, want 300", first.RefreshIntervalSeconds)
	}
	if got := first.Config["repo"]; got != "kubernetes/kubernetes" {
		t.Errorf("Trackers[0].Config[repo] = %v", got)
	}
}

func TestKeyDerivation(t *testing.T) {
	c, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Derived from "Kubernetes Releases".
	if c.Trackers[0].Key != "kubernetes-releases" {
		t.Errorf("derived Key = %q, want kubernetes-releases", c.Trackers[0].Key)
	}
	// Explicit key is preserved.
	if c.Trackers[1].Key != "custom-key" {
		t.Errorf("explicit Key = %q, want custom-key", c.Trackers[1].Key)
	}
}

func TestParseValidationErrors(t *testing.T) {
	cases := map[string]string{
		"missing plugin": `
trackers:
  - name: "No Plugin"
`,
		"duplicate keys": `
trackers:
  - key: dup
    plugin: p
  - key: dup
    plugin: q
`,
		"neither key nor name": `
trackers:
  - plugin: p
`,
		"unknown top-level key": `
trackerz:
  - plugin: p
`,
		"unknown field key": `
trackers:
  - plugin: p
    nme: oops
`,
	}
	for name, yml := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(yml)); err == nil {
				t.Errorf("Parse(%s) = nil error, want error", name)
			}
		})
	}
}

func TestFileTrackers(t *testing.T) {
	c, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	fts := c.FileTrackers()
	if len(fts) != 2 {
		t.Fatalf("len(FileTrackers) = %d, want 2", len(fts))
	}
	ft := fts[0]
	if ft.Key != "kubernetes-releases" {
		t.Errorf("FileTrackers[0].Key = %q, want kubernetes-releases", ft.Key)
	}
	if ft.PluginID != "github-activity" {
		t.Errorf("FileTrackers[0].PluginID = %q", ft.PluginID)
	}
	if ft.Name != "Kubernetes Releases" {
		t.Errorf("FileTrackers[0].Name = %q", ft.Name)
	}
	if ft.RefreshIntervalSeconds != 300 {
		t.Errorf("FileTrackers[0].RefreshIntervalSeconds = %d, want 300", ft.RefreshIntervalSeconds)
	}
	if got := ft.Config["repo"]; got != "kubernetes/kubernetes" {
		t.Errorf("FileTrackers[0].Config[repo] = %v", got)
	}
	if fts[1].Key != "custom-key" {
		t.Errorf("FileTrackers[1].Key = %q, want custom-key", fts[1].Key)
	}
}

func TestLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(validYAML), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Trackers) != 2 {
		t.Errorf("len(Trackers) = %d, want 2", len(c.Trackers))
	}
	if c.Settings.GitHubToken != "ghp_abc123" {
		t.Errorf("GitHubToken = %q", c.Settings.GitHubToken)
	}
}

func TestLoadMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	if _, err := Load(path); err == nil {
		t.Error("Load(missing) = nil error, want error")
	}
}
