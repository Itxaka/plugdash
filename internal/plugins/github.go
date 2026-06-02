// Package plugins contains the concrete plugin implementations and the GitHub
// API helper they share.
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"plugdash/internal/plugin"
)

// GHBaseURL is the GitHub REST API root. It is a var so tests can point it at a
// local stub server.
var GHBaseURL = "https://api.github.com"

// GHClient performs authenticated (when a token is present) GitHub API calls.
type GHClient struct {
	http  *http.Client
	token string
}

// NewGHClient builds a client. If token is empty it falls back to the
// GITHUB_TOKEN environment variable; unauthenticated requests still work but are
// rate-limited harder by GitHub.
func NewGHClient(token string) *GHClient {
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	return &GHClient{
		http:  &http.Client{Timeout: 15 * time.Second},
		token: token,
	}
}

// Release is the subset of GitHub's release object the plugins consume.
type Release struct {
	Name        string    `json:"name"`
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"assets"`
}

// Asset is a release artifact.
type Asset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	DownloadCount      int    `json:"download_count"`
}

// NormalizeRepo accepts "owner/repo" or a full GitHub URL and returns
// owner, repo. It returns an error if the shape is unrecognized.
func NormalizeRepo(s string) (owner, repo string, err error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://github.com/")
	s = strings.TrimPrefix(s, "http://github.com/")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.TrimSuffix(s, ".git")
	s = strings.Trim(s, "/")
	parts := strings.Split(s, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo %q, want owner/repo", s)
	}
	return parts[0], parts[1], nil
}

// Get performs a GET against the API path (e.g. "/repos/o/r/releases") and
// decodes the JSON body into out.
func (c *GHClient) Get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GHBaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	log := plugin.LoggerFrom(ctx)
	log.Debug("github request", "method", http.MethodGet, "url", GHBaseURL+path, "authenticated", c.token != "")
	resp, err := c.http.Do(req)
	if err != nil {
		log.Debug("github request failed", "url", GHBaseURL+path, "error", err.Error())
		return fmt.Errorf("github request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	log.Debug("github response", "url", GHBaseURL+path, "status", resp.StatusCode, "bytes", len(body))
	if resp.StatusCode != http.StatusOK {
		return rateAwareError(resp, body, GHBaseURL+path)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode github response: %w", err)
	}
	return nil
}

// rateAwareError turns a non-200 GitHub response into an error, with a friendly
// hint when it looks like a rate limit (the common pain without a token).
func rateAwareError(resp *http.Response, body []byte, url string) error {
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" ||
			strings.Contains(strings.ToLower(string(body)), "rate limit") {
			return fmt.Errorf("GitHub API rate limit exceeded — add a GitHub token in Settings to raise the limit")
		}
	}
	return fmt.Errorf("github %s returned %d: %s", url, resp.StatusCode, strings.TrimSpace(truncate(body, 200)))
}

// ListReleases returns up to `limit` most-recent releases for owner/repo.
func (c *GHClient) ListReleases(ctx context.Context, owner, repo string, limit int) ([]Release, error) {
	if limit <= 0 {
		limit = 5
	}
	var releases []Release
	path := fmt.Sprintf("/repos/%s/%s/releases?per_page=%d", owner, repo, limit)
	if err := c.Get(ctx, path, &releases); err != nil {
		return nil, err
	}
	if len(releases) > limit {
		releases = releases[:limit]
	}
	return releases, nil
}

// ReleaseByTag returns the release for a specific tag, or the latest release
// when tag is empty or "latest".
func (c *GHClient) ReleaseByTag(ctx context.Context, owner, repo, tag string) (*Release, error) {
	var path string
	if tag == "" || strings.EqualFold(tag, "latest") {
		path = fmt.Sprintf("/repos/%s/%s/releases/latest", owner, repo)
	} else {
		path = fmt.Sprintf("/repos/%s/%s/releases/tags/%s", owner, repo, tag)
	}
	var rel Release
	if err := c.Get(ctx, path, &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}
