// Package plugins contains the concrete plugin implementations and the GitHub
// API helper they share.
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"plugdash/internal/plugin"
)

// GHBaseURL is the GitHub REST API root. It is a var so tests can point it at a
// local stub server.
var GHBaseURL = "https://api.github.com"

// ghCacheEntry holds a cached response body and its ETag for conditional GETs.
type ghCacheEntry struct {
	etag string
	body []byte
}

// ghCache is a process-wide conditional-request cache. Plugins build a fresh
// GHClient on every run, so a cache living on the client would never survive
// between runs — it must be package-global. Entries are keyed by request URL +
// a token fingerprint (so unauthenticated and authenticated views, or different
// tokens, never share an entry). GitHub conditional requests that come back
// 304 Not Modified do NOT count against the REST rate limit, so this both cuts
// latency/bandwidth and conserves the budget for monitored trackers. The cache
// only grows, bounded by the (small, finite) set of tracker URLs.
var ghCache = struct {
	sync.RWMutex
	m map[string]ghCacheEntry
}{m: map[string]ghCacheEntry{}}

// ghRate records, per token fingerprint, a time before which GitHub has told us
// further requests will be rejected (primary rate-limit reset, or a secondary
// Retry-After). While that time is in the future we fail fast instead of
// hammering the API and burning more of the budget.
var ghRate = struct {
	sync.Mutex
	until map[string]time.Time
}{until: map[string]time.Time{}}

// tokenFingerprint maps a token to a short, non-reversible key. The token
// itself is never used as a map key (avoids leaking it) and "anon" namespaces
// unauthenticated requests.
func tokenFingerprint(token string) string {
	if token == "" {
		return "anon"
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(token))
	return strconv.FormatUint(h.Sum64(), 36)
}

func ghCacheKey(fp, url string) string { return fp + "\x00" + url }

func ghCacheGet(key string) (ghCacheEntry, bool) {
	ghCache.RLock()
	defer ghCache.RUnlock()
	e, ok := ghCache.m[key]
	return e, ok
}

func ghCachePut(key string, e ghCacheEntry) {
	ghCache.Lock()
	ghCache.m[key] = e
	ghCache.Unlock()
}

// rateLimitedUntil reports whether requests for fp should currently be skipped,
// returning the reset time. Expired entries are cleared.
func rateLimitedUntil(fp string) (time.Time, bool) {
	ghRate.Lock()
	defer ghRate.Unlock()
	until, ok := ghRate.until[fp]
	if !ok {
		return time.Time{}, false
	}
	if time.Now().Before(until) {
		return until, true
	}
	delete(ghRate.until, fp)
	return time.Time{}, false
}

func setRateLimited(fp string, until time.Time) {
	ghRate.Lock()
	ghRate.until[fp] = until
	ghRate.Unlock()
}

// parseRateLimitReset extracts a back-off deadline from a rejected response: a
// secondary-limit Retry-After (seconds), or a primary-limit X-RateLimit-Reset
// (unix seconds) but only once the remaining budget is actually exhausted. It
// returns ok=false for ordinary non-200s (e.g. 404), so those don't trip the
// back-off.
func parseRateLimitReset(resp *http.Response) (time.Time, bool) {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && secs >= 0 {
			return time.Now().Add(time.Duration(secs) * time.Second), true
		}
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
			if unix, err := strconv.ParseInt(strings.TrimSpace(reset), 10, 64); err == nil {
				return time.Unix(unix, 0), true
			}
		}
	}
	return time.Time{}, false
}

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

// OwnerAvatarURL returns the GitHub avatar image URL for a user or org login.
// github.com/<owner>.png redirects to the current avatar and works unauthenticated,
// so it can be used directly in an <img> tag.
func OwnerAvatarURL(owner string) string {
	if owner == "" {
		return ""
	}
	return "https://github.com/" + owner + ".png?size=64"
}

// Badge is one tone-colored pill on a list item, shared by the issue/PR widgets.
// Tone is one of ok | warn | bad | neutral (the frontend maps it to a color).
type Badge struct {
	Label string `json:"label"`
	Tone  string `json:"tone"`
}

// CheckRun and CheckRunsResp model the GitHub check-runs API.
type CheckRun struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type CheckRunsResp struct {
	TotalCount int        `json:"total_count"`
	CheckRuns  []CheckRun `json:"check_runs"`
}

// AggregateCIBadge collapses a commit's check runs into a single CI badge:
// failing if any run concluded in a failure-like state, running if any is not
// completed, passing if all completed cleanly, or "no checks" when there are none.
func AggregateCIBadge(runs CheckRunsResp) Badge {
	if runs.TotalCount == 0 {
		return Badge{Label: "CI: no checks", Tone: "neutral"}
	}
	failed := map[string]bool{
		"failure":         true,
		"timed_out":       true,
		"cancelled":       true,
		"action_required": true,
	}
	for _, r := range runs.CheckRuns {
		if failed[r.Conclusion] {
			return Badge{Label: "CI: failing", Tone: "bad"}
		}
	}
	for _, r := range runs.CheckRuns {
		if r.Status != "completed" {
			return Badge{Label: "CI: running", Tone: "neutral"}
		}
	}
	return Badge{Label: "CI: passing", Tone: "ok"}
}

// CIBadge fetches a commit's check runs and returns the aggregated CI badge.
// ok is false when the request failed (the caller omits the badge).
func (c *GHClient) CIBadge(ctx context.Context, owner, repo, sha string) (Badge, bool) {
	var runs CheckRunsResp
	if err := c.Get(ctx, fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs", owner, repo, sha), &runs); err != nil {
		return Badge{}, false
	}
	return AggregateCIBadge(runs), true
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
	url := GHBaseURL + path
	fp := tokenFingerprint(c.token)
	log := plugin.LoggerFrom(ctx)

	// Fail fast if GitHub recently told us this token is rate-limited.
	if until, limited := rateLimitedUntil(fp); limited {
		return fmt.Errorf("GitHub API rate limit exceeded — resets at %s (add a GitHub token in Settings to raise the limit)",
			until.Local().Format("15:04:05"))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	// Conditional request: a matching ETag lets GitHub answer 304 (free, no
	// rate-limit cost) when nothing changed.
	key := ghCacheKey(fp, url)
	if ent, ok := ghCacheGet(key); ok && ent.etag != "" {
		req.Header.Set("If-None-Match", ent.etag)
	}

	log.Debug("github request", "method", http.MethodGet, "url", url, "authenticated", c.token != "")
	resp, err := c.http.Do(req)
	if err != nil {
		log.Debug("github request failed", "url", url, "error", err.Error())
		return fmt.Errorf("github request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		ent, ok := ghCacheGet(key)
		log.Debug("github not-modified (cache hit)", "url", url, "cached", ok)
		if ok {
			return json.Unmarshal(ent.body, out)
		}
		// The cache never evicts and we only send If-None-Match when an entry
		// exists, so this is effectively unreachable; surface a retryable error
		// rather than silently returning empty data.
		return fmt.Errorf("github cache miss on 304 for %s", url)
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	log.Debug("github response", "url", url, "status", resp.StatusCode, "bytes", len(body))
	if resp.StatusCode != http.StatusOK {
		if until, limited := parseRateLimitReset(resp); limited {
			setRateLimited(fp, until)
			log.Debug("github rate-limited", "url", url, "until", until.Format(time.RFC3339))
		}
		return rateAwareError(resp, body, url)
	}

	if etag := resp.Header.Get("ETag"); etag != "" {
		ghCachePut(key, ghCacheEntry{etag: etag, body: body})
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
