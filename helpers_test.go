package main

import (
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/stretchr/testify/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("helpers", func() {
	Describe("getAPIEndpoints", func() {
		It("returns empty when config not set", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("", false)
			Expect(getAPIEndpoints()).To(BeEmpty())
		})

		It("returns empty when config is blank", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("  \n \n", true)
			Expect(getAPIEndpoints()).To(BeEmpty())
		})

		It("parses single endpoint", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://a.example.com", true)
			Expect(getAPIEndpoints()).To(Equal([]string{"https://a.example.com"}))
		})

		It("parses multiple newline-separated endpoints", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://a.example.com\nhttps://b.example.com\nhttps://c.example.com", true)
			Expect(getAPIEndpoints()).To(Equal([]string{
				"https://a.example.com",
				"https://b.example.com",
				"https://c.example.com",
			}))
		})

		It("trims spaces and skips empty lines", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("  https://a.example.com \n\n\nhttps://b.example.com\n  ", true)
			Expect(getAPIEndpoints()).To(Equal([]string{"https://a.example.com", "https://b.example.com"}))
		})

		It("strips trailing slashes", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://a.example.com/\nhttps://b.example.com//", true)
			Expect(getAPIEndpoints()).To(Equal([]string{"https://a.example.com", "https://b.example.com"}))
		})
	})

	Describe("shuffledAPIEndpoints", func() {
		It("returns empty when nothing is configured", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("", false)
			Expect(shuffledAPIEndpoints()).To(BeEmpty())
		})

		It("returns the only endpoint when one is configured", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://only.example.com", true)
			Expect(shuffledAPIEndpoints()).To(Equal([]string{"https://only.example.com"}))
		})

		It("returns every configured endpoint exactly once", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://a.example.com\nhttps://b.example.com\nhttps://c.example.com", true)
			for i := 0; i < 20; i++ {
				Expect(shuffledAPIEndpoints()).To(ConsistOf(
					"https://a.example.com",
					"https://b.example.com",
					"https://c.example.com",
				))
			}
		})
	})

	Describe("getMusicU", func() {
		It("returns empty when config not set", func() {
			host.ConfigMock.On("Get", configMusicU).Return("", false)
			Expect(getMusicU()).To(Equal(""))
		})

		It("returns trimmed value when set", func() {
			host.ConfigMock.On("Get", configMusicU).Return("  secret-token  ", true)
			Expect(getMusicU()).To(Equal("secret-token"))
		})
	})

	Describe("apiGet", func() {
		It("returns a clear error when no endpoint is configured", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("", false)

			var out struct{}
			err := apiGet("/test", &out)
			Expect(err).To(MatchError(errNoAPIEndpoint))
		})

		It("joins endpoint and path, and unmarshals the response", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://api.example.com", true)
			host.ConfigMock.On("Get", configMusicU).Return("", false)
			host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
				return req.Method == "GET" && req.URL == "https://api.example.com/test?a=1"
			})).Return(&host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":200,"value":"ok"}`)}, nil)

			var out struct {
				Code  int    `json:"code"`
				Value string `json:"value"`
			}
			Expect(apiGet("/test?a=1", &out)).To(Succeed())
			Expect(out.Code).To(Equal(200))
			Expect(out.Value).To(Equal("ok"))
		})

		It("appends the cookie parameter when MUSIC_U is configured", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://api.example.com", true)
			host.ConfigMock.On("Get", configMusicU).Return("secret-token", true)
			host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
				return strings.HasPrefix(req.URL, "https://api.example.com/test?a=1&") &&
					strings.Contains(req.URL, "cookie=MUSIC_U%3Dsecret-token")
			})).Return(&host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":200}`)}, nil)

			var out struct {
				Code int `json:"code"`
			}
			Expect(apiGet("/test?a=1", &out)).To(Succeed())
		})

		It("omits the cookie parameter when MUSIC_U is not configured", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://api.example.com", true)
			host.ConfigMock.On("Get", configMusicU).Return("", false)
			host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
				return req.URL == "https://api.example.com/test"
			})).Return(&host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":200}`)}, nil)

			var out struct {
				Code int `json:"code"`
			}
			Expect(apiGet("/test", &out)).To(Succeed())
		})

		It("returns error on non-200 status", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://api.example.com", true)
			host.ConfigMock.On("Get", configMusicU).Return("", false)
			host.HTTPMock.On("Send", mock.Anything).Return(&host.HTTPResponse{StatusCode: 502, Body: []byte("bad gateway")}, nil)

			var out struct{}
			Expect(apiGet("/test", &out)).To(HaveOccurred())
		})

		It("returns error on invalid JSON", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://api.example.com", true)
			host.ConfigMock.On("Get", configMusicU).Return("", false)
			host.HTTPMock.On("Send", mock.Anything).Return(&host.HTTPResponse{StatusCode: 200, Body: []byte("not json")}, nil)

			var out struct{}
			Expect(apiGet("/test", &out)).To(HaveOccurred())
		})

		It("falls through to the next endpoint when one is down", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://down.example.com\nhttps://ok.example.com", true)
			host.ConfigMock.On("Get", configMusicU).Return("", false)
			host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
				return strings.HasPrefix(req.URL, "https://down.example.com")
			})).Return(&host.HTTPResponse{StatusCode: 502, Body: []byte("bad gateway")}, nil)
			host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
				return strings.HasPrefix(req.URL, "https://ok.example.com")
			})).Return(&host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":200,"value":"ok"}`)}, nil)

			var out struct {
				Code  int    `json:"code"`
				Value string `json:"value"`
			}
			Expect(apiGet("/test", &out)).To(Succeed())
			Expect(out.Value).To(Equal("ok"))
		})

		It("falls through to the next endpoint on rate-limit codes", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://limited.example.com\nhttps://ok.example.com", true)
			host.ConfigMock.On("Get", configMusicU).Return("", false)
			host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
				return strings.HasPrefix(req.URL, "https://limited.example.com")
			})).Return(&host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":460}`)}, nil)
			host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
				return strings.HasPrefix(req.URL, "https://ok.example.com")
			})).Return(&host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":200,"value":"ok"}`)}, nil)

			var out struct {
				Code  int    `json:"code"`
				Value string `json:"value"`
			}
			Expect(apiGet("/test", &out)).To(Succeed())
			Expect(out.Value).To(Equal("ok"))
		})

		It("returns error when every endpoint is rate-limited", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://a.example.com\nhttps://b.example.com", true)
			host.ConfigMock.On("Get", configMusicU).Return("", false)
			host.HTTPMock.On("Send", mock.Anything).Return(&host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":462}`)}, nil)

			var out struct{}
			err := apiGet("/test", &out)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("rate limited"))
		})

		It("tries at most maxEndpointAttempts mirrors even when more are configured", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://a.example.com\nhttps://b.example.com\nhttps://c.example.com", true)
			host.ConfigMock.On("Get", configMusicU).Return("", false)
			host.HTTPMock.On("Send", mock.Anything).Return(&host.HTTPResponse{StatusCode: 502, Body: []byte("bad gateway")}, nil)

			var out struct{}
			Expect(apiGet("/test", &out)).To(HaveOccurred())
			host.HTTPMock.AssertNumberOfCalls(GinkgoT(), "Send", 2)
		})

		It("does not treat non-retryable API codes as endpoint failure", func() {
			host.ConfigMock.On("Get", configAPIEndpoints).Return("https://api.example.com", true)
			host.ConfigMock.On("Get", configMusicU).Return("", false)
			host.HTTPMock.On("Send", mock.Anything).Return(&host.HTTPResponse{StatusCode: 200, Body: []byte(`{"code":301}`)}, nil)

			var out struct {
				Code int `json:"code"`
			}
			// 301 (login required) is surfaced to the caller, not retried.
			Expect(apiGet("/test", &out)).To(Succeed())
			Expect(out.Code).To(Equal(301))
		})
	})

	Describe("isEnabled", func() {
		BeforeEach(func() {
			// Clear default enable_* mocks so we can set specific expectations
			host.ConfigMock.ExpectedCalls = nil
			host.ConfigMock.Calls = nil
		})

		It("returns true when config not set (default enabled)", func() {
			host.ConfigMock.On("Get", configArtistURL).Return("", false)
			Expect(isEnabled(configArtistURL)).To(BeTrue())
		})

		It("returns true when config is true", func() {
			host.ConfigMock.On("Get", configArtistURL).Return("true", true)
			Expect(isEnabled(configArtistURL)).To(BeTrue())
		})

		It("returns false when config is false", func() {
			host.ConfigMock.On("Get", configArtistURL).Return("false", true)
			Expect(isEnabled(configArtistURL)).To(BeFalse())
		})
	})

	Describe("getCacheTTLSeconds", func() {
		It("returns default TTL when config not set", func() {
			host.ConfigMock.On("GetInt", configCacheTTLDays).Return(int64(0), false)
			Expect(getCacheTTLSeconds()).To(Equal(int64(7 * 24 * 60 * 60)))
		})

		It("returns default TTL when config is zero", func() {
			host.ConfigMock.On("GetInt", configCacheTTLDays).Return(int64(0), true)
			Expect(getCacheTTLSeconds()).To(Equal(int64(7 * 24 * 60 * 60)))
		})

		It("returns configured TTL in seconds", func() {
			host.ConfigMock.On("GetInt", configCacheTTLDays).Return(int64(14), true)
			Expect(getCacheTTLSeconds()).To(Equal(int64(14 * 24 * 60 * 60)))
		})
	})

	Describe("normalizeName", func() {
		It("lowercases and trims", func() {
			Expect(normalizeName("  Taylor Swift  ")).To(Equal("taylor swift"))
		})

		It("handles empty string", func() {
			Expect(normalizeName("")).To(Equal(""))
		})
	})

	Describe("normalizeText", func() {
		It("collapses tab characters between words into single spaces", func() {
			Expect(normalizeText("In\tHet\tMidden\tVan\tAlles")).To(Equal("In Het Midden Van Alles"))
		})

		It("trims leading and trailing whitespace", func() {
			Expect(normalizeText("  \t hello world \n ")).To(Equal("hello world"))
		})

		It("preserves paragraph breaks while collapsing in-line whitespace", func() {
			Expect(normalizeText("Para\tone\there.\n\nPara\ttwo\tends.")).
				To(Equal("Para one here.\n\nPara two ends."))
		})

		It("normalizes CRLF/CR line endings to LF", func() {
			Expect(normalizeText("one\r\ntwo\rthree")).To(Equal("one\ntwo\nthree"))
		})

		It("handles empty string", func() {
			Expect(normalizeText("")).To(Equal(""))
		})
	})

	Describe("kvGet", func() {
		It("returns cached value", func() {
			data := mustMarshal(cachedArtistID{ArtistID: 12345})
			host.KVStoreMock.On("Get", "artist:test").Return(data, true, nil)
			var result cachedArtistID
			ok := kvGet("artist:test", &result)
			Expect(ok).To(BeTrue())
			Expect(result.ArtistID).To(Equal(int64(12345)))
		})

		It("returns false when key not found", func() {
			host.KVStoreMock.On("Get", "artist:missing").Return([]byte(nil), false, nil)
			var result cachedArtistID
			ok := kvGet("artist:missing", &result)
			Expect(ok).To(BeFalse())
		})

		It("returns false on invalid JSON", func() {
			host.KVStoreMock.On("Get", "artist:bad").Return([]byte("invalid"), true, nil)
			var result cachedArtistID
			ok := kvGet("artist:bad", &result)
			Expect(ok).To(BeFalse())
		})
	})

	Describe("kvSet", func() {
		It("marshals and stores value", func() {
			expected := mustMarshal(cachedArtistID{ArtistID: 999})
			host.KVStoreMock.On("Set", "key", expected).Return(nil)
			err := kvSet("key", cachedArtistID{ArtistID: 999})
			Expect(err).ToNot(HaveOccurred())
			host.KVStoreMock.AssertCalled(GinkgoT(), "Set", "key", expected)
		})
	})

	Describe("kvSetWithTTL", func() {
		It("marshals and stores value with TTL", func() {
			expected := mustMarshal(cachedArtistID{ArtistID: 999})
			host.KVStoreMock.On("SetWithTTL", "key", expected, int64(3600)).Return(nil)
			err := kvSetWithTTL("key", cachedArtistID{ArtistID: 999}, 3600)
			Expect(err).ToNot(HaveOccurred())
			host.KVStoreMock.AssertCalled(GinkgoT(), "SetWithTTL", "key", expected, int64(3600))
		})
	})

	Describe("httpGet", func() {
		It("sends GET request with user agent", func() {
			host.HTTPMock.On("Send", mock.MatchedBy(func(req host.HTTPRequest) bool {
				return req.Method == "GET" &&
					req.URL == "https://example.com/test" &&
					req.Headers["User-Agent"] == userAgent &&
					req.TimeoutMs == httpTimeoutMs
			})).Return(&host.HTTPResponse{
				StatusCode: 200,
				Body:       []byte("response"),
			}, nil)

			body, status, err := httpGet("https://example.com/test")
			Expect(err).ToNot(HaveOccurred())
			Expect(status).To(Equal(int32(200)))
			Expect(body).To(Equal([]byte("response")))
		})
	})

	Describe("resizeImageURL", func() {
		It("appends the param size argument and forces https", func() {
			url := "http://p4.music.126.net/abcdef/12345.jpg"
			Expect(resizeImageURL(url, 600)).To(Equal("https://p4.music.126.net/abcdef/12345.jpg?param=600y600"))
		})

		It("replaces an existing query string", func() {
			url := "https://p4.music.126.net/abcdef/12345.jpg?param=100y100&x=1"
			Expect(resizeImageURL(url, 300)).To(Equal("https://p4.music.126.net/abcdef/12345.jpg?param=300y300"))
		})

		It("returns unparseable URLs unchanged", func() {
			url := "ht%tp://bad url"
			Expect(resizeImageURL(url, 300)).To(Equal(url))
		})
	})

	Describe("buildImageList", func() {
		It("builds three sizes via the param argument", func() {
			images := buildImageList("http://p3.music.126.net/a/1.jpg")
			Expect(images).To(HaveLen(3))
			Expect(images[0].Size).To(Equal(int32(1500)))
			Expect(images[0].URL).To(Equal("https://p3.music.126.net/a/1.jpg?param=1500y1500"))
			Expect(images[1].Size).To(Equal(int32(600)))
			Expect(images[1].URL).To(ContainSubstring("param=600y600"))
			Expect(images[2].Size).To(Equal(int32(300)))
			Expect(images[2].URL).To(ContainSubstring("param=300y300"))
		})
	})
})
