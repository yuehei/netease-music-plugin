package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

//go:embed manifest.json
var manifestJSON []byte

// userAgent identifies the plugin to the API endpoints.
var userAgent = "NavidromeNeteaseMusicPlugin/" + pluginVersion()

// pluginVersion reads the version from the embedded manifest, falling back to a
// placeholder if the manifest is missing or malformed.
func pluginVersion() string {
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifestJSON, &m); err != nil || m.Version == "" {
		return "0.0.0"
	}
	return m.Version
}

const (
	neteaseArtistURL        = "https://music.163.com/#/artist?id=%d"
	neteaseAlbumURL         = "https://music.163.com/#/album?id=%d"
	defaultCacheTTL         = 7 // days
	defaultTopSongs         = 10
	httpTimeoutMs           = 10000
	negativeCacheTTLSeconds = 7200 // 2 hours

	// Config keys
	configAPIEndpoints     = "api_endpoints"
	configMusicU           = "music_u"
	configCacheTTLDays     = "cache_ttl_days"
	configArtistExactMatch = "artist_exact_match"
	configArtistIDOverride = "artist_id_overrides"
	configArtistURL        = "enable_artist_url"
	configArtistBiography = "enable_artist_biography"
	configArtistImages    = "enable_artist_images"
	configSimilarArtists  = "enable_similar_artists"
	configTopSongs        = "enable_top_songs"
	configLyrics          = "enable_lyrics"
	configAlbumImages     = "enable_album_images"
	configAlbumInfo       = "enable_album_info"
)

// Compile-time interface assertions
var (
	_ metadata.ArtistURLProvider       = (*neteaseMusicAgent)(nil)
	_ metadata.ArtistBiographyProvider = (*neteaseMusicAgent)(nil)
	_ metadata.ArtistImagesProvider    = (*neteaseMusicAgent)(nil)
	_ metadata.SimilarArtistsProvider  = (*neteaseMusicAgent)(nil)
	_ metadata.ArtistTopSongsProvider  = (*neteaseMusicAgent)(nil)
	_ metadata.AlbumImagesProvider     = (*neteaseMusicAgent)(nil)
	_ metadata.AlbumInfoProvider       = (*neteaseMusicAgent)(nil)
	_ lyrics.Lyrics                    = (*neteaseMusicAgent)(nil)
)

func init() {
	metadata.Register(&neteaseMusicAgent{})
	lyrics.Register(&neteaseMusicAgent{})
}

func main() {}

type neteaseMusicAgent struct{}

// GetArtistURL returns the Netease Cloud Music URL for the artist.
func (a *neteaseMusicAgent) GetArtistURL(input metadata.ArtistRequest) (*metadata.ArtistURLResponse, error) {
	if !isEnabled(configArtistURL) {
		return nil, nil
	}
	artistID, err := resolveArtistID(input.Name)
	if err != nil {
		return nil, err
	}
	if artistID == 0 {
		return nil, nil
	}

	return &metadata.ArtistURLResponse{URL: fmt.Sprintf(neteaseArtistURL, artistID)}, nil
}

// GetArtistBiography returns the artist biography from Netease Cloud Music.
func (a *neteaseMusicAgent) GetArtistBiography(input metadata.ArtistRequest) (*metadata.ArtistBiographyResponse, error) {
	if !isEnabled(configArtistBiography) {
		return nil, nil
	}
	artistID, err := resolveArtistID(input.Name)
	if err != nil {
		return nil, err
	}
	if artistID == 0 {
		return nil, nil
	}

	page, err := fetchArtistPage(artistID)
	if err != nil {
		return nil, err
	}
	if page == nil || page.Biography == "" {
		pdk.Log(pdk.LogDebug, "no biography found for: "+input.Name)
		return nil, nil
	}

	return &metadata.ArtistBiographyResponse{Biography: page.Biography}, nil
}

// GetArtistImages returns artist images from Netease Cloud Music in multiple sizes.
func (a *neteaseMusicAgent) GetArtistImages(input metadata.ArtistRequest) (*metadata.ArtistImagesResponse, error) {
	if !isEnabled(configArtistImages) {
		return nil, nil
	}
	artistID, err := resolveArtistID(input.Name)
	if err != nil {
		return nil, err
	}
	if artistID == 0 {
		return nil, nil
	}

	page, err := fetchArtistPage(artistID)
	if err != nil {
		return nil, err
	}
	if page == nil || page.ImageURL == "" {
		pdk.Log(pdk.LogDebug, "no artist image found for: "+input.Name)
		return nil, nil
	}

	return &metadata.ArtistImagesResponse{Images: buildImageList(page.ImageURL)}, nil
}

// GetSimilarArtists returns similar artists from Netease Cloud Music.
// Requires the MUSIC_U cookie to be configured; degrades gracefully otherwise.
func (a *neteaseMusicAgent) GetSimilarArtists(input metadata.SimilarArtistsRequest) (*metadata.SimilarArtistsResponse, error) {
	if !isEnabled(configSimilarArtists) {
		return nil, nil
	}
	artistID, err := resolveArtistID(input.Name)
	if err != nil {
		return nil, err
	}
	if artistID == 0 {
		return nil, nil
	}

	similar, err := fetchSimilarArtists(artistID)
	if err != nil {
		return nil, err
	}
	if len(similar) == 0 {
		pdk.Log(pdk.LogDebug, "no similar artists found for: "+input.Name)
		return nil, nil
	}

	limit := clampLimit(int(input.Limit), len(similar))

	artists := make([]metadata.ArtistRef, 0, limit)
	for i := 0; i < limit; i++ {
		artists = append(artists, metadata.ArtistRef{
			Name: similar[i].Name,
		})
	}

	return &metadata.SimilarArtistsResponse{Artists: artists}, nil
}

