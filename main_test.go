package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/stretchr/testify/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// setupArtistCache pre-caches the Taylor Swift artist ID and configures the
// TTL mock used by most capability method tests.
func setupArtistCache() {
	host.KVStoreMock.On("Get", "artist:taylor swift").Return(
		mustMarshal(cachedArtistID{ArtistID: testArtistID}), true, nil,
	)
	host.ConfigMock.On("GetInt", configCacheTTLDays).Return(int64(7), true)
}

var _ = Describe("userAgent", func() {
	It("is derived from the manifest.json version", func() {
		raw, err := os.ReadFile("manifest.json")
		Expect(err).ToNot(HaveOccurred())
		var m struct {
			Version string `json:"version"`
		}
		Expect(json.Unmarshal(raw, &m)).To(Succeed())
		Expect(m.Version).ToNot(BeEmpty())
		Expect(userAgent).To(Equal("NavidromeNeteaseMusicPlugin/" + m.Version))
	})
})

var _ = Describe("neteaseMusicAgent", func() {
	Describe("GetArtistURL", func() {
		var agent neteaseMusicAgent

		BeforeEach(func() {
			setupArtistCache()
		})

		It("returns Netease Cloud Music URL", func() {
			resp, err := agent.GetArtistURL(metadata.ArtistRequest{Name: "Taylor Swift"})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.URL).To(Equal(fmt.Sprintf("https://music.163.com/#/artist?id=%d", testArtistID)))
		})

		It("returns nil when disabled", func() {
			host.ConfigMock.ExpectedCalls = nil
			host.ConfigMock.On("Get", configArtistURL).Return("false", true)
			resp, err := agent.GetArtistURL(metadata.ArtistRequest{Name: "Taylor Swift"})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp).To(BeNil())
		})

		It("returns nil when artist is not found", func() {
			host.KVStoreMock.On("Get", "artist:nobody").Return(
				mustMarshal(cachedArtistID{ArtistID: 0}), true, nil)
			resp, err := agent.GetArtistURL(metadata.ArtistRequest{Name: "Nobody"})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp).To(BeNil())
		})
	})

	Describe("GetArtistBiography", func() {
		var agent neteaseMusicAgent

		BeforeEach(func() {
			setupArtistCache()
		})

		It("returns biography from cached page", func() {
			pageData := cachedArtistPage{Biography: "Taylor Swift biography"}
			host.KVStoreMock.On("Get", "page:6452").Return(mustMarshal(pageData), true, nil)

			resp, err := agent.GetArtistBiography(metadata.ArtistRequest{Name: "Taylor Swift"})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Biography).To(Equal("Taylor Swift biography"))
		})

		It("returns nil when no biography found", func() {
			pageData := cachedArtistPage{ImageURL: "https://img.com/img.jpg"}
			host.KVStoreMock.On("Get", "page:6452").Return(mustMarshal(pageData), true, nil)

			resp, err := agent.GetArtistBiography(metadata.ArtistRequest{Name: "Taylor Swift"})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp).To(BeNil())
		})
	})

	Describe("GetArtistImages", func() {
		var agent neteaseMusicAgent

		BeforeEach(func() {
			setupArtistCache()
		})

		It("returns images in multiple sizes", func() {
			pageData := cachedArtistPage{ImageURL: "http://p4.music.126.net/abcdef/123.jpg"}
			host.KVStoreMock.On("Get", "page:6452").Return(mustMarshal(pageData), true, nil)

			resp, err := agent.GetArtistImages(metadata.ArtistRequest{Name: "Taylor Swift"})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Images).To(HaveLen(3))
			Expect(resp.Images[0].Size).To(Equal(int32(1500)))
			Expect(resp.Images[0].URL).To(Equal("https://p4.music.126.net/abcdef/123.jpg?param=1500y1500"))
			Expect(resp.Images[1].Size).To(Equal(int32(600)))
			Expect(resp.Images[2].Size).To(Equal(int32(300)))
		})

		It("returns nil when no image found", func() {
			pageData := cachedArtistPage{Biography: "bio only"}
			host.KVStoreMock.On("Get", "page:6452").Return(mustMarshal(pageData), true, nil)

			resp, err := agent.GetArtistImages(metadata.ArtistRequest{Name: "Taylor Swift"})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp).To(BeNil())
		})
	})

	Describe("GetSimilarArtists", func() {
		var agent neteaseMusicAgent

		BeforeEach(func() {
			setupArtistCache()
		})

		It("returns similar artists limited by the request", func() {
			cached := []neteaseArtist{{Name: "A", ID: 1}, {Name: "B", ID: 2}, {Name: "C", ID: 3}}
			host.KVStoreMock.On("Get", "simi:6452").Return(mustMarshal(cached), true, nil)

			resp, err := agent.GetSimilarArtists(metadata.SimilarArtistsRequest{
				Name:  "Taylor Swift",
				Limit: 2,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Artists).To(HaveLen(2))
			Expect(resp.Artists[0].Name).To(Equal("A"))
			Expect(resp.Artists[1].Name).To(Equal("B"))
		})

		It("returns nil when no similar artists available", func() {
			host.KVStoreMock.On("Get", "simi:6452").Return(mustMarshal([]neteaseArtist{}), true, nil)

			resp, err := agent.GetSimilarArtists(metadata.SimilarArtistsRequest{
				Name: "Taylor Swift",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp).To(BeNil())
		})
	})

	Describe("GetArtistTopSongs", func() {
		var agent neteaseMusicAgent

		BeforeEach(func() {
			setupArtistCache()
		})

		It("returns cached top songs", func() {
			cached := metadata.TopSongsResponse{Songs: []metadata.SongRef{{Name: "晴天", Artist: "周杰伦"}}}
			host.KVStoreMock.On("Get", "topsongs:6452:10").Return(mustMarshal(cached), true, nil)

			resp, err := agent.GetArtistTopSongs(metadata.TopSongsRequest{
				Name: "Taylor Swift",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Songs).To(HaveLen(1))
			Expect(resp.Songs[0].Name).To(Equal("晴天"))
		})

		It("fetches top songs and caches them", func() {
			host.KVStoreMock.On("Get", "topsongs:6452:10").Return([]byte(nil), false, nil)
			mockAPIConfig()

			var topResp neteaseTopSongResponse
			topResp.Code = 200
			topResp.Songs = append(topResp.Songs, struct {
				Name    string `json:"name"`
				Artists []struct {
					Name string `json:"name"`
				} `json:"ar"`
			}{Name: "晴天"})
			mockHTTPJSON("/artist/top/song?id=6452", topResp)

			host.KVStoreMock.On("SetWithTTL", "topsongs:6452:10", mock.Anything, mock.Anything).Return(nil)

			resp, err := agent.GetArtistTopSongs(metadata.TopSongsRequest{
				Name: "Taylor Swift",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Songs).To(HaveLen(1))
			Expect(resp.Songs[0].Name).To(Equal("晴天"))
		})
	})

	Describe("GetAlbumImages", func() {
		var agent neteaseMusicAgent

		It("returns album artwork in multiple sizes", func() {
			cached := cachedAlbumMatch{AlbumID: 18915, ArtworkURL: "http://p3.music.126.net/a/1.jpg"}
			host.KVStoreMock.On("Get", "album:taylor swift:fearless").Return(mustMarshal(cached), true, nil)

			resp, err := agent.GetAlbumImages(metadata.AlbumRequest{Name: "Fearless", Artist: "Taylor Swift"})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.Images).To(HaveLen(3))
			Expect(resp.Images[0].URL).To(Equal("https://p3.music.126.net/a/1.jpg?param=1500y1500"))
		})

		It("returns nil when no artwork found", func() {
			host.KVStoreMock.On("Get", "album:taylor swift:unknown").Return(mustMarshal(cachedAlbumMatch{}), true, nil)

			resp, err := agent.GetAlbumImages(metadata.AlbumRequest{Name: "Unknown", Artist: "Taylor Swift"})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp).To(BeNil())
		})
	})

	Describe("GetAlbumInfo", func() {
		var agent neteaseMusicAgent

		It("returns cached album info", func() {
			cached := cachedAlbumInfo{URL: "https://music.163.com/#/album?id=18915", Description: "desc"}
			host.KVStoreMock.On("Get", "album_info:taylor swift:fearless").Return(mustMarshal(cached), true, nil)

			resp, err := agent.GetAlbumInfo(metadata.AlbumRequest{Name: "Fearless", Artist: "Taylor Swift"})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.URL).To(Equal("https://music.163.com/#/album?id=18915"))
			Expect(resp.Description).To(Equal("desc"))
		})

		It("returns nil for cached empty info", func() {
			host.KVStoreMock.On("Get", "album_info:taylor swift:unknown").Return(mustMarshal(cachedAlbumInfo{}), true, nil)

			resp, err := agent.GetAlbumInfo(metadata.AlbumRequest{Name: "Unknown", Artist: "Taylor Swift"})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp).To(BeNil())
		})

		It("resolves the match, fetches the description and caches the info", func() {
			host.KVStoreMock.On("Get", "album_info:taylor swift:fearless").Return([]byte(nil), false, nil)
			match := cachedAlbumMatch{AlbumID: 18915, ArtworkURL: "https://img/a.jpg"}
			host.KVStoreMock.On("Get", "album:taylor swift:fearless").Return(mustMarshal(match), true, nil)
			mockAPIConfig()
			host.ConfigMock.On("GetInt", configCacheTTLDays).Return(int64(7), true)

			var albumResp neteaseAlbumDetailResponse
			albumResp.Code = 200
			albumResp.Album.ID = 18915
			albumResp.Album.Description = "editorial notes"
			mockHTTPJSON("/album?id=18915", albumResp)

			host.KVStoreMock.On("SetWithTTL", "album_info:taylor swift:fearless", mock.Anything, mock.Anything).Return(nil)

			resp, err := agent.GetAlbumInfo(metadata.AlbumRequest{Name: "Fearless", Artist: "Taylor Swift"})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.URL).To(Equal("https://music.163.com/#/album?id=18915"))
			Expect(resp.Description).To(Equal("editorial notes"))
		})

		It("returns the URL without caching when the description fetch fails", func() {
			host.KVStoreMock.On("Get", "album_info:taylor swift:fearless").Return([]byte(nil), false, nil)
			match := cachedAlbumMatch{AlbumID: 18915}
			host.KVStoreMock.On("Get", "album:taylor swift:fearless").Return(mustMarshal(match), true, nil)
			mockAPIConfig()

			host.HTTPMock.On("Send", mock.Anything).Return(&host.HTTPResponse{StatusCode: 500, Body: []byte("err")}, nil)

			resp, err := agent.GetAlbumInfo(metadata.AlbumRequest{Name: "Fearless", Artist: "Taylor Swift"})
			Expect(err).ToNot(HaveOccurred())
			Expect(resp.URL).To(Equal("https://music.163.com/#/album?id=18915"))
			Expect(resp.Description).To(BeEmpty())
			// No SetWithTTL registered: the test fails if the info gets cached.
		})
	})
})
