package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"plugdash/internal/plugin"
)

// imageRefChars matches a bare image reference (no tag/digest): registry host,
// path components, ports. Notably excludes spaces.
var imageRefChars = regexp.MustCompile(`^[A-Za-z0-9._:/-]+$`)

// ValidateImage checks that image looks like a registry image reference without
// a tag or digest, returning a clear error otherwise. It rejects the common
// mistakes: pasting a "docker pull ..." command, a full URL, embedding a tag,
// or stray whitespace.
func ValidateImage(image string) error {
	trimmed := strings.TrimSpace(image)
	if trimmed == "" {
		return fmt.Errorf("image is required")
	}
	if strings.ContainsAny(trimmed, " \t\n\r") {
		return fmt.Errorf("image %q must not contain spaces (enter just the image, e.g. ghcr.io/org/repo)", image)
	}
	if strings.Contains(trimmed, "://") {
		return fmt.Errorf("image must be a registry reference like ghcr.io/org/repo, not a URL")
	}
	if !imageRefChars.MatchString(trimmed) {
		return fmt.Errorf("image %q contains invalid characters", image)
	}
	// Reject a tag or digest in the repository portion (host:port is allowed).
	repo := trimmed
	if i := strings.IndexByte(trimmed, '/'); i >= 0 {
		first := trimmed[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			repo = trimmed[i+1:] // drop the registry host before checking
		}
	}
	if strings.ContainsRune(repo, ':') {
		return fmt.Errorf("do not include a tag in the image; put the tag in the Tags field")
	}
	if strings.ContainsRune(repo, '@') {
		return fmt.Errorf("do not include a digest in the image")
	}
	return nil
}

// errStr renders an error for structured logging, "" when nil.
func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Platform describes a single os/architecture entry in a multi-arch image
// index (manifest list).
type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

// registryClient performs Docker Registry v2 manifest checks. A 15s timeout
// guards against slow registries.
var registryClient = &http.Client{Timeout: 15 * time.Second}

// manifestAccept is the comma-joined Accept header advertising support for both
// OCI and Docker image-index and single-image manifest media types.
const manifestAccept = "application/vnd.oci.image.index.v1+json," +
	"application/vnd.docker.distribution.manifest.list.v2+json," +
	"application/vnd.oci.image.manifest.v1+json," +
	"application/vnd.docker.distribution.manifest.v2+json"

// manifestList is the subset of an image index / manifest list we decode.
type manifestList struct {
	MediaType string `json:"mediaType"`
	Manifests []struct {
		Platform Platform `json:"platform"`
	} `json:"manifests"`
}

// parsedImage holds the registry host, the normalized repository, and the URL
// scheme to use (http for local/ported hosts, https otherwise).
type parsedImage struct {
	registry string
	repo     string
	scheme   string
	// hint records which well-known registry this is so we can pre-fetch a
	// token without waiting for a challenge: "docker", "ghcr", or "" (generic).
	hint string
}

// parseImage splits an image reference (without a tag) into its registry host
// and repository, applying Docker Hub's library/ and host defaults.
func parseImage(image string) parsedImage {
	image = strings.TrimSpace(image)
	image = strings.Trim(image, "/")

	var host, rest string
	if i := strings.IndexByte(image, '/'); i >= 0 {
		first := image[:i]
		// The first path element is a registry host only if it looks like one:
		// it contains a dot or colon, or is localhost.
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			host = first
			rest = image[i+1:]
		} else {
			rest = image
		}
	} else {
		rest = image
	}

	scheme := "https"
	if isLocalOrPorted(host) {
		scheme = "http"
	}

	switch {
	case host == "" || host == "docker.io" || host == "index.docker.io" || host == "registry-1.docker.io":
		repo := rest
		// Bare names like "nginx" live under the library/ namespace.
		if !strings.Contains(repo, "/") {
			repo = "library/" + repo
		}
		return parsedImage{registry: "registry-1.docker.io", repo: repo, scheme: "https", hint: "docker"}
	case host == "ghcr.io":
		return parsedImage{registry: "ghcr.io", repo: rest, scheme: "https", hint: "ghcr"}
	default:
		return parsedImage{registry: host, repo: rest, scheme: scheme, hint: ""}
	}
}

// isLocalOrPorted reports whether host is a local address or carries an
// explicit port, in which case plain http should be used (handy for tests
// hitting an httptest server like 127.0.0.1:PORT).
func isLocalOrPorted(host string) bool {
	if host == "" {
		return false
	}
	if host == "localhost" || host == "127.0.0.1" {
		return true
	}
	if strings.HasPrefix(host, "localhost:") || strings.HasPrefix(host, "127.0.0.1:") {
		return true
	}
	// A colon anywhere indicates host:port (IPv6 literals would be bracketed).
	if strings.Contains(host, ":") {
		return true
	}
	return false
}

