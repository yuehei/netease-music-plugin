package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

type neteaseAlbum struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	PicURL string `json:"picUrl"`
}

type neteaseArtistAlbumsResponse struct {
	Code      int            `json:"code"`
	HotAlbums []neteaseAlbum `json:"hotAlbums"`
}

type neteaseAlbumDetailResponse struct {
	Code  int `json:"code"`
	Album struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		PicURL      string `json:"picUrl"`
		Description string `json:"description"`
	} `json:"album"`
}

type cachedAlbumMatch struct {
	AlbumID    int64  `json:"albumId,omitempty"`
	ArtworkURL string `json:"artworkUrl,omitempty"`
}

type cachedAlbumInfo struct {
	URL         string `json:"url,omitempty"`
	Description string `json:"description"`
}

var baseNameDelimiters = []string{" (", " [", " - ", ": "}

func extractBaseName(normalized string) string {
	for _, delim := range baseNameDelimiters {
		if idx := strings.Index(normalized, delim); idx > 0 {
			normalized = normalized[:idx]
		}
	}
	return strings.TrimSpace(normalized)
}

func findBestAlbumMatch(albumName string, results []neteaseAlbum) *neteaseAlbum {
	normalizedAlbum := normalizeName(albumName)
	baseAlbum := extractBaseName(normalizedAlbum)

	type candidate struct {
		index          int
		normalizedName string
		baseName       string
	}
	candidates := make([]candidate, 0, len(results))
	for i := range results {
		cn := normalizeName(results[i].Name)
		candidates = append(candidates, candidate{
			index:          i,
			normalizedName: cn,
			baseName:       extractBaseName(cn),
		})
	}

	// Pass 1: exact match on full name
	for _, c := range candidates {
		if c.normalizedName == normalizedAlbum {
			return &results[c.index]
		}
	}

	// Pass 2: exact match on base names
	for _, c := range candidates {
		if c.baseName == baseAlbum {
			return &results[c.index]
		}
	}

	// Pass 3: containment — one base name contains the other.
	// Require the shorter name to be at least 4 characters to avoid false positives.
	if len(baseAlbum) >= 4 {
		for _, c := range candidates {
			if len(c.baseName) >= 4 &&
				(strings.Contains(c.baseName, baseAlbum) || strings.Contains(baseAlbum, c.baseName)) {
				return &results[c.index]
			}
		}
	}

	return nil
}

// readCachedAlbumMatch returns the cached album match for cacheKey. The second
// return value reports whether a usable entry exists: a zero entry means
// "known to not exist" (negative cache), while a legacy entry lacking the
// album ID is treated as a miss so it gets refreshed.
func readCachedAlbumMatch(cacheKey string) (*cachedAlbumMatch, bool) {
	var cached cachedAlbumMatch
	if !kvGet(cacheKey, &cached) {
		return nil, false
	}
	if cached.AlbumID == 0 && cached.ArtworkURL == "" {
		pdk.Log(pdk.LogDebug, "album negative cache hit: "+cacheKey)
		return nil, true
	}
	if cached.AlbumID != 0 {
		pdk.Log(pdk.LogDebug, "album cache hit: "+cacheKey)
		return &cached, true
	}
	pdk.Log(pdk.LogDebug, "album cache entry missing ID, refreshing: "+cacheKey)
	return nil, false
}

func resolveAlbumMatch(albumName, artistName string) (*cachedAlbumMatch, error) {
	normalizedAlbum := normalizeName(albumName)
	normalizedArtist := normalizeName(artistName)
	if normalizedAlbum == "" {
		return nil, errors.New("empty album name")
	}
	if normalizedArtist == "" {
		return nil, errors.New("empty artist name")
	}

	// Check cache
	cacheKey := fmt.Sprintf("album:%s:%s", normalizedArtist, normalizedAlbum)
	if match, ok := readCachedAlbumMatch(cacheKey); ok {
		return match, nil
	}

	// Serialize concurrent resolutions for this album (see resolveArtistID).
	mu := lockForKey(cacheKey)
	mu.Lock()
	defer mu.Unlock()
	if match, ok := readCachedAlbumMatch(cacheKey); ok {
		return match, nil
	}

	// Resolve artist ID first
	artistID, err := resolveArtistID(artistName)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve artist for album lookup: %w", err)
	}
	if artistID == 0 {
		pdk.Log(pdk.LogDebug, "artist not found for album lookup: "+artistName)
		return nil, nil
	}

	// List the artist's albums via the Netease API
	path := fmt.Sprintf("/artist/album?id=%d&limit=200", artistID)

	var albumsResp neteaseArtistAlbumsResponse
	if err := apiGet(path, &albumsResp); err != nil {
		return nil, fmt.Errorf("Netease artist albums: %w", err)
	}
	if albumsResp.Code != 200 {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("artist albums returned code %d for artist %d", albumsResp.Code, artistID))
		return nil, nil
	}

	// Find match by album name (artist already matched via artist ID)
	bestMatch := findBestAlbumMatch(albumName, albumsResp.HotAlbums)

	if bestMatch == nil || (bestMatch.PicURL == "" && bestMatch.ID == 0) {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("no matching album found for '%s' by '%s'", albumName, artistName))
		if err := kvSetWithTTL(cacheKey, cachedAlbumMatch{}, negativeCacheTTLSeconds); err != nil {
			pdk.Log(pdk.LogWarn, "failed to cache negative album result: "+err.Error())
		}
		return nil, nil
	}

	match := &cachedAlbumMatch{
		AlbumID:    bestMatch.ID,
		ArtworkURL: bestMatch.PicURL,
	}

	// Cache with standard TTL
	ttl := getCacheTTLSeconds()
	if err := kvSetWithTTL(cacheKey, match, ttl); err != nil {
		pdk.Log(pdk.LogWarn, "failed to cache album match: "+err.Error())
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("resolved album '%s' by '%s' → ID %d", albumName, artistName, bestMatch.ID))
	return match, nil
}

// fetchAlbumDescription returns the editorial description of an album. The
// second return value reports whether the fetch succeeded, so the caller can
// distinguish "no description" (cacheable) from "fetch failed" (retry later).
func fetchAlbumDescription(albumID int64) (string, bool) {
	path := fmt.Sprintf("/album?id=%d", albumID)

	var albumResp neteaseAlbumDetailResponse
	if err := apiGet(path, &albumResp); err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("failed to fetch album %d: %s", albumID, err.Error()))
		return "", false
	}
	if albumResp.Code != 200 {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("album detail returned code %d for album %d", albumResp.Code, albumID))
		return "", false
	}

	return normalizeText(albumResp.Album.Description), true
}
