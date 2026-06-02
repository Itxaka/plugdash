package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"plugdash/internal/plugin"
)

const (
	activityPerPage    = 100
	activityMaxPages   = 400
	activityPageLimit  = 8 << 20 // ~8MB per page
	activityAPIVersion = "2022-11-28"
	starAcceptHeader   = "application/vnd.github.star+json"
)

var activityClient = &http.Client{Timeout: 15 * time.Second}

// ActivityMetricOptions are the metrics the activity plugins can plot. Shared so
// the cumulative and per-period plugins offer the same choices.
var ActivityMetricOptions = []plugin.SelectOption{
	{Value: "stars", Label: "Stars"},
	{Value: "commits", Label: "Commits"},
	{Value: "issues", Label: "Issues"},
	{Value: "prs", Label: "Pull requests"},
}

// ActivityMetricLabel returns the human label for a metric key (or "" if unknown).
func ActivityMetricLabel(metric string) string {
	for _, o := range ActivityMetricOptions {
		if o.Value == metric {
			return o.Label
		}
	}
	return ""
}

// FetchActivityTimestamps returns the timestamps of a repository's activity
// items for the given metric (stars/commits/issues/prs), paginating up to
// maxPages of 100. token, when empty, falls back to GITHUB_TOKEN. Order follows
// the API; callers sort as needed.
func FetchActivityTimestamps(ctx context.Context, owner, repo, metric, token string, maxPages int) ([]time.Time, error) {
	if ActivityMetricLabel(metric) == "" {
		return nil, fmt.Errorf("unknown metric %q", metric)
	}
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if maxPages <= 0 {
		maxPages = 30
	}
	if maxPages > activityMaxPages {
		maxPages = activityMaxPages
	}

	var times []time.Time
	for page := 1; page <= maxPages; page++ {
		ts, more, err := fetchActivityPage(ctx, owner, repo, metric, token, page)
		if err != nil {
			return nil, err
		}
		times = append(times, ts...)
		if !more {
			break
		}
	}
	return times, nil
}

// fetchActivityPage fetches one page and returns its timestamps plus whether the
// page was full (so the caller should keep paginating).
func fetchActivityPage(ctx context.Context, owner, repo, metric, token string, page int) (times []time.Time, more bool, err error) {
	var path, accept string
	switch metric {
	case "stars":
		path = fmt.Sprintf("/repos/%s/%s/stargazers?per_page=%d&page=%d", owner, repo, activityPerPage, page)
		accept = starAcceptHeader
	case "commits":
		path = fmt.Sprintf("/repos/%s/%s/commits?per_page=%d&page=%d", owner, repo, activityPerPage, page)
	case "issues":
		path = fmt.Sprintf("/repos/%s/%s/issues?state=all&per_page=%d&page=%d", owner, repo, activityPerPage, page)
	case "prs":
		path = fmt.Sprintf("/repos/%s/%s/pulls?state=all&per_page=%d&page=%d", owner, repo, activityPerPage, page)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GHBaseURL+path, nil)
	if err != nil {
		return nil, false, err
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", activityAPIVersion)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	log := plugin.LoggerFrom(ctx)
	log.Debug("activity request", "metric", metric, "url", GHBaseURL+path, "page", page)
	resp, err := activityClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("github request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, activityPageLimit))
	log.Debug("activity response", "url", GHBaseURL+path, "status", resp.StatusCode, "bytes", len(body))
	if resp.StatusCode != http.StatusOK {
		return nil, false, rateAwareError(resp, body, GHBaseURL+path)
	}

	switch metric {
	case "stars":
		var items []struct {
			StarredAt time.Time `json:"starred_at"`
		}
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, false, fmt.Errorf("decode stargazers: %w", err)
		}
		for _, it := range items {
			times = append(times, it.StarredAt)
		}
		return times, len(items) == activityPerPage, nil
	case "commits":
		var items []struct {
			Commit struct {
				Committer struct {
					Date time.Time `json:"date"`
				} `json:"committer"`
			} `json:"commit"`
		}
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, false, fmt.Errorf("decode commits: %w", err)
		}
		for _, it := range items {
			times = append(times, it.Commit.Committer.Date)
		}
		return times, len(items) == activityPerPage, nil
	case "issues":
		var items []struct {
			CreatedAt   time.Time       `json:"created_at"`
			PullRequest json.RawMessage `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, false, fmt.Errorf("decode issues: %w", err)
		}
		for _, it := range items {
			if len(it.PullRequest) > 0 {
				continue // skip PRs returned by the issues endpoint
			}
			times = append(times, it.CreatedAt)
		}
		return times, len(items) == activityPerPage, nil
	default: // prs
		var items []struct {
			CreatedAt time.Time `json:"created_at"`
		}
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, false, fmt.Errorf("decode pulls: %w", err)
		}
		for _, it := range items {
			times = append(times, it.CreatedAt)
		}
		return times, len(items) == activityPerPage, nil
	}
}
