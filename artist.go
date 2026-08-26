package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

type neteaseArtist struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type neteaseArtistSearchResponse struct {
	Code   int `json:"code"`
	Result struct {
		Artists []neteaseArtist `json:"artists"`
	} `json:"result"`
}

type neteaseArtistDetailResponse struct {
	Code int `json:"code"`
	Data struct {
		Artist struct {
			ID        int64  `json:"id"`
			Name      string `json:"name"`
			Cover     string `json:"cover"`
			Avatar    string `json:"avatar"`
			BriefDesc string `json:"briefDesc"`
		} `json:"artist"`
	} `json:"data"`
}

type neteaseTopSongResponse struct {
	Code  int `json:"code"`
	Songs []struct {
		Name    string `json:"name"`
		Artists []struct {
			Name string `json:"name"`
		} `json:"ar"`
	} `json:"songs"`
}

type neteaseSimiArtistResponse struct {
	Code    int             `json:"code"`
	Artists []neteaseArtist `json:"artists"`
}

type cachedArtistID struct {
	ArtistID int64 `json:"artistId"`
}

// cachedArtistPage stores the artist detail fields shared by the biography
// and images capabilities, so one fetch serves both.
type cachedArtistPage struct {
	Biography string `json:"biography,omitempty"`
	ImageURL  string `json:"imageURL,omitempty"`
}

// readCachedArtistID returns the cached resolution for cacheKey. The second
// return value reports whether any cache entry exists — including negative
// entries (ArtistID == 0), which mean "known to not exist", not "missing".
func readCachedArtistID(cacheKey string) (int64, bool) {
	var cached cachedArtistID
	if !kvGet(cacheKey, &cached) {
		return 0, false
	}
	if cached.ArtistID == 0 {
		pdk.Log(pdk.LogDebug, "artist ID negative cache hit: "+cacheKey)
		return 0, true
	}
	pdk.Log(pdk.LogDebug, "artist ID cache hit: "+cacheKey)
	return cached.ArtistID, true
}

// artistResolveCall tracks one in-flight resolveArtistID call. Concurrent
// callers for the same artist wait on done and then share the leader's
// result — success, "not found", AND failure — so a burst of capability
// calls issues only one /search even when the upstream is rate-limiting.
// The result lives only in memory for the current burst; nothing is
// persisted, keeping the "no negative cache on rate limiting" rule.
type artistResolveCall struct {
	done chan struct{}
	id   int64
	err  error
}

// artistResolveInflight maps an artist cache key to its in-flight call.
var artistResolveInflight sync.Map // string -> *artistResolveCall

func resolveArtistID(artistName string) (int64, error) {
	normalized := normalizeName(artistName)
	if normalized == "" {
		return 0, errors.New("empty artist name")
	}

	// Check cache
	cacheKey := "artist:" + normalized
	if id, ok := readCachedArtistID(cacheKey); ok {
		return id, nil
	}

	// Join or start the in-flight resolution for this artist.
	call := &artistResolveCall{done: make(chan struct{})}
	if actual, loaded := artistResolveInflight.LoadOrStore(cacheKey, call); loaded {
		shared := actual.(*artistResolveCall)
		<-shared.done
		return shared.id, shared.err
	}

	// Leader: LIFO defers close(done) before deleting the map entry, so
	// waiters always observe the published result.
	defer artistResolveInflight.Delete(cacheKey)
	defer close(call.done)
	call.id, call.err = resolveArtistIDFetch(cacheKey, artistName, normalized)
	return call.id, call.err
}

// resolveArtistIDFetch performs the actual /search lookup and cache writes.
// Called only by the resolveArtistID leader.
func resolveArtistIDFetch(cacheKey, artistName, normalized string) (int64, error) {
	// Double-check: a just-finished leader may have populated the cache
	// between our fast-path check and becoming the leader.
	if id, ok := readCachedArtistID(cacheKey); ok {
		return id, nil
	}

	// Search Netease API (type=100: artists)
	path := fmt.Sprintf("/search?keywords=%s&type=100&limit=5", url.QueryEscape(artistName))

	var searchResp neteaseArtistSearchResponse
	if err := apiGet(path, &searchResp); err != nil {
		return 0, fmt.Errorf("Netease artist search: %w", err)
	}

	if searchResp.Code != 200 {
		// API-level failure (e.g. login required, upstream error): NOT a
		// genuine "not found". Rate-limit codes are already retried across
		// mirrors inside apiGet, so reaching here means a non-retryable
		// code. Return an error without negative-caching, so the next scan
		// retries instead of treating the artist as missing.
		return 0, fmt.Errorf("artist search for '%s' returned code %d", artistName, searchResp.Code)
	}
	if len(searchResp.Result.Artists) == 0 {
		// Genuine "no result": negative-cache for 2h to throttle retries
		// for obscure names.
		pdk.Log(pdk.LogDebug, "no artist found for: "+artistName)
		if err := kvSetWithTTL(cacheKey, cachedArtistID{ArtistID: 0}, negativeCacheTTLSeconds); err != nil {
			pdk.Log(pdk.LogWarn, "failed to cache negative artist result: "+err.Error())
		} else {
			pdk.Log(pdk.LogDebug, "cached negative artist ID: "+cacheKey)
		}
		return 0, nil
	}

	// Find best match by name similarity
	bestMatch := findBestArtistMatch(artistName, searchResp.Result.Artists)
	if bestMatch == nil {
		pdk.Log(pdk.LogDebug, "no matching artist found for: "+artistName)
		if err := kvSetWithTTL(cacheKey, cachedArtistID{ArtistID: 0}, negativeCacheTTLSeconds); err != nil {
			pdk.Log(pdk.LogWarn, "failed to cache negative artist result: "+err.Error())
		} else {
			pdk.Log(pdk.LogDebug, "cached negative artist ID: "+cacheKey)
		}
		return 0, nil
	}

	if normalizeName(bestMatch.Name) != normalized {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("no exact match for '%s', using best candidate '%s' (ID %d)", artistName, bestMatch.Name, bestMatch.ID))
	}

	// Cache permanently
	if err := kvSet(cacheKey, cachedArtistID{ArtistID: bestMatch.ID}); err != nil {
		pdk.Log(pdk.LogWarn, "failed to cache artist ID: "+err.Error())
	} else {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("cached artist ID: %s → %d", cacheKey, bestMatch.ID))
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("resolved artist '%s' → ID %d", artistName, bestMatch.ID))
	return bestMatch.ID, nil
}

