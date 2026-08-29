package main

import (
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/stretchr/testify/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("lyrics", func() {
	Describe("normalizeLrc", func() {
		It("leaves plain LRC untouched", func() {
			raw := "[00:01.50]hello\n[00:03.00]world"
			Expect(normalizeLrc(raw)).To(Equal(raw))
		})

		It("converts embedded JSON meta lines to standard LRC", func() {
			raw := "{\"t\":0,\"c\":[{\"tx\":\"作词: \"},{\"tx\":\"宋冬野\"}]}\n" +
				"[00:12.34]董小姐"
			Expect(normalizeLrc(raw)).To(Equal("[00:00.00]作词: 宋冬野\n[00:12.34]董小姐"))
		})

		It("keeps the original line when the JSON payload is unusable", func() {
			raw := "{\"t\":0,\"c\":[{\"tx\":\" \"}]}\n[00:01.00]keep"
			Expect(normalizeLrc(raw)).To(Equal(raw))
		})
	})

	Describe("formatLrcTimestamp", func() {
		It("formats milliseconds as [mm:ss.xx]", func() {
			Expect(formatLrcTimestamp(61540)).To(Equal("[01:01.54]"))
		})

		It("clamps negative values to zero", func() {
			Expect(formatLrcTimestamp(-5)).To(Equal("[00:00.00]"))
		})
	})

	Describe("pickSongMatch", func() {
		songs := []neteaseSong{
			{ID: 1, Name: "晴天 (Live)", Artists: []struct {
				Name string `json:"name"`
			}{{Name: "周杰伦"}}},
			{ID: 2, Name: "晴天", Ar: []struct {
				Name string `json:"name"`
			}{{Name: "周杰伦"}}},
		}

		It("prefers the exact title with matching artist (both ar and artists shapes)", func() {
			match := pickSongMatch("晴天", "周杰伦", songs)
			Expect(match).ToNot(BeNil())
			Expect(match.ID).To(Equal(int64(2)))
		})

		It("falls back to the first result when nothing matches exactly", func() {
			match := pickSongMatch("彩虹", "周杰伦", songs)
			Expect(match).ToNot(BeNil())
			Expect(match.ID).To(Equal(int64(1)))
		})

		It("returns nil for empty results", func() {
			Expect(pickSongMatch("晴天", "周杰伦", nil)).To(BeNil())
		})
	})

	Describe("resolveSongID", func() {
		It("returns the cached song ID", func() {
			host.KVStoreMock.On("Get", "song:周杰伦:晴天").Return(mustMarshal(cachedSongID{SongID: 186016}), true, nil)

			id, err := resolveSongID("周杰伦", "晴天")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(int64(186016)))
		})

		It("searches and caches the song ID with the configured TTL", func() {
			host.KVStoreMock.On("Get", "song:周杰伦:晴天").Return([]byte(nil), false, nil)
			mockAPIConfig()
			host.ConfigMock.On("GetInt", configLyricsCacheTTLDays).Return(int64(7), true)

			searchResp := neteaseSongSearchResponse{Code: 200}
			searchResp.Result.Songs = []neteaseSong{{ID: 186016, Name: "晴天"}}
			mockHTTPJSON("/search?keywords=", searchResp)

			host.KVStoreMock.On("SetWithTTL", "song:周杰伦:晴天",
				mustMarshal(cachedSongID{SongID: 186016}), int64(7*24*60*60)).Return(nil)

			id, err := resolveSongID("周杰伦", "晴天")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(int64(186016)))
		})

		It("negative-caches when no song is found", func() {
			host.KVStoreMock.On("Get", "song:x:y").Return([]byte(nil), false, nil)
			mockAPIConfig()

			mockHTTPJSON("/search?keywords=", neteaseSongSearchResponse{Code: 200})

			host.KVStoreMock.On("SetWithTTL", "song:x:y",
				mustMarshal(cachedSongID{}), int64(negativeCacheTTLSeconds)).Return(nil)

			id, err := resolveSongID("x", "y")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(int64(0)))
		})
	})

	Describe("fetchLyrics", func() {
		It("fetches from /lyric/new and caches text, translation and track path", func() {
			host.KVStoreMock.On("Get", "lyrics:186016").Return([]byte(nil), false, nil)
			mockAPIConfig()
			host.ConfigMock.On("GetInt", configLyricsCacheTTLDays).Return(int64(7), true)

			var lyricResp neteaseLyricResponse
			lyricResp.Code = 200
			lyricResp.Lrc.Lyric = "[00:01.00]晴天"
			lyricResp.Tlyric.Lyric = "[00:01.00]sunny day"
			mockHTTPJSON("/lyric/new?id=186016", lyricResp)

			trackPath := "周杰伦/叶惠美/晴天.mp3"
			host.KVStoreMock.On("SetWithTTL", "lyrics:186016",
				mustMarshal(cachedLyrics{
					Text:       "[00:01.00]晴天",
					Translated: "[00:01.00]sunny day",
					Path:       trackPath,
				}), int64(7*24*60*60)).Return(nil)

			lyric, err := fetchLyrics(186016, trackPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(lyric.Text).To(Equal("[00:01.00]晴天"))
			Expect(lyric.Translated).To(Equal("[00:01.00]sunny day"))
			Expect(lyric.Path).To(Equal(trackPath))
		})

		It("serves old cache entries that have no path recorded", func() {
			legacy := mustMarshal(cachedLyrics{Text: "[00:01.00]晴天"})
			host.KVStoreMock.On("Get", "lyrics:186016").Return(legacy, true, nil)

			lyric, err := fetchLyrics(186016, "some/path.mp3")
			Expect(err).ToNot(HaveOccurred())
			Expect(lyric.Text).To(Equal("[00:01.00]晴天"))
			Expect(lyric.Path).To(BeEmpty())
		})

		It("falls back to /lyric when /lyric/new has no lyrics", func() {
			host.KVStoreMock.On("Get", "lyrics:186016").Return([]byte(nil), false, nil)
			mockAPIConfig()
			host.ConfigMock.On("GetInt", configLyricsCacheTTLDays).Return(int64(7), true)

			mockHTTPJSON("/lyric/new?id=186016", neteaseLyricResponse{Code: 200})

			var lyricResp neteaseLyricResponse
			lyricResp.Code = 200
			lyricResp.Lrc.Lyric = "[00:01.00]old endpoint"
			mockHTTPJSON("/lyric?id=186016", lyricResp)

			host.KVStoreMock.On("SetWithTTL", "lyrics:186016", mock.Anything, mock.Anything).Return(nil)

			lyric, err := fetchLyrics(186016, "")
			Expect(err).ToNot(HaveOccurred())
			Expect(lyric.Text).To(Equal("[00:01.00]old endpoint"))
		})

		It("negative-caches empty lyrics", func() {
			host.KVStoreMock.On("Get", "lyrics:186016").Return([]byte(nil), false, nil)
			mockAPIConfig()
			host.ConfigMock.On("GetInt", configLyricsCacheTTLDays).Return(int64(7), true)

			mockHTTPJSON("/lyric/new?id=186016", neteaseLyricResponse{Code: 200})
			mockHTTPJSON("/lyric?id=186016", neteaseLyricResponse{Code: 200})

			host.KVStoreMock.On("SetWithTTL", "lyrics:186016",
				mustMarshal(cachedLyrics{}), int64(7*24*60*60)).Return(nil)

			lyric, err := fetchLyrics(186016, "")
			Expect(err).ToNot(HaveOccurred())
			Expect(lyric.Text).To(BeEmpty())
		})
	})

	Describe("getLyricsCacheTTLSeconds", func() {
		It("falls back to the default when unset", func() {
			host.ConfigMock.On("GetInt", configLyricsCacheTTLDays).Return(int64(0), false)
			Expect(getLyricsCacheTTLSeconds()).To(Equal(int64(7 * 24 * 60 * 60)))
		})

		It("falls back to the default when invalid", func() {
			host.ConfigMock.On("GetInt", configLyricsCacheTTLDays).Return(int64(0), true)
			Expect(getLyricsCacheTTLSeconds()).To(Equal(int64(7 * 24 * 60 * 60)))
		})

		It("uses the dedicated lyrics TTL when configured", func() {
			host.ConfigMock.On("GetInt", configLyricsCacheTTLDays).Return(int64(30), true)
			Expect(getLyricsCacheTTLSeconds()).To(Equal(int64(30 * 24 * 60 * 60)))
		})
	})

	Describe("mergeLrcByTimeline", func() {
		It("appends matching translations in fullwidth parentheses", func() {
			orig := "[00:01.00]晴天\n[00:02.50]小雨\n[03:00.00]outro"
			trans := "[00:01.00]sunny\n[00:02.50]drizzle\n[00:09.00]extra"
			Expect(mergeLrcByTimeline(orig, trans)).To(Equal(
				"[00:01.00]晴天（sunny）\n[00:02.50]小雨（drizzle）\n[03:00.00]outro"))
		})

		It("matches timestamps regardless of fractional precision", func() {
			orig := "[00:01.000]hello"
			trans := "[00:01.00]你好"
			Expect(mergeLrcByTimeline(orig, trans)).To(Equal("[00:01.000]hello（你好）"))
		})

		It("keeps the full tag prefix of multi-tag lines", func() {
			orig := "[00:05.00][00:40.00]repeat"
			trans := "[00:05.00]X"
			Expect(mergeLrcByTimeline(orig, trans)).To(Equal("[00:05.00][00:40.00]repeat（X）"))
		})

		It("leaves lines without timestamps and translation-only lines alone", func() {
			orig := "plain header\n[00:01.00]晴天"
			trans := "no tag here\n[00:01.00]sunny"
			Expect(mergeLrcByTimeline(orig, trans)).To(Equal(
				"plain header\n[00:01.00]晴天（sunny）"))
		})

		It("returns the original when the translation has no timestamps", func() {
			orig := "[00:01.00]晴天"
			Expect(mergeLrcByTimeline(orig, "纯文本翻译")).To(Equal(orig))
		})

		It("normalizes CRLF line endings and trims text when merging", func() {
			orig := "[00:01.00]晴天 \r\n[00:02.50]小雨"
			trans := "[00:01.00]sunny\r\n[00:02.50]drizzle"
			Expect(mergeLrcByTimeline(orig, trans)).To(Equal(
				"[00:01.00]晴天（sunny）\n[00:02.50]小雨（drizzle）"))
		})
	})

	Describe("GetLyrics", func() {
		var agent neteaseMusicAgent

		// enableLyrics registers the opt-in lyrics switch (manifest default
		// is false, so specs must turn it on explicitly).
		enableLyrics := func() {
			host.ConfigMock.On("Get", configLyrics).Return("true", true)
		}

		// unsetDisplayMode registers the display mode as unset (manifest
		// default "original"). Not defaulted in resetMocks so mode specs
		// can register their own value without order shadowing.
		unsetDisplayMode := func() {
			host.ConfigMock.On("Get", configLyricsDisplayMode).Return("", false).Maybe()
		}

		setupSongAndLyrics := func() {
			host.KVStoreMock.On("Get", "song:周杰伦:晴天").Return(mustMarshal(cachedSongID{SongID: 186016}), true, nil)
			host.KVStoreMock.On("Get", "lyrics:186016").Return(mustMarshal(cachedLyrics{
				Text:       "[00:01.00]晴天",
				Translated: "[00:01.00]sunny day",
			}), true, nil)
		}

		It("returns the original first and the translation second when available", func() {
			enableLyrics()
			unsetDisplayMode()
			setupSongAndLyrics()

			resp, err := agent.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
				Artist: "周杰伦",
				Title:  "晴天",
			}})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Lyrics).To(HaveLen(2))
			// Original first; the client decides which entry to display.
			Expect(resp.Lyrics[0].Lang).To(BeEmpty())
			Expect(resp.Lyrics[0].Text).To(Equal("[00:01.00]晴天"))
			Expect(resp.Lyrics[1].Lang).To(Equal("zh"))
			Expect(resp.Lyrics[1].Text).To(Equal("[00:01.00]sunny day"))
		})

		It("returns a single timeline-merged entry in bilingual mode", func() {
			enableLyrics()
			host.ConfigMock.On("Get", configLyricsDisplayMode).Return("bilingual", true)
			setupSongAndLyrics()

			resp, err := agent.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
				Artist: "周杰伦",
				Title:  "晴天",
			}})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Lyrics).To(HaveLen(1))
			Expect(resp.Lyrics[0].Lang).To(BeEmpty())
			Expect(resp.Lyrics[0].Text).To(Equal("[00:01.00]晴天（sunny day）"))
		})

		It("returns a single translation entry in chinese mode", func() {
			enableLyrics()
			host.ConfigMock.On("Get", configLyricsDisplayMode).Return("chinese", true)
			setupSongAndLyrics()

			resp, err := agent.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
				Artist: "周杰伦",
				Title:  "晴天",
			}})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Lyrics).To(HaveLen(1))
			Expect(resp.Lyrics[0].Lang).To(BeEmpty())
			Expect(resp.Lyrics[0].Text).To(Equal("[00:01.00]sunny day"))
		})

		It("falls back to the original when bilingual mode has no translation", func() {
			enableLyrics()
			host.ConfigMock.On("Get", configLyricsDisplayMode).Return("bilingual", true)
			host.KVStoreMock.On("Get", "song:周杰伦:晴天").Return(mustMarshal(cachedSongID{SongID: 186016}), true, nil)
			host.KVStoreMock.On("Get", "lyrics:186016").Return(mustMarshal(cachedLyrics{
				Text: "[00:01.00]晴天",
			}), true, nil)

			resp, err := agent.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
				Artist: "周杰伦",
				Title:  "晴天",
			}})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Lyrics).To(HaveLen(1))
			Expect(resp.Lyrics[0].Text).To(Equal("[00:01.00]晴天"))
		})

		It("falls back to the original when chinese mode has no translation", func() {
			enableLyrics()
			host.ConfigMock.On("Get", configLyricsDisplayMode).Return("chinese", true)
			host.KVStoreMock.On("Get", "song:周杰伦:晴天").Return(mustMarshal(cachedSongID{SongID: 186016}), true, nil)
			host.KVStoreMock.On("Get", "lyrics:186016").Return(mustMarshal(cachedLyrics{
				Text: "[00:01.00]晴天",
			}), true, nil)

			resp, err := agent.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
				Artist: "周杰伦",
				Title:  "晴天",
			}})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Lyrics).To(HaveLen(1))
			Expect(resp.Lyrics[0].Text).To(Equal("[00:01.00]晴天"))
		})

		It("treats an unknown display mode as the default", func() {
			enableLyrics()
			host.ConfigMock.On("Get", configLyricsDisplayMode).Return("weird", true)
			setupSongAndLyrics()

			resp, err := agent.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
				Artist: "周杰伦",
				Title:  "晴天",
			}})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Lyrics).To(HaveLen(2))
			Expect(resp.Lyrics[0].Text).To(Equal("[00:01.00]晴天"))
			Expect(resp.Lyrics[1].Text).To(Equal("[00:01.00]sunny day"))
		})

		It("returns only the original when no translation exists", func() {
			enableLyrics()
			unsetDisplayMode()
			host.KVStoreMock.On("Get", "song:周杰伦:晴天").Return(mustMarshal(cachedSongID{SongID: 186016}), true, nil)
			host.KVStoreMock.On("Get", "lyrics:186016").Return(mustMarshal(cachedLyrics{
				Text: "[00:01.00]晴天",
			}), true, nil)

			resp, err := agent.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
				Artist: "周杰伦",
				Title:  "晴天",
			}})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Lyrics).To(HaveLen(1))
			Expect(resp.Lyrics[0].Text).To(Equal("[00:01.00]晴天"))
		})

		It("returns nothing when not explicitly enabled", func() {
			// Opt-in: unset config (the manifest default) keeps lyrics off.
			host.ConfigMock.On("Get", configLyrics).Return("", false)
			setupSongAndLyrics()

			resp, err := agent.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
				Artist: "周杰伦",
				Title:  "晴天",
			}})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Lyrics).To(BeEmpty())
		})

		It("returns empty (not error) when disabled", func() {
			host.ConfigMock.On("Get", configLyrics).Return("false", true)

			resp, err := agent.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "晴天"}})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Lyrics).To(BeEmpty())
		})

		It("returns errLyricsNotFound when no lyrics exist", func() {
			enableLyrics()
			host.KVStoreMock.On("Get", "song:周杰伦:晴天").Return(mustMarshal(cachedSongID{SongID: 0}), true, nil)

			_, err := agent.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
				Artist: "周杰伦",
				Title:  "晴天",
			}})
			Expect(err).To(MatchError(errLyricsNotFound))
		})

		It("records the requested track path in the lyrics cache", func() {
			enableLyrics()
			unsetDisplayMode()
			host.KVStoreMock.On("Get", "song:周杰伦:晴天").Return(mustMarshal(cachedSongID{SongID: 186016}), true, nil)
			host.KVStoreMock.On("Get", "lyrics:186016").Return([]byte(nil), false, nil)
			mockAPIConfig()
			host.ConfigMock.On("GetInt", configLyricsCacheTTLDays).Return(int64(7), true)

			var lyricResp neteaseLyricResponse
			lyricResp.Code = 200
			lyricResp.Lrc.Lyric = "[00:01.00]晴天"
			mockHTTPJSON("/lyric/new?id=186016", lyricResp)

			host.KVStoreMock.On("SetWithTTL", "lyrics:186016",
				mustMarshal(cachedLyrics{Text: "[00:01.00]晴天", Path: "周杰伦/叶惠美/晴天.mp3"}),
				int64(7*24*60*60)).Return(nil)

			resp, err := agent.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{
				Artist: "周杰伦",
				Title:  "晴天",
				Path:   "周杰伦/叶惠美/晴天.mp3",
			}})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Lyrics[0].Text).To(Equal("[00:01.00]晴天"))
		})

		It("normalizes JSON meta lines when fetching lyrics", func() {
			host.KVStoreMock.On("Get", "lyrics:186016").Return([]byte(nil), false, nil)
			mockAPIConfig()
			host.ConfigMock.On("GetInt", configLyricsCacheTTLDays).Return(int64(7), true)

			var lyricResp neteaseLyricResponse
			lyricResp.Code = 200
			lyricResp.Lrc.Lyric = "{\"t\":0,\"c\":[{\"tx\":\"作曲: \"},{\"tx\":\"周杰伦\"}]}\n[00:12.00]晴天"
			mockHTTPJSON("/lyric/new?id=186016", lyricResp)
			host.KVStoreMock.On("SetWithTTL", "lyrics:186016", mock.Anything, mock.Anything).Return(nil)

			lyric, err := fetchLyrics(186016, "")
			Expect(err).ToNot(HaveOccurred())
			Expect(lyric.Text).To(HavePrefix("[00:00.00]作曲: 周杰伦"))
			Expect(strings.Contains(lyric.Text, "\"tx\"")).To(BeFalse())
		})
	})
})
