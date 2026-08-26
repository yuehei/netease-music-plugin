package main

import (
	"errors"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/stretchr/testify/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const testArtistID = int64(6452)

// mockAPIConfig registers the API endpoint (no cookie) used by apiGet.
func mockAPIConfig() {
	host.ConfigMock.On("Get", configAPIEndpoints).Return("https://api.example.com", true)
	host.ConfigMock.On("Get", configMusicU).Return("", false)
}

func mockHTTPJSON(urlPart string, body any) {
	host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
		return req.Method == "GET" && strings.Contains(req.URL, urlPart)
	})).Return(&host.HTTPResponse{StatusCode: 200, Body: mustMarshal(body)}, nil)
}

var _ = Describe("artist", func() {
	Describe("findBestArtistMatch", func() {
		It("returns exact case-insensitive match", func() {
			results := []neteaseArtist{
				{Name: "Taylor", ID: 1},
				{Name: "Taylor Swift", ID: 2},
			}
			match := findBestArtistMatch("taylor swift", results)
			Expect(match).ToNot(BeNil())
			Expect(match.ID).To(Equal(int64(2)))
		})

		It("falls back to first result when no exact match", func() {
			results := []neteaseArtist{
				{Name: "Some Artist", ID: 1},
				{Name: "Other Artist", ID: 2},
			}
			match := findBestArtistMatch("query", results)
			Expect(match).ToNot(BeNil())
			Expect(match.ID).To(Equal(int64(1)))
		})

		It("returns nil for empty results", func() {
			Expect(findBestArtistMatch("anything", nil)).To(BeNil())
		})
	})

	Describe("resolveArtistID", func() {
		It("returns cached artist ID", func() {
			data := mustMarshal(cachedArtistID{ArtistID: testArtistID})
			host.KVStoreMock.On("Get", "artist:周杰伦").Return(data, true, nil)

			id, err := resolveArtistID("周杰伦")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(testArtistID))
		})

		It("returns 0 on negative cache hit", func() {
			data := mustMarshal(cachedArtistID{ArtistID: 0})
			host.KVStoreMock.On("Get", "artist:nobody").Return(data, true, nil)

			id, err := resolveArtistID("Nobody")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(int64(0)))
		})

		It("searches the API on cache miss and caches the result", func() {
			host.KVStoreMock.On("Get", "artist:周杰伦").Return([]byte(nil), false, nil)
			mockAPIConfig()

			searchResp := neteaseArtistSearchResponse{Code: 200}
			searchResp.Result.Artists = []neteaseArtist{{Name: "周杰伦", ID: testArtistID}}
			mockHTTPJSON("/search?keywords=", searchResp)

			cachedData := mustMarshal(cachedArtistID{ArtistID: testArtistID})
			host.KVStoreMock.On("Set", "artist:周杰伦", cachedData).Return(nil)

			id, err := resolveArtistID("周杰伦")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(testArtistID))
		})

		It("caches a negative result when no artist is found", func() {
			host.KVStoreMock.On("Get", "artist:nobody").Return([]byte(nil), false, nil)
			mockAPIConfig()

			mockHTTPJSON("/search?keywords=", neteaseArtistSearchResponse{Code: 200})

			host.KVStoreMock.On("SetWithTTL", "artist:nobody",
				mustMarshal(cachedArtistID{ArtistID: 0}), int64(negativeCacheTTLSeconds)).Return(nil)

			id, err := resolveArtistID("Nobody")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(int64(0)))
		})

		It("returns error without negative-caching on rate-limit codes", func() {
			host.KVStoreMock.On("Get", "artist:unlucky").Return([]byte(nil), false, nil)
			mockAPIConfig()

			// HTTP 200 but API code 460 (rate limited): retried across mirrors
			// inside apiGet; with a single mirror it surfaces as an error.
			mockHTTPJSON("/search?keywords=", neteaseArtistSearchResponse{Code: 460})

			// No SetWithTTL expectation: rate limiting must NOT be cached as
			// "not found". (An unexpected SetWithTTL call would panic the mock.)

			id, err := resolveArtistID("Unlucky")
			Expect(err).To(HaveOccurred())
			Expect(id).To(Equal(int64(0)))
		})

		It("returns error without negative-caching on other API-level codes", func() {
			host.KVStoreMock.On("Get", "artist:weird").Return([]byte(nil), false, nil)
			mockAPIConfig()

			// Non-retryable API code (e.g. 500-class upstream error) reaches the
			// caller: error out, do not negative-cache.
			mockHTTPJSON("/search?keywords=", neteaseArtistSearchResponse{Code: 500})

			id, err := resolveArtistID("Weird")
			Expect(err).To(HaveOccurred())
			Expect(id).To(Equal(int64(0)))
		})

		It("returns error when the request fails", func() {
			host.KVStoreMock.On("Get", "artist:x").Return([]byte(nil), false, nil)
			mockAPIConfig()
			host.HTTPMock.On("Send", mock.Anything).Return(&host.HTTPResponse{StatusCode: 500, Body: []byte("err")}, nil)

			_, err := resolveArtistID("X")
			Expect(err).To(HaveOccurred())
		})

		It("shares an in-flight success with concurrent callers", func() {
			host.KVStoreMock.On("Get", "artist:shared").Return([]byte(nil), false, nil)

			// Pre-register a completed in-flight call: resolveArtistID must join
			// it and return the shared result without any HTTP request (no HTTP
			// mock is registered, so an unexpected Send would panic the mock).
			call := &artistResolveCall{done: make(chan struct{})}
			call.id = 4242
			close(call.done)
			artistResolveInflight.Store("artist:shared", call)
			defer artistResolveInflight.Delete("artist:shared")

			id, err := resolveArtistID("Shared")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(int64(4242)))
		})

		It("shares an in-flight failure with concurrent callers", func() {
			host.KVStoreMock.On("Get", "artist:badburst").Return([]byte(nil), false, nil)

			rateLimited := errors.New("endpoint attempts exhausted: rate limited (code 460)")
			call := &artistResolveCall{done: make(chan struct{})}
			call.err = rateLimited
			close(call.done)
			artistResolveInflight.Store("artist:badburst", call)
			defer artistResolveInflight.Delete("artist:badburst")

			id, err := resolveArtistID("Badburst")
			Expect(err).To(MatchError(rateLimited))
			Expect(id).To(Equal(int64(0)))
		})
	})

	Describe("fetchArtistPage", func() {
		It("returns cached page data", func() {
			page := cachedArtistPage{Biography: "bio", ImageURL: "https://img/1.jpg"}
			host.KVStoreMock.On("Get", "page:6452").Return(mustMarshal(page), true, nil)

			result, err := fetchArtistPage(testArtistID)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Biography).To(Equal("bio"))
			Expect(result.ImageURL).To(Equal("https://img/1.jpg"))
		})

		It("fetches and caches the artist detail, preferring avatar over cover", func() {
			host.KVStoreMock.On("Get", "page:6452").Return([]byte(nil), false, nil)
			mockAPIConfig()
			host.ConfigMock.On("GetInt", configCacheTTLDays).Return(int64(7), true)

			var detailResp neteaseArtistDetailResponse
			detailResp.Code = 200
			detailResp.Data.Artist.ID = testArtistID
			detailResp.Data.Artist.Name = "周杰伦"
			detailResp.Data.Artist.Avatar = "http://p4.music.126.net/avatar.jpg"
			detailResp.Data.Artist.Cover = "http://p4.music.126.net/cover.jpg"
			detailResp.Data.Artist.BriefDesc = "台湾  歌手\n演员"
			mockHTTPJSON("/artist/detail?id=6452", detailResp)

			host.KVStoreMock.On("SetWithTTL", "page:6452", mock.Anything, mock.Anything).Return(nil)

			result, err := fetchArtistPage(testArtistID)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ImageURL).To(Equal("http://p4.music.126.net/avatar.jpg"))
			Expect(result.Biography).To(Equal("台湾 歌手\n演员"))
		})

		It("falls back to cover when avatar is empty", func() {
			host.KVStoreMock.On("Get", "page:6452").Return([]byte(nil), false, nil)
			mockAPIConfig()
			host.ConfigMock.On("GetInt", configCacheTTLDays).Return(int64(7), true)

			var detailResp neteaseArtistDetailResponse
			detailResp.Code = 200
			detailResp.Data.Artist.Cover = "http://p4.music.126.net/cover.jpg"
			mockHTTPJSON("/artist/detail?id=6452", detailResp)

			host.KVStoreMock.On("SetWithTTL", "page:6452", mock.Anything, mock.Anything).Return(nil)

			result, err := fetchArtistPage(testArtistID)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.ImageURL).To(Equal("http://p4.music.126.net/cover.jpg"))
		})

		It("returns nil when the API reports a non-200 code", func() {
			host.KVStoreMock.On("Get", "page:6452").Return([]byte(nil), false, nil)
			mockAPIConfig()

			mockHTTPJSON("/artist/detail?id=6452", neteaseArtistDetailResponse{Code: 404})

			result, err := fetchArtistPage(testArtistID)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(BeNil())
		})
	})

	Describe("fetchSimilarArtists", func() {
		It("returns cached similar artists", func() {
			cached := []neteaseArtist{{Name: "杨宗纬", ID: 6066}}
			host.KVStoreMock.On("Get", "simi:6452").Return(mustMarshal(cached), true, nil)

			result, err := fetchSimilarArtists(testArtistID)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(1))
			Expect(result[0].Name).To(Equal("杨宗纬"))
		})

		It("fetches and caches similar artists", func() {
			host.KVStoreMock.On("Get", "simi:6452").Return([]byte(nil), false, nil)
			mockAPIConfig()
			host.ConfigMock.On("GetInt", configCacheTTLDays).Return(int64(7), true)

			simiResp := neteaseSimiArtistResponse{
				Code:    200,
				Artists: []neteaseArtist{{Name: "杨宗纬", ID: 6066}, {Name: "王力宏", ID: 5346}},
			}
			mockHTTPJSON("/simi/artist?id=6452", simiResp)

			host.KVStoreMock.On("SetWithTTL", "simi:6452", mock.Anything, mock.Anything).Return(nil)

			result, err := fetchSimilarArtists(testArtistID)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(HaveLen(2))
		})

		It("degrades gracefully when login is required (code 301)", func() {
			host.KVStoreMock.On("Get", "simi:6452").Return([]byte(nil), false, nil)
			mockAPIConfig()

			mockHTTPJSON("/simi/artist?id=6452", neteaseSimiArtistResponse{Code: 301})

			result, err := fetchSimilarArtists(testArtistID)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(BeEmpty())
		})
	})

	Describe("fetchTopSongs", func() {
		It("joins multiple artist names and respects the count", func() {
			mockAPIConfig()

			var topResp neteaseTopSongResponse
			topResp.Code = 200
			song := func(name string, artists ...string) struct {
				Name    string `json:"name"`
				Artists []struct {
					Name string `json:"name"`
				} `json:"ar"`
			} {
				var s struct {
					Name    string `json:"name"`
					Artists []struct {
						Name string `json:"name"`
					} `json:"ar"`
				}
				s.Name = name
				for _, a := range artists {
					s.Artists = append(s.Artists, struct {
						Name string `json:"name"`
					}{Name: a})
				}
				return s
			}
			topResp.Songs = append(topResp.Songs, song("布拉格广场", "蔡依林", "周杰伦"))
			topResp.Songs = append(topResp.Songs, song("晴天", "周杰伦"))
			topResp.Songs = append(topResp.Songs, song("稻香", "周杰伦"))
			mockHTTPJSON("/artist/top/song?id=6452", topResp)

			songs, err := fetchTopSongs(testArtistID, 2)
			Expect(err).ToNot(HaveOccurred())
			Expect(songs).To(HaveLen(2))
			Expect(songs[0].Name).To(Equal("布拉格广场"))
			Expect(songs[0].Artist).To(Equal("蔡依林 / 周杰伦"))
			Expect(songs[1].Name).To(Equal("晴天"))
		})

		It("returns empty when the API reports a non-200 code", func() {
			mockAPIConfig()
			mockHTTPJSON("/artist/top/song?id=6452", neteaseTopSongResponse{Code: 500})

			songs, err := fetchTopSongs(testArtistID, 10)
			Expect(err).ToNot(HaveOccurred())
			Expect(songs).To(BeEmpty())
		})
	})
})
