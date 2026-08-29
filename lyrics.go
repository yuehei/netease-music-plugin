package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
)

// errLyricsNotFound signals "no lyrics for this track" to Navidrome, so it
// can fall through to the next configured lyrics agent. The lyrics
// capability returns values (not pointers), so an error is the only way to
// report absence without ending the agent chain.
var errLyricsNotFound = errors.New("lyrics not found")

// metaLine 映射网易云混在 lrc 字段里的 JSON 元信息行,例如:
//
//	{"t":0,"c":[{"tx":"作词: "},{"tx":"宋冬野"}]}
//
// 这类行不是标准 LRC([mm:ss.xx]文本),Navidrome 的解析器会丢弃,
// 导致作词/作曲/编曲等信息丢失。
type metaLine struct {
	T int `json:"t"` // 毫秒时间戳
	C []struct {
		Tx string `json:"tx"` // 文本片段,需按序拼接
	} `json:"c"`
}

// normalizeLrc 清洗网易云 lrc 文本:把混入的 JSON 元信息行转换成标准
// LRC 行([mm:ss.xx]文本),其余行原样保留。这样作词/作曲/编曲信息
// 能被 Navidrome 正常解析显示。
func normalizeLrc(raw string) string {
	if !strings.Contains(raw, `"tx"`) {
		return raw // 无 JSON 行,快速返回
	}

	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "{") && strings.Contains(trimmed, `"tx"`) {
			if converted, ok := convertMetaLine(trimmed); ok {
				out = append(out, converted)
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// convertMetaLine 把单行 JSON 元信息转成标准 LRC 行,失败时返回 false。
func convertMetaLine(line string) (string, bool) {
	var m metaLine
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return "", false
	}
	var sb strings.Builder
	for _, frag := range m.C {
		sb.WriteString(frag.Tx)
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "", false
	}
	return formatLrcTimestamp(m.T) + text, true
}

// formatLrcTimestamp 把毫秒转成 LRC 时间标签 [mm:ss.xx]。
func formatLrcTimestamp(ms int) string {
	if ms < 0 {
		ms = 0
	}
	minutes := ms / 60000
	seconds := (ms % 60000) / 1000
	centis := (ms % 1000) / 10
	return fmt.Sprintf("[%02d:%02d.%02d]", minutes, seconds, centis)
}

// mergeLrcByTimeline 把原文 LRC 与中文翻译按时间轴合并为单条歌词:
// 时间戳一致的行合并为"原文（译文）",原文独有的行原样保留,翻译独有
// 的行丢弃(网易云 lrc/tlyric 时间戳基本精确对齐)。时间戳精度差异
// ([00:01.00] 与 [00:01.000])视为同一时间。翻译没有任何时间标签时,
// 无法对齐,直接返回原文。
func mergeLrcByTimeline(original, translated string) string {
	// 部分上游部署可能返回 CRLF 行尾,先归一为 \n,避免 \r 残留在
	// 合并行中间("text\r（译文）")。
	original = normalizeLrcNewlines(original)
	translated = normalizeLrcNewlines(translated)

	transByMs := translationByTimestamp(translated)
	if len(transByMs) == 0 {
		return original
	}

	lines := strings.Split(original, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		prefix, text, ms, ok := splitLrcTags(line)
		text = strings.TrimSpace(text)
		if ok && text != "" {
			if trans, found := transByMs[ms]; found {
				line = prefix + text + "（" + trans + "）"
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// normalizeLrcNewlines 把 \r\n 与孤立的 \r 统一为 \n。
func normalizeLrcNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// translationByTimestamp 把翻译 LRC 解析为 时间戳(毫秒)→文本 的映射。
// 无时间标签或文本为空的行跳过;同一时间戳取第一行。
func translationByTimestamp(raw string) map[int64]string {
	result := map[int64]string{}
	for _, line := range strings.Split(raw, "\n") {
		_, text, ms, ok := splitLrcTags(line)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		if _, dup := result[ms]; !dup {
			result[ms] = strings.TrimSpace(text)
		}
	}
	return result
}

// lrcTagPattern matches a leading LRC time tag like [mm:ss.xx] / [mm:ss.xxx].
var lrcTagPattern = regexp.MustCompile(`^\[(\d{1,3}):(\d{1,2})(?:[.:](\d{1,3}))?\]`)

// splitLrcTags 拆出 LRC 行开头的全部时间标签。返回标签前缀(原样)、
// 剩余文本、第一个标签的毫秒数,以及是否存在时间标签。
func splitLrcTags(line string) (prefix, text string, ms int64, ok bool) {
	rest := line
	var firstMs int64
	hadTag := false
	for {
		m := lrcTagPattern.FindStringSubmatch(rest)
		if m == nil {
			break
		}
		minutes, _ := strconv.ParseInt(m[1], 10, 64)
		seconds, _ := strconv.ParseInt(m[2], 10, 64)
		var frac int64
		switch len(m[3]) {
		case 1: // .x → 百毫秒
			frac, _ = strconv.ParseInt(m[3], 10, 64)
			frac *= 100
		case 2: // .xx → 厘秒
			frac, _ = strconv.ParseInt(m[3], 10, 64)
			frac *= 10
		case 3: // .xxx → 毫秒
			frac, _ = strconv.ParseInt(m[3], 10, 64)
		}
		if !hadTag {
			firstMs = minutes*60000 + seconds*1000 + frac
			hadTag = true
		}
		prefix += rest[:len(m[0])]
		rest = rest[len(m[0]):]
	}
	return prefix, rest, firstMs, hadTag
}

// neteaseSongSearchResponse maps /search?type=1 (songs).
type neteaseSongSearchResponse struct {
	Code   int `json:"code"`
	Result struct {
		Songs []neteaseSong `json:"songs"`
	} `json:"result"`
}

type neteaseSong struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// /search returns `artists`; cloudsearch-style payloads use `ar`.
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Ar []struct {
		Name string `json:"name"`
	} `json:"ar"`
}

// neteaseLyricResponse maps /lyric/new (fallback: /lyric).
type neteaseLyricResponse struct {
	Code int `json:"code"`
	Lrc  struct {
		Lyric string `json:"lyric"`
	} `json:"lrc"`
	Tlyric struct {
		Lyric string `json:"lyric"`
	} `json:"tlyric"`
}

// firstArtistName returns the lead artist name, tolerating both the
// `artists` and `ar` payload shapes.
func (s *neteaseSong) firstArtistName() string {
	if len(s.Artists) > 0 {
		return s.Artists[0].Name
	}
	if len(s.Ar) > 0 {
		return s.Ar[0].Name
	}
	return ""
}

// cachedSongID stores the resolved Netease song ID for an artist:title pair.
// An empty SongID is a negative entry ("known to not exist").
type cachedSongID struct {
	SongID int64 `json:"songId"`
}

// cachedLyrics stores the fetched lyric parts for a song. An entry with an
// empty Text is a valid (negative) cache entry. Path records the track's
// library-relative path (from the lyrics request) so external tools can
// write the lyrics back next to the source file.
type cachedLyrics struct {
	Text       string `json:"text,omitempty"`
	Translated string `json:"translated,omitempty"`
	Path       string `json:"path,omitempty"`
}

// resolveSongID maps an artist:title pair to a Netease song ID via
// /search?type=1. Results are cached with the configured lyrics TTL (song
// matching is fuzzier than artist matching, so entries expire instead of
// living forever); "not found" is negative-cached for 2h to throttle retries.
func resolveSongID(artistName, title string) (int64, error) {
	cacheKey := "song:" + normalizeName(artistName) + ":" + normalizeName(title)

	var cached cachedSongID
	if kvGet(cacheKey, &cached) {
		return cached.SongID, nil
	}

	keywords := strings.TrimSpace(artistName + " " + title)
	path := fmt.Sprintf("/search?keywords=%s&type=1&limit=5", url.QueryEscape(keywords))

	var searchResp neteaseSongSearchResponse
	if err := apiGet(path, &searchResp); err != nil {
		return 0, fmt.Errorf("Netease song search: %w", err)
	}
	if searchResp.Code != 200 {
		return 0, fmt.Errorf("song search for '%s' returned code %d", keywords, searchResp.Code)
	}

	song := pickSongMatch(title, artistName, searchResp.Result.Songs)
	if song == nil {
		pdk.Log(pdk.LogDebug, "no song found for: "+keywords)
		if err := kvSetWithTTL(cacheKey, cachedSongID{}, negativeCacheTTLSeconds); err != nil {
			pdk.Log(pdk.LogWarn, "failed to cache negative song result: "+err.Error())
		}
		return 0, nil
	}

	if err := kvSetWithTTL(cacheKey, cachedSongID{SongID: song.ID}, getLyricsCacheTTLSeconds()); err != nil {
		pdk.Log(pdk.LogWarn, "failed to cache song ID: "+err.Error())
	} else {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("cached song ID: %s → %d", cacheKey, song.ID))
	}
	return song.ID, nil
}

// pickSongMatch selects the result whose title matches exactly (case
// insensitive) and whose lead artist matches the queried artist, falling
// back to the first result (Netease ranks by relevance).
func pickSongMatch(title, artist string, songs []neteaseSong) *neteaseSong {
	for i := range songs {
		if !strings.EqualFold(songs[i].Name, title) {
			continue
		}
		if artist == "" || containsFold(artist, songs[i].firstArtistName()) ||
			containsFold(songs[i].firstArtistName(), artist) {
			return &songs[i]
		}
	}
	if len(songs) > 0 {
		return &songs[0]
	}
	return nil
}

// containsFold reports whether s contains substr, case-insensitively.
func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// fetchLyrics returns the song's lyrics (and Chinese translation when
// available) from /lyric/new, falling back to the classic /lyric endpoint
// for older NeteaseCloudMusicApi deployments. Results (including "no
// lyrics") are cached with the configured lyrics TTL. trackPath (the
// library-relative path of the requesting track, when available) is recorded
// in the cache so external tools can write the lyrics back to the source file.
func fetchLyrics(songID int64, trackPath string) (*cachedLyrics, error) {
	cacheKey := fmt.Sprintf("lyrics:%d", songID)

	var cached cachedLyrics
	if kvGet(cacheKey, &cached) {
		pdk.Log(pdk.LogDebug, "lyrics cache hit: "+cacheKey)
		return &cached, nil
	}

	paths := []string{
		fmt.Sprintf("/lyric/new?id=%d", songID),
		fmt.Sprintf("/lyric?id=%d", songID),
	}
	for _, path := range paths {
		var lyricResp neteaseLyricResponse
		if err := apiGet(path, &lyricResp); err != nil {
			return nil, fmt.Errorf("Netease lyrics: %w", err)
		}
		if lyricResp.Code != 200 {
			pdk.Log(pdk.LogDebug, fmt.Sprintf("lyrics endpoint %s returned code %d for song %d", path, lyricResp.Code, songID))
			continue
		}
		if strings.TrimSpace(lyricResp.Lrc.Lyric) == "" {
			continue
		}

		lyrics := &cachedLyrics{
			Text:       normalizeLrc(lyricResp.Lrc.Lyric),
			Translated: normalizeLrc(lyricResp.Tlyric.Lyric),
			Path:       trackPath,
		}
		if err := kvSetWithTTL(cacheKey, lyrics, getLyricsCacheTTLSeconds()); err != nil {
			pdk.Log(pdk.LogWarn, "failed to cache lyrics: "+err.Error())
		} else {
			pdk.Log(pdk.LogDebug, fmt.Sprintf("cached lyrics: %s", cacheKey))
		}
		return lyrics, nil
	}

	// No lyrics on either endpoint: negative-cache to avoid refetching.
	pdk.Log(pdk.LogDebug, fmt.Sprintf("no lyrics found for song %d", songID))
	if err := kvSetWithTTL(cacheKey, cachedLyrics{}, getLyricsCacheTTLSeconds()); err != nil {
		pdk.Log(pdk.LogWarn, "failed to cache negative lyrics: "+err.Error())
	}
	return &cachedLyrics{}, nil
}