// findBestArtistMatch returns the exact name match, or the first result when
// no exact match exists (Netease ranks results by relevance).
func findBestArtistMatch(query string, results []neteaseArtist) *neteaseArtist {
	normalized := normalizeName(query)
	for i := range results {
		if normalizeName(results[i].Name) == normalized {
			return &results[i]
		}
	}
	if len(results) > 0 {
		return &results[0]
	}
	return nil
}

// fetchArtistPage returns the artist's biography and image, from cache when
// available. An entry with both fields empty is a valid (negative) cache entry.
func fetchArtistPage(artistID int64) (*cachedArtistPage, error) {
	cacheKey := fmt.Sprintf("page:%d", artistID)

	var cached cachedArtistPage
	if kvGet(cacheKey, &cached) {
		pdk.Log(pdk.LogDebug, "page cache hit: "+cacheKey)
		return &cached, nil
	}

	path := fmt.Sprintf("/artist/detail?id=%d", artistID)

	var detailResp neteaseArtistDetailResponse
	if err := apiGet(path, &detailResp); err != nil {
		return nil, fmt.Errorf("Netease artist detail: %w", err)
	}
	if detailResp.Code != 200 {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("artist detail returned code %d for artist %d", detailResp.Code, artistID))
		return nil, nil
	}

	artist := detailResp.Data.Artist
	page := &cachedArtistPage{
		Biography: normalizeText(artist.BriefDesc),
		ImageURL:  artist.Avatar,
	}
	if page.ImageURL == "" {
		page.ImageURL = artist.Cover
	}

	if err := kvSetWithTTL(cacheKey, page, getCacheTTLSeconds()); err != nil {
		pdk.Log(pdk.LogWarn, "failed to cache page data: "+err.Error())
	}

	return page, nil
}

// fetchSimilarArtists calls /simi/artist, which requires a logged-in cookie.
// When the API reports "login required" (or any non-200 code), it degrades
// gracefully and returns an empty list.
func fetchSimilarArtists(artistID int64) ([]neteaseArtist, error) {
	cacheKey := fmt.Sprintf("simi:%d", artistID)

	var cached []neteaseArtist
	if kvGet(cacheKey, &cached) {
		pdk.Log(pdk.LogDebug, "similar artists cache hit: "+cacheKey)
		return cached, nil
	}

	path := fmt.Sprintf("/simi/artist?id=%d", artistID)

	var simiResp neteaseSimiArtistResponse
	if err := apiGet(path, &simiResp); err != nil {
		return nil, fmt.Errorf("Netease similar artists: %w", err)
	}
	if simiResp.Code != 200 {
		// 301 = login required: MUSIC_U not configured or expired.
		pdk.Log(pdk.LogDebug, fmt.Sprintf("similar artists returned code %d for artist %d (login required?)", simiResp.Code, artistID))
		return nil, nil
	}

	artists := simiResp.Artists
	if artists == nil {
		artists = []neteaseArtist{}
	}
	if err := kvSetWithTTL(cacheKey, artists, getCacheTTLSeconds()); err != nil {
		pdk.Log(pdk.LogWarn, "failed to cache similar artists: "+err.Error())
	}

	return artists, nil
}

// fetchTopSongs returns the artist's hot songs (up to ~50) from
// /artist/top/song. Artist names of a track are joined with " / ".
func fetchTopSongs(artistID int64, count int) ([]metadata.SongRef, error) {
	path := fmt.Sprintf("/artist/top/song?id=%d", artistID)

	var topResp neteaseTopSongResponse
	if err := apiGet(path, &topResp); err != nil {
		return nil, fmt.Errorf("Netease top songs: %w", err)
	}
	if topResp.Code != 200 {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("top songs returned code %d for artist %d", topResp.Code, artistID))
		return nil, nil
	}

	songs := make([]metadata.SongRef, 0, min(count, len(topResp.Songs)))
	for i, s := range topResp.Songs {
		if i >= count {
			break
		}
		if s.Name == "" {
			continue
		}
		names := make([]string, 0, len(s.Artists))
		for _, a := range s.Artists {
			names = append(names, a.Name)
		}
		songs = append(songs, metadata.SongRef{
			Name:   s.Name,
			Artist: strings.Join(names, " / "),
		})
	}
	return songs, nil
}
