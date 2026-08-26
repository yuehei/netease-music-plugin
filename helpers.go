package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

// getAPIEndpoints returns the configured Netease Cloud Music API base URLs.
// The config value holds one URL per line; blank lines are skipped and
// trailing slashes are stripped so endpoints can be joined with paths directly.
// Returns nil when nothing is configured — the plugin has no built-in default
// endpoint and must be configured after installation.
func getAPIEndpoints() []string {
	val, exists := host.ConfigGet(configAPIEndpoints)
	if !exists || strings.TrimSpace(val) == "" {
		return nil
	}
	parts := strings.Split(val, "\n")
	endpoints := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimRight(strings.TrimSpace(p), "/")
		if p != "" {
			endpoints = append(endpoints, p)
		}
	}
	return endpoints
}

// errNoAPIEndpoint is returned by apiGet when no API endpoint is configured.
var errNoAPIEndpoint = errors.New("api_endpoints not configured: set at least one API endpoint in the plugin settings")

// retryableAPICodes are Netease API-level codes worth retrying on another
// mirror: rate limiting is enforced per source IP, so a mirror returning one
// of these may be throttled while another is not.
var retryableAPICodes = map[int]bool{
	405: true, // frequency limit
	429: true, // too many requests
	460: true, // operation too frequent ("cheating")
	462: true, // temporarily unavailable due to frequency
}

// shuffledAPIEndpoints returns all configured endpoints in random order, so
// load is spread across mirrors while apiGet can fall through to the next one
// on failure. Returns nil when no endpoint is configured.
func shuffledAPIEndpoints() []string {
	endpoints := getAPIEndpoints()
	perm := rand.Perm(len(endpoints))
	out := make([]string, 0, len(endpoints))
	for _, i := range perm {
		out = append(out, endpoints[i])
	}
	return out
}

// getMusicU returns the configured Netease MUSIC_U cookie value, or "" when
// not set. Endpoints that require login (e.g. /simi/artist) only work when
// this is configured.
func getMusicU() string {
	val, exists := host.ConfigGet(configMusicU)
	if !exists {
		return ""
	}
	return strings.TrimSpace(val)
}

// apiGet fetches a Netease Cloud Music API path (e.g. "/artist/detail?id=123")
// from the configured endpoints and unmarshals the JSON body into target.
// Endpoints are tried in random order: when a mirror is unreachable (transport
// error or non-200 HTTP status) or rate-limited (API code in retryableAPICodes)
// the next mirror is tried, and only when every mirror fails is an error
// returned. When a MUSIC_U cookie is configured it is passed via the `cookie`
// query parameter, which the API service forwards to Netease. Callers must
// check the response `code` field for non-retryable API-level errors (e.g.
// 301 = login required).
func apiGet(path string, target any) error {
	endpoints := shuffledAPIEndpoints()
	if len(endpoints) == 0 {
		pdk.Log(pdk.LogWarn, "api_endpoints not configured: set at least one API endpoint in the plugin settings")
		return errNoAPIEndpoint
	}

	cookieParam := ""
	if musicU := getMusicU(); musicU != "" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		cookieParam = sep + "cookie=" + url.QueryEscape("MUSIC_U="+musicU)
	}

	var lastErr error
	for _, endpoint := range endpoints {
		reqURL := endpoint + path + cookieParam
		pdk.Log(pdk.LogDebug, "fetching Netease API: "+endpoint+path)

		body, statusCode, err := httpGet(reqURL)
		switch {
		case err != nil:
			lastErr = fmt.Errorf("%s: request failed: %w", endpoint, err)
		case statusCode != 200:
			lastErr = fmt.Errorf("%s: returned status %d", endpoint, statusCode)
		default:
			var head struct {
				Code int `json:"code"`
			}
			if jsonErr := json.Unmarshal(body, &head); jsonErr == nil && retryableAPICodes[head.Code] {
				lastErr = fmt.Errorf("%s: rate limited (code %d)", endpoint, head.Code)
				break
			}
			if jsonErr := json.Unmarshal(body, target); jsonErr != nil {
				lastErr = fmt.Errorf("%s: failed to parse response: %w", endpoint, jsonErr)
				break
			}
			return nil
		}
		pdk.Log(pdk.LogDebug, "endpoint attempt failed, trying next: "+lastErr.Error())
	}
	return fmt.Errorf("all endpoints failed: %w", lastErr)
}

// isEnabled checks if a capability is enabled. Defaults to true; "false" disables.
func isEnabled(key string) bool {
	val, exists := host.ConfigGet(key)
	return !exists || val != "false"
}

func getCacheTTLSeconds() int64 {
	days, exists := host.ConfigGetInt(configCacheTTLDays)
	if !exists || days <= 0 {
		days = defaultCacheTTL
	}
	return days * 24 * 60 * 60
}

func kvGet(key string, target any) bool {
	data, exists, err := host.KVStoreGet(key)
	if err != nil {
		pdk.Log(pdk.LogWarn, "KVStore error for key "+key+": "+err.Error())
		return false
	}
	if !exists {
		return false
	}
	if err := json.Unmarshal(data, target); err != nil {
		return false
	}
	return true
}

func kvSet(key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return host.KVStoreSet(key, data)
}

func kvSetWithTTL(key string, value any, ttlSeconds int64) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return host.KVStoreSetWithTTL(key, data, ttlSeconds)
}

func clampLimit(limit, total int) int {
	if limit <= 0 || limit > total {
		return total
	}
	return limit
}

func httpGet(rawURL string) ([]byte, int32, error) {
	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:    "GET",
		URL:       rawURL,
		Headers:   map[string]string{"User-Agent": userAgent},
		TimeoutMs: httpTimeoutMs,
	})
	if err != nil {
		return nil, 0, err
	}
	return resp.Body, resp.StatusCode, nil
}

func httpGetJSON(rawURL string, target any) error {
	body, statusCode, err := httpGet(rawURL)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	if statusCode != 200 {
		return fmt.Errorf("returned status %d", statusCode)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}
	return nil
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// normalizeText collapses runs of horizontal whitespace (tabs, regular spaces,
// non-breaking and narrow no-break spaces, etc.) within each line into single
// regular spaces, while preserving line breaks so the paragraph structure of
// editorial notes and biographies survives. Leading/trailing blank space is
// trimmed. strings.Fields (which splits on unicode.IsSpace) collapses the
// whitespace per line, and splitting on newlines first keeps the paragraph
// breaks intact.
func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// resizeImageURL rewrites a Netease image URL (p*.music.126.net) to request a
// specific square size via the `param` query argument (format: {w}y{h}). The
// scheme is forced to https and any existing query string is replaced.
func resizeImageURL(imageURL string, size int) string {
	u, err := url.Parse(imageURL)
	if err != nil {
		return imageURL
	}
	u.Scheme = "https"
	u.RawQuery = fmt.Sprintf("param=%dy%d", size, size)
	return u.String()
}

func buildImageList(baseURL string) []metadata.ImageInfo {
	sizes := []int{1500, 600, 300}
	images := make([]metadata.ImageInfo, 0, len(sizes))
	for _, size := range sizes {
		images = append(images, metadata.ImageInfo{
			URL:  resizeImageURL(baseURL, size),
			Size: int32(size),
		})
	}
	return images
}
