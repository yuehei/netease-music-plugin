package main

import (
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/stretchr/testify/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("album", func() {
	Describe("extractBaseName", func() {
		It("strips parenthesized suffix", func() {
			Expect(extractBaseName("范特西 (deluxe)")).To(Equal("范特西"))
		})

		It("strips bracketed suffix", func() {
			Expect(extractBaseName("album [remastered]")).To(Equal("album"))
		})

		It("strips dash suffix", func() {
			Expect(extractBaseName("album - single")).To(Equal("album"))
		})

		It("keeps name without delimiters unchanged", func() {
			Expect(extractBaseName("范特西")).To(Equal("范特西"))
		})
	})

	Describe("findBestAlbumMatch", func() {
		It("returns exact match on album name", func() {
			results := []neteaseAlbum{
				{Name: "Fantasy", ID: 1},
				{Name: "范特西", ID: 2},
			}
			match := findBestAlbumMatch("范特西", results)
			Expect(match).ToNot(BeNil())
			Expect(match.ID).To(Equal(int64(2)))
		})

		It("matches case-insensitively", func() {
			results := []neteaseAlbum{{Name: "Fantasy", ID: 1}}
			match := findBestAlbumMatch("fantasy", results)
			Expect(match).ToNot(BeNil())
			Expect(match.ID).To(Equal(int64(1)))
		})

		It("matches on base name when full names differ", func() {
			results := []neteaseAlbum{
				{Name: "Thriller (25th Anniversary)", ID: 1},
			}
			match := findBestAlbumMatch("Thriller", results)
			Expect(match).ToNot(BeNil())
			Expect(match.ID).To(Equal(int64(1)))
		})

		It("matches by containment for long enough names", func() {
			results := []neteaseAlbum{
				{Name: "The Wall Live", ID: 1},
			}
			match := findBestAlbumMatch("The Wall", results)
			Expect(match).ToNot(BeNil())
			Expect(match.ID).To(Equal(int64(1)))
		})

		It("does not match by containment for very short names", func() {
			results := []neteaseAlbum{
				{Name: "Abba Gold", ID: 1},
			}
			match := findBestAlbumMatch("Abb", results)
			Expect(match).To(BeNil())
		})

		It("returns nil when nothing matches", func() {
			results := []neteaseAlbum{
				{Name: "Something Else", ID: 1},
			}
			Expect(findBestAlbumMatch("范特西", results)).To(BeNil())
		})
	})

	Describe("resolveAlbumMatch", func() {
		It("returns cached match", func() {
			cached := cachedAlbumMatch{AlbumID: 18915, ArtworkURL: "https://img/album.jpg"}
			host.KVStoreMock.On("Get", "album:周杰伦:范特西").Return(mustMarshal(cached), true, nil)

			match, err := resolveAlbumMatch("范特西", "周杰伦")
			Expect(err).ToNot(HaveOccurred())
			Expect(match.AlbumID).To(Equal(int64(18915)))
			Expect(match.ArtworkURL).To(Equal("https://img/album.jpg"))
		})

		It("returns nil on negative cache hit", func() {
			host.KVStoreMock.On("Get", "album:周杰伦:不存在").Return(mustMarshal(cachedAlbumMatch{}), true, nil)

			match, err := resolveAlbumMatch("不存在", "周杰伦")
			Expect(err).ToNot(HaveOccurred())
			Expect(match).To(BeNil())
		})

		It("returns nil when the artist is not found", func() {
			mockArtistMatchConfig()
			host.KVStoreMock.On("Get", "album:nobody:范特西").Return([]byte(nil), false, nil)
			host.KVStoreMock.On("Get", "artist:nobody").Return(mustMarshal(cachedArtistID{ArtistID: 0}), true, nil)

			match, err := resolveAlbumMatch("范特西", "Nobody")
			Expect(err).ToNot(HaveOccurred())
			Expect(match).To(BeNil())
		})

		It("looks up artist albums and caches the match", func() {
			host.KVStoreMock.On("Get", "album:周杰伦:范特西").Return([]byte(nil), false, nil)
			host.KVStoreMock.On("Get", "artist:周杰伦").Return(mustMarshal(cachedArtistID{ArtistID: testArtistID}), true, nil)
			mockAPIConfig()
			host.ConfigMock.On("GetInt", configCacheTTLDays).Return(int64(7), true)

			albumsResp := neteaseArtistAlbumsResponse{
				Code: 200,
				HotAlbums: []neteaseAlbum{
					{Name: "叶惠美", ID: 18917, PicURL: "https://img/yhm.jpg"},
					{Name: "范特西", ID: 18915, PicURL: "https://img/ftx.jpg"},
				},
			}
			mockHTTPJSON("/artist/album?id=6452", albumsResp)

			host.KVStoreMock.On("SetWithTTL", "album:周杰伦:范特西", mock.Anything, mock.Anything).Return(nil)

			match, err := resolveAlbumMatch("范特西", "周杰伦")
			Expect(err).ToNot(HaveOccurred())
			Expect(match.AlbumID).To(Equal(int64(18915)))
			Expect(match.ArtworkURL).To(Equal("https://img/ftx.jpg"))
		})

		It("caches a negative result when no album matches", func() {
			host.KVStoreMock.On("Get", "album:周杰伦:不存在").Return([]byte(nil), false, nil)
			host.KVStoreMock.On("Get", "artist:周杰伦").Return(mustMarshal(cachedArtistID{ArtistID: testArtistID}), true, nil)
			mockAPIConfig()

			albumsResp := neteaseArtistAlbumsResponse{
				Code:      200,
				HotAlbums: []neteaseAlbum{{Name: "叶惠美", ID: 18917}},
			}
			mockHTTPJSON("/artist/album?id=6452", albumsResp)

			host.KVStoreMock.On("SetWithTTL", "album:周杰伦:不存在",
				mustMarshal(cachedAlbumMatch{}), int64(negativeCacheTTLSeconds)).Return(nil)

			match, err := resolveAlbumMatch("不存在", "周杰伦")
			Expect(err).ToNot(HaveOccurred())
			Expect(match).To(BeNil())
		})

		It("refreshes legacy cache entries that lack the album ID", func() {
			legacy := cachedAlbumMatch{ArtworkURL: "https://img/old.jpg"}
			host.KVStoreMock.On("Get", "album:周杰伦:范特西").Return(mustMarshal(legacy), true, nil)
			host.KVStoreMock.On("Get", "artist:周杰伦").Return(mustMarshal(cachedArtistID{ArtistID: testArtistID}), true, nil)
			mockAPIConfig()
			host.ConfigMock.On("GetInt", configCacheTTLDays).Return(int64(7), true)

			albumsResp := neteaseArtistAlbumsResponse{
				Code:      200,
				HotAlbums: []neteaseAlbum{{Name: "范特西", ID: 18915, PicURL: "https://img/ftx.jpg"}},
			}
			mockHTTPJSON("/artist/album?id=6452", albumsResp)

			host.KVStoreMock.On("SetWithTTL", "album:周杰伦:范特西", mock.Anything, mock.Anything).Return(nil)

			match, err := resolveAlbumMatch("范特西", "周杰伦")
			Expect(err).ToNot(HaveOccurred())
			Expect(match.AlbumID).To(Equal(int64(18915)))
		})
	})

	Describe("fetchAlbumDescription", func() {
		It("returns the normalized description", func() {
			mockAPIConfig()

			var albumResp neteaseAlbumDetailResponse
			albumResp.Code = 200
			albumResp.Album.ID = 18915
			albumResp.Album.Description = "「范特西」  专辑\n第二段"
			mockHTTPJSON("/album?id=18915", albumResp)

			desc, ok := fetchAlbumDescription(18915)
			Expect(ok).To(BeTrue())
			Expect(desc).To(Equal("「范特西」 专辑\n第二段"))
		})

		It("reports failure when the API reports a non-200 code", func() {
			mockAPIConfig()
			mockHTTPJSON("/album?id=18915", neteaseAlbumDetailResponse{Code: 404})

			_, ok := fetchAlbumDescription(18915)
			Expect(ok).To(BeFalse())
		})

		It("reports failure when the request errors", func() {
			mockAPIConfig()
			host.HTTPMock.On("Send", mock.Anything).Return(&host.HTTPResponse{StatusCode: 500, Body: []byte("err")}, nil)

			_, ok := fetchAlbumDescription(18915)
			Expect(ok).To(BeFalse())
		})
	})
})