// CheckManifest reports whether image:tag exists in its registry and, when the
// tag resolves to a multi-arch image index, the platforms it advertises.
//
//   - exists=true, platforms set: multi-arch image index found.
//   - exists=true, platforms nil/empty: single-arch image manifest found
//     (architecture unverified).
//   - exists=false, err=nil: the tag is absent (HTTP 404).
//   - err!=nil: a transport or unexpected-status failure.
//
// token, when non-empty, is used as a fallback bearer credential if no auth
// challenge yields one.
func CheckManifest(ctx context.Context, image, tag, token string) (exists bool, platforms []Platform, err error) {
	pi := parseImage(image)
	manifestURL := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", pi.scheme, pi.registry, pi.repo, tag)
	log := plugin.LoggerFrom(ctx)
	log.Debug("registry manifest check", "url", manifestURL, "registry", pi.registry, "repo", pi.repo, "tag", tag)
	defer func() {
		log.Debug("registry manifest result", "url", manifestURL, "exists", exists, "platforms", len(platforms), "error", errStr(err))
	}()

	// Pre-fetch a token for the well-known registries so the first request is
	// already authorized. Failures here are non-fatal: we still try the request
	// and honor any challenge it returns.
	bearer := token
	switch pi.hint {
	case "docker":
		if t := fetchToken(ctx, "https://auth.docker.io/token?service=registry.docker.io&scope=repository:"+pi.repo+":pull"); t != "" {
			bearer = t
		}
	case "ghcr":
		if t := fetchToken(ctx, "https://ghcr.io/token?scope=repository:"+pi.repo+":pull"); t != "" {
			bearer = t
		}
	default:
		// Generic registries: try the conventional /token endpoint. If it 404s
		// or yields nothing we proceed unauthenticated and let any challenge on
		// the manifest request drive auth.
		tokenURL := fmt.Sprintf("%s://%s/token?scope=repository:%s:pull", pi.scheme, pi.registry, pi.repo)
		if t := fetchToken(ctx, tokenURL); t != "" {
			bearer = t
		}
	}

	resp, body, err := doManifest(ctx, manifestURL, bearer)
	if err != nil {
		return false, nil, err
	}

	// Honor a bearer challenge: re-auth against the advertised realm and retry.
	if resp.StatusCode == http.StatusUnauthorized {
		if t := tokenFromChallenge(ctx, resp.Header.Get("WWW-Authenticate")); t != "" {
			resp, body, err = doManifest(ctx, manifestURL, t)
			if err != nil {
				return false, nil, err
			}
		}
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		return true, decodePlatforms(resp.Header.Get("Content-Type"), body), nil
	case resp.StatusCode == http.StatusNotFound:
		return false, nil, nil
	default:
		return false, nil, fmt.Errorf("registry %s returned %d: %s",
			manifestURL, resp.StatusCode, strings.TrimSpace(truncate(body, 200)))
	}
}

// doManifest issues a single manifest GET with the manifest Accept header and
// an optional bearer token, returning the response (body already drained) plus
// the body bytes.
func doManifest(ctx context.Context, url, bearer string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", manifestAccept)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := registryClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("registry request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp, body, nil
}

// decodePlatforms returns the platform list when the response is an image index
// / manifest list (detected via Content-Type or the JSON mediaType), or nil for
// a single-image manifest.
func decodePlatforms(contentType string, body []byte) []Platform {
	var ml manifestList
	_ = json.Unmarshal(body, &ml)

	if !isIndexMediaType(contentType) && !isIndexMediaType(ml.MediaType) {
		return nil
	}
	if len(ml.Manifests) == 0 {
		return nil
	}
	out := make([]Platform, 0, len(ml.Manifests))
	for _, m := range ml.Manifests {
		if m.Platform.Architecture == "" {
			continue
		}
		out = append(out, m.Platform)
	}
	return out
}

// isIndexMediaType reports whether a media type names a multi-arch image index
// or Docker manifest list.
func isIndexMediaType(mt string) bool {
	mt = strings.ToLower(mt)
	return strings.Contains(mt, "image.index") || strings.Contains(mt, "manifest.list")
}

// fetchToken GETs a token endpoint and returns the bearer token, or "" on any
// non-200 / decode failure (caller treats "" as "proceed without a token").
func fetchToken(ctx context.Context, url string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	resp, err := registryClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var tr struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &tr) != nil {
		return ""
	}
	if tr.Token != "" {
		return tr.Token
	}
	return tr.AccessToken
}

// tokenFromChallenge parses a "Bearer realm=...,service=...,scope=..." header
// and follows it to fetch a token. Returns "" if the header is not a usable
// bearer challenge.
func tokenFromChallenge(ctx context.Context, header string) string {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return ""
	}
	params := parseChallengeParams(header[len("bearer "):])
	realm := params["realm"]
	if realm == "" {
		return ""
	}
	u := realm
	q := url.Values{}
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if s := params["scope"]; s != "" {
		q.Set("scope", s)
	}
	if enc := q.Encode(); enc != "" {
		if strings.Contains(u, "?") {
			u += "&" + enc
		} else {
			u += "?" + enc
		}
	}
	return fetchToken(ctx, u)
}

// parseChallengeParams splits a comma-separated list of key="value" (or
// key=value) pairs from a WWW-Authenticate header into a map.
func parseChallengeParams(s string) map[string]string {
	out := map[string]string{}
	for _, part := range splitChallenge(s) {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		val = strings.Trim(val, `"`)
		out[key] = val
	}
	return out
}

// splitChallenge splits on commas that are not inside a quoted value.
func splitChallenge(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch r {
		case '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case ',':
			if inQuote {
				cur.WriteRune(r)
			} else {
				parts = append(parts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// truncate returns at most n bytes of b as a string.
func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}
