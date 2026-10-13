package vrsmash

import (
	"errors"
	"testing"

	"github.com/xbapps/xbvr/pkg/config"
)

// A missing cookie source (no Firefox, no collector config) must not block a
// download: the paid URLs are self-authorizing.
func TestDownloadHeadersWithoutCookieSource(t *testing.T) {
	origJar, origCfg := jarFn, config.Config
	defer func() {
		jarFn, config.Config = origJar, origCfg
		resetTokenCache()
	}()

	jarFn = func() (map[string]string, error) { return nil, errors.New("no firefox cookie database") }
	config.Config.VRSMashDownloader.AutoRefreshToken = false
	config.Config.VRSMashDownloader.RefreshToken = ""
	resetTokenCache()

	headers, err := downloadHeaders()
	if err != nil {
		t.Fatalf("downloadHeaders must not fail without a cookie source: %v", err)
	}
	if headers["User-Agent"] == "" {
		t.Error("expected a user-agent header")
	}
	if _, ok := headers["Cookie"]; ok {
		t.Error("did not expect a cookie header when there is no jar")
	}
}

func TestCookieSourceToken(t *testing.T) {
	origCfg := config.Config
	defer func() { config.Config = origCfg }()

	// Use a key that does not exist so the collector config is empty.
	config.Config.VRSMashDownloader.CookieConfigKey = "vrsmash.com-test-nonexistent-config"
	config.Config.VRSMashDownloader.AutoRefreshToken = true
	config.Config.VRSMashDownloader.RefreshToken = "rt"
	config.Config.VRSMashDownloader.UseFirefoxCookies = true

	if got := CookieSource(); got != "token" {
		t.Errorf("CookieSource = %q, want token", got)
	}
}
