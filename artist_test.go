package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/stretchr/testify/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const testArtistID = int64(6452)

// mockAPIConfig registers the API endpoint (no cookie) used by apiGet,
// plus neutral defaults for the artist-matching config keys. Specs needing
// non-default values must register their own expectations BEFORE calling
// this — testify matches the FIRST registered expectation for a key.
func mockAPIConfig() {
	mockArtistMatchConfig()
	host.ConfigMock.On("Get", configAPIEndpoints).Return("https://api.example.com", true)
	host.ConfigMock.On("Get", configMusicU).Return("", false)
}

// mockArtistMatchConfig registers neutral defaults for artist_exact_match
// (off) and artist_id_overrides (unset). Needed by any spec whose code path
// reaches resolveArtistID without going through mockAPIConfig. Also covers
// the host-Cache calls these paths perform (override state + inflight
// markers). Specs needing specific inflight behavior must register their own
// expectations BEFORE calling this — testify matches the FIRST registered
// expectation for a key.
func mockArtistMatchConfig() {
	host.ConfigMock.On("Get", configArtistExactMatch).Return("", false).Maybe()
	host.ConfigMock.On("Get", configArtistIDOverride).Return("", false).Maybe()
	host.CacheMock.On("GetBytes", overrideStateKey).Return([]byte(nil), false, nil).Maybe()
	host.CacheMock.On("Remove", overrideStateKey).Return(nil).Maybe()
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

	// setupOverridesFile writes an overrides JSON file and points both the
	// override config and the mount resolver at it, so the cache loads it
	// from a "library" the same way production does. Host-Cache expectations:
	// no shared state yet (miss), writes accepted.
	setupOverridesFile := func(content string) string {
		path := filepath.Join(GinkgoTB().TempDir(), "artist-ids.json")
		ExpectWithOffset(1, os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
		host.ConfigMock.On("Get", configArtistIDOverride).Return(path, true)
		host.CacheMock.On("GetBytes", overrideStateKey).Return([]byte(nil), false, nil)
		host.CacheMock.On("SetBytes", overrideStateKey, mock.Anything, int64(overrideStateTTL)).Return(nil)
		host.CacheMock.On("Remove", overrideStateKey).Return(nil).Maybe()
		origResolver := resolveOverrideMounts
		// In tests the configured path is already an absolute temp-file path,
		// so the "mount" resolver just maps it to itself.
		resolveOverrideMounts = func(rel string) []string { return []string{rel} }
		DeferCleanup(func() { resolveOverrideMounts = origResolver })
		return path
	}

	Describe("parseArtistOverrides", func() {
		It("maps numeric and string IDs, splitting aliases and normalizing keys", func() {
			m, err := parseArtistOverrides([]byte(`{" 周杰伦 ;Jay Chou": 6452, "Radiohead": "72161"}`))
			Expect(err).ToNot(HaveOccurred())
			Expect(m).To(Equal(map[string]int64{
				"周杰伦":     6452,
				"jay chou":  6452,
				"radiohead": 72161,
			}))
		})

		It("skips invalid ID values and empty aliases", func() {
			m, err := parseArtistOverrides([]byte(`{"周杰伦": "abc", "林俊杰": 0, ";;": 5, "五月天": 11065}`))
			Expect(err).ToNot(HaveOccurred())
			Expect(m).To(Equal(map[string]int64{"五月天": 11065}))
		})

		It("rejects malformed JSON", func() {
			_, err := parseArtistOverrides([]byte("{not json"))
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("artistOverrideCache", func() {
		It("loads the configured file into memory on first use", func() {
			setupOverridesFile(`{"周杰伦": 6452}`)

			id, ok := artistOverrideCache.get("周杰伦")
			Expect(ok).To(BeTrue())
			Expect(id).To(Equal(int64(6452)))
		})

		It("returns false when config is unset or blank", func() {
			host.CacheMock.On("Remove", overrideStateKey).Return(nil)
			host.ConfigMock.On("Get", configArtistIDOverride).Return("", false)
			_, ok := artistOverrideCache.get("周杰伦")
			Expect(ok).To(BeFalse())

			host.ConfigMock.ExpectedCalls = nil
			host.ConfigMock.Calls = nil
			host.ConfigMock.On("Get", configArtistIDOverride).Return("  ", true)
			_, ok = artistOverrideCache.get("周杰伦")
			Expect(ok).To(BeFalse())
		})

		It("serves nothing when the file is missing", func() {
			missing := filepath.Join(GinkgoTB().TempDir(), "missing.json")
			host.ConfigMock.On("Get", configArtistIDOverride).Return(missing, true)
			host.CacheMock.On("GetBytes", overrideStateKey).Return([]byte(nil), false, nil)
			host.CacheMock.On("SetBytes", overrideStateKey, mock.Anything, int64(overrideStateTTL)).Return(nil)

			_, ok := artistOverrideCache.get("周杰伦")
			Expect(ok).To(BeFalse())
		})

		It("hydrates the shared state from the host cache without touching the file", func() {
			// Simulate a fresh wasm instance (Navidrome creates one per call)
			// finding the state another instance published to the host Cache.
			path := filepath.Join(GinkgoTB().TempDir(), "artist-ids.json")
			st := overrideState{
				Path:      path,
				AbsPath:   path,
				ModTime:   time.Now().Add(-time.Minute).UnixNano(),
				Size:      42,
				LastCheck: time.Now().UnixNano(), // fresh: interval not due
				M:         map[string]int64{"周杰伦": 6452},
			}
			host.ConfigMock.On("Get", configArtistIDOverride).Return(path, true)
			host.CacheMock.On("GetBytes", overrideStateKey).Return(mustMarshal(st), true, nil)

			// No file is written and no SetBytes expectation is registered:
			// an unexpected call would panic the mock.

			id, ok := artistOverrideCache.get("周杰伦")
			Expect(ok).To(BeTrue())
			Expect(id).To(Equal(int64(6452)))
		})

		It("reloads when the config path changes or the file is edited", func() {
			file := setupOverridesFile(`{"周杰伦": 6452}`)
			fi, err := os.Stat(file)
			Expect(err).ToNot(HaveOccurred())

			id, ok := artistOverrideCache.get("周杰伦")
			Expect(ok).To(BeTrue())
			Expect(id).To(Equal(int64(6452)))

			// Edit the file with a newer mtime: within the 60s check window
			// the in-memory copy is served; after it the edit is picked up.
			Expect(os.WriteFile(file, []byte(`{"周杰伦": 111}`), 0o644)).To(Succeed())
			newer := fi.ModTime().Add(time.Hour)
			Expect(os.Chtimes(file, newer, newer)).To(Succeed())

			id, ok = artistOverrideCache.get("周杰伦")
			Expect(ok).To(BeTrue())
			Expect(id).To(Equal(int64(6452))) // throttled: still the old value

			base := overrideNow
			overrideNow = func() time.Time { return base().Add(overrideCheckInterval + time.Second) }

			id, ok = artistOverrideCache.get("周杰伦")
			Expect(ok).To(BeTrue())
			Expect(id).To(Equal(int64(111))) // past the interval: reloaded

			// The reload must have been logged at Info level.
			pdk.PDKMock.AssertCalled(GinkgoTB(), "Log", pdk.LogInfo,
				mock.MatchedBy(func(msg string) bool { return strings.Contains(msg, "reloaded") }))

			// Switch to another file via a config change — takes effect
			// immediately (config compare is not throttled). Clear first:
			// testify matches expectations in registration order.
			other := filepath.Join(GinkgoTB().TempDir(), "other.json")
			Expect(os.WriteFile(other, []byte(`{"林俊杰": 9012}`), 0o644)).To(Succeed())
			host.ConfigMock.ExpectedCalls = nil
			host.ConfigMock.Calls = nil
			host.ConfigMock.On("Get", configArtistIDOverride).Return(other, true)

			_, ok = artistOverrideCache.get("周杰伦")
			Expect(ok).To(BeFalse())
			id, ok = artistOverrideCache.get("林俊杰")
			Expect(ok).To(BeTrue())
			Expect(id).To(Equal(int64(9012)))
		})
	})

	Describe("resolveArtistID with overrides", func() {
		It("returns the override ID and updates a stale cache entry", func() {
			// Cache holds a wrong ID from an earlier fuzzy match; the override
			// must win and overwrite it.
			setupOverridesFile(`{"周杰伦": 6452}`)
			host.KVStoreMock.On("Get", "artist:周杰伦").Return(mustMarshal(cachedArtistID{ArtistID: 111}), true, nil)

			updated := mustMarshal(cachedArtistID{ArtistID: testArtistID})
			host.KVStoreMock.On("Set", "artist:周杰伦", updated).Return(nil)

			id, err := resolveArtistID("周杰伦")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(testArtistID))
		})

		It("does not rewrite the cache when it already matches the override", func() {
			setupOverridesFile(`{"周杰伦": 6452}`)
			host.KVStoreMock.On("Get", "artist:周杰伦").Return(mustMarshal(cachedArtistID{ArtistID: testArtistID}), true, nil)

			// No Set expectation: an unexpected call would panic the mock.

			id, err := resolveArtistID("周杰伦")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(testArtistID))
		})

		It("returns the override ID without searching on cache miss", func() {
			setupOverridesFile(`{"周杰伦": 6452}`)
			host.KVStoreMock.On("Get", "artist:周杰伦").Return([]byte(nil), false, nil)
			host.KVStoreMock.On("Set", "artist:周杰伦", mustMarshal(cachedArtistID{ArtistID: testArtistID})).Return(nil)

			// No HTTP mock registered: an unexpected Send would panic the mock.

			id, err := resolveArtistID("周杰伦")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(testArtistID))
		})

		It("matches a registered alias to the same Netease ID", func() {
			setupOverridesFile(`{"周杰伦;Jay Chou": 6452}`)
			host.KVStoreMock.On("Get", "artist:jay chou").Return([]byte(nil), false, nil)
			host.KVStoreMock.On("Set", "artist:jay chou", mustMarshal(cachedArtistID{ArtistID: testArtistID})).Return(nil)

			id, err := resolveArtistID("Jay Chou")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(testArtistID))
		})
	})

	Describe("resolveArtistID with exact-match-only", func() {
		It("skips fuzzy matches and negative-caches instead of guessing", func() {
			host.KVStoreMock.On("Get", "artist:taylor swift").Return([]byte(nil), false, nil)
			host.ConfigMock.On("Get", configArtistExactMatch).Return("true", true)
			mockAPIConfig()

			searchResp := neteaseArtistSearchResponse{Code: 200}
			searchResp.Result.Artists = []neteaseArtist{{Name: "Taylor", ID: 1}, {Name: "Taylor Swift Fan Club", ID: 2}}
			mockHTTPJSON("/search?keywords=", searchResp)

			// Fuzzy fallback must not persist a wrong ID: negative cache only.
			host.KVStoreMock.On("SetWithTTL", "artist:taylor swift",
				mustMarshal(cachedArtistID{ArtistID: 0}), int64(negativeCacheTTLSeconds)).Return(nil)

			id, err := resolveArtistID("Taylor Swift")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(int64(0)))
		})

		It("still resolves when an exact match exists", func() {
			host.KVStoreMock.On("Get", "artist:taylor swift").Return([]byte(nil), false, nil)
			host.ConfigMock.On("Get", configArtistExactMatch).Return("true", true)
			mockAPIConfig()

			searchResp := neteaseArtistSearchResponse{Code: 200}
			searchResp.Result.Artists = []neteaseArtist{{Name: "Taylor", ID: 1}, {Name: "Taylor Swift", ID: testArtistID}}
			mockHTTPJSON("/search?keywords=", searchResp)

			host.KVStoreMock.On("Set", "artist:taylor swift", mustMarshal(cachedArtistID{ArtistID: testArtistID})).Return(nil)

			id, err := resolveArtistID("Taylor Swift")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(testArtistID))
		})
	})

	Describe("resolveArtistID", func() {
		It("returns cached artist ID", func() {
			mockArtistMatchConfig()
			data := mustMarshal(cachedArtistID{ArtistID: testArtistID})
			host.KVStoreMock.On("Get", "artist:周杰伦").Return(data, true, nil)

			id, err := resolveArtistID("周杰伦")
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(testArtistID))
		})

		It("returns 0 on negative cache hit", func() {
			mockArtistMatchConfig()
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
