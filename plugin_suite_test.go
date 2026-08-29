package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/stretchr/testify/mock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestNeteaseMusicPlugin(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Netease Music Plugin Suite")
}

var _ = BeforeEach(resetMocks)

// resetMocks clears all host mocks and re-registers the default capability config.
// Called from a package-level BeforeEach so every spec starts with fresh state.
func resetMocks() {
	pdk.ResetMock()
	host.ConfigMock.ExpectedCalls = nil
	host.ConfigMock.Calls = nil
	host.KVStoreMock.ExpectedCalls = nil
	host.KVStoreMock.Calls = nil
	host.HTTPMock.ExpectedCalls = nil
	host.HTTPMock.Calls = nil
	host.CacheMock.ExpectedCalls = nil
	host.CacheMock.Calls = nil
	pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()

	// Reset the in-memory artist overrides cache between specs (including
	// the injectable clock used for its check interval).
	artistOverrideCache = artistOverrides{}
	overrideNow = time.Now

	// Default all capabilities to enabled (not set = enabled)
	host.ConfigMock.On("Get", configArtistURL).Return("", false).Maybe()
	host.ConfigMock.On("Get", configArtistBiography).Return("", false).Maybe()
	host.ConfigMock.On("Get", configArtistImages).Return("", false).Maybe()
	host.ConfigMock.On("Get", configSimilarArtists).Return("", false).Maybe()
	host.ConfigMock.On("Get", configTopSongs).Return("", false).Maybe()
	host.ConfigMock.On("Get", configAlbumImages).Return("", false).Maybe()
	host.ConfigMock.On("Get", configAlbumInfo).Return("", false).Maybe()
	// NOTE: artist_exact_match / artist_id_overrides / enable_lyrics are
	// intentionally NOT defaulted here — they are opt-in (manifest default
	// false), and testify matches expectations in registration order, so a
	// default here would shadow spec-level registrations of the same key.
	// Register them per-spec (mockArtistMatchConfig / mockAPIConfig /
	// lyrics specs).
}
func mustMarshal(v any) []byte {
	data, err := json.Marshal(v)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	return data
}