// GetArtistTopSongs returns the artist's hot songs via the Netease API.
func (a *neteaseMusicAgent) GetArtistTopSongs(input metadata.TopSongsRequest) (*metadata.TopSongsResponse, error) {
	if !isEnabled(configTopSongs) {
		return nil, nil
	}
	artistID, err := resolveArtistID(input.Name)
	if err != nil {
		return nil, err
	}
	if artistID == 0 {
		return nil, nil
	}

	count := int(input.Count)
	if count <= 0 {
		count = defaultTopSongs
	}

	// Check cache
	cacheKey := fmt.Sprintf("topsongs:%d:%d", artistID, count)
	var cached metadata.TopSongsResponse
	if kvGet(cacheKey, &cached) {
		pdk.Log(pdk.LogDebug, "top songs cache hit: "+cacheKey)
		return &cached, nil
	}

	songs, err := fetchTopSongs(artistID, count)
	if err != nil {
		return nil, err
	}
	if len(songs) == 0 {
		pdk.Log(pdk.LogDebug, "no top songs found for: "+input.Name)
		return nil, nil
	}

	result := &metadata.TopSongsResponse{Songs: songs}

	// Cache with TTL
	ttl := getCacheTTLSeconds()
	if err := kvSetWithTTL(cacheKey, result, ttl); err != nil {
		pdk.Log(pdk.LogWarn, "failed to cache top songs: "+err.Error())
	}

	return result, nil
}

// GetAlbumImages returns album artwork from Netease Cloud Music in multiple sizes.
func (a *neteaseMusicAgent) GetAlbumImages(input metadata.AlbumRequest) (*metadata.AlbumImagesResponse, error) {
	if !isEnabled(configAlbumImages) {
		return nil, nil
	}

	match, err := resolveAlbumMatch(input.Name, input.Artist)
	if err != nil {
		return nil, err
	}
	if match == nil || match.ArtworkURL == "" {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("no album artwork found for '%s' by '%s'", input.Name, input.Artist))
		return nil, nil
	}

	return &metadata.AlbumImagesResponse{Images: buildImageList(match.ArtworkURL)}, nil
}

// GetAlbumInfo returns the Netease Cloud Music URL and editorial description for an album.
// Uses a dedicated album_info cache (separate from the album-match cache) so that
// a cache hit avoids both the album match KV read and the album detail fetch.
func (a *neteaseMusicAgent) GetAlbumInfo(input metadata.AlbumRequest) (*metadata.AlbumInfoResponse, error) {
	if !isEnabled(configAlbumInfo) {
		return nil, nil
	}

	cacheKey := fmt.Sprintf("album_info:%s:%s", normalizeName(input.Artist), normalizeName(input.Name))
	var cachedInfo cachedAlbumInfo
	if kvGet(cacheKey, &cachedInfo) {
		if cachedInfo.URL == "" {
			return nil, nil
		}
		return &metadata.AlbumInfoResponse{
			Name:        input.Name,
			URL:         cachedInfo.URL,
			Description: cachedInfo.Description,
		}, nil
	}

	match, err := resolveAlbumMatch(input.Name, input.Artist)
	if err != nil {
		return nil, err
	}
	if match == nil || match.AlbumID == 0 {
		return nil, nil
	}

	resp := &metadata.AlbumInfoResponse{
		Name: input.Name,
		URL:  fmt.Sprintf(neteaseAlbumURL, match.AlbumID),
	}

	description, fetched := fetchAlbumDescription(match.AlbumID)
	resp.Description = description
	if !fetched {
		// Fetch failed: return URL but don't cache, so the next call retries.
		return resp, nil
	}

	entry := cachedAlbumInfo{URL: resp.URL, Description: description}
	if err := kvSetWithTTL(cacheKey, entry, getCacheTTLSeconds()); err != nil {
		pdk.Log(pdk.LogWarn, "failed to cache album info: "+err.Error())
	}
	return resp, nil
}

// GetLyrics returns the track's lyrics from Netease Cloud Music as LRC text,
// optionally with the Chinese translation as a second entry. The song is
// located by artist:title search and both the song ID and the lyrics are
// cached with the configured TTL. When no lyrics exist, errLyricsNotFound
// lets Navidrome fall through to the next lyrics agent.
func (a *neteaseMusicAgent) GetLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, error) {
	var empty lyrics.GetLyricsResponse
	if !isOptInEnabled(configLyrics) {
		return empty, nil
	}

	title := strings.TrimSpace(input.Track.Title)
	if title == "" {
		return empty, errLyricsNotFound
	}

	songID, err := resolveSongID(input.Track.Artist, title)
	if err != nil {
		return empty, err
	}
	if songID == 0 {
		return empty, errLyricsNotFound
	}

	lyric, err := fetchLyrics(songID, input.Track.Path)
	if err != nil {
		return empty, err
	}
	if lyric.Text == "" {
		return empty, errLyricsNotFound
	}

	// The original lyrics come first; the Netease translation (when
	// available) is always included as a second lang="zh" entry — the
	// client decides which one to display.
	out := []lyrics.LyricsText{{Text: lyric.Text}}
	if lyric.Translated != "" {
		out = append(out, lyrics.LyricsText{Lang: "zh", Text: lyric.Translated})
	}
	return lyrics.GetLyricsResponse{Lyrics: out}, nil
}
